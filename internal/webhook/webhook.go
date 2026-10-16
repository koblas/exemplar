// Package webhook receives GitHub App webhooks for exemplar-webhook:
// it verifies X-Hub-Signature-256, stores the raw event in gh_events keyed
// by X-GitHub-Delivery, and enqueues a gh_event job in the same
// transaction, answering 202 well inside GitHub's 10 s window. It has no
// business logic: the orchestrator decides what an event means.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// MaxPayload is GitHub's cap on a webhook payload.
const MaxPayload = 25 << 20

// Enqueuer inserts a job in the caller's transaction; a River client
// (river.Client[pgx.Tx]) implements it through InsertTx.
type Enqueuer interface {
	InsertGHEvent(ctx context.Context, tx pgx.Tx, args store.GHEventArgs) error
}

// Handler is the webhook endpoint.
type Handler struct {
	secret []byte
	st     *store.Store
	enq    Enqueuer
	log    *slog.Logger
}

// New returns a Handler. The secret must be the App's webhook secret.
func New(secret []byte, st *store.Store, enq Enqueuer, log *slog.Logger) (*Handler, error) {
	if len(secret) < 16 {
		return nil, errors.New("webhook: the secret must be at least 16 bytes")
	}
	if st == nil || enq == nil {
		return nil, errors.New("webhook: store and enqueuer are required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{secret: secret, st: st, enq: enq, log: log}, nil
}

// envelope is the little the receiver reads from a payload.
type envelope struct {
	Action       string `json:"action"`
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ServeHTTP verifies, stores and enqueues one delivery.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(github.SHA256SignatureHeader) == "" {
		// go-github would fall back to the legacy SHA-1 header; Exemplar
		// accepts only SHA-256.
		http.Error(w, "missing X-Hub-Signature-256", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxPayload)
	// ValidatePayload checks the HMAC before anything is parsed.
	body, err := github.ValidatePayload(r, h.secret)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		h.log.Warn("webhook rejected", "reason", "signature", "remote", r.RemoteAddr)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	event := github.WebHookType(r)
	delivery, err := uuid.Parse(github.DeliveryID(r))
	if event == "" || err != nil {
		http.Error(w, "missing X-GitHub-Event or X-GitHub-Delivery", http.StatusBadRequest)
		return
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "payload is not JSON", http.StatusBadRequest)
		return
	}
	if env.Installation == nil || env.Installation.ID == 0 {
		// ping and other app-level events carry no installation; there is
		// nothing to review.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	params := queries.InsertGHEventParams{
		DeliveryID: delivery, Event: event, Action: optional(env.Action),
		InstallationID: env.Installation.ID, Payload: body,
	}
	if env.Repository != nil {
		params.RepoFullName = optional(strings.ToLower(env.Repository.FullName))
	}
	dup := false
	ctx := r.Context()
	err = h.st.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
		n, err := q.InsertGHEvent(ctx, params)
		if err != nil {
			return fmt.Errorf("store event: %w", err)
		}
		if n == 0 {
			dup = true // a redelivery: its job was enqueued with the first copy
			return nil
		}
		return h.enq.InsertGHEvent(ctx, tx, store.GHEventArgs{DeliveryID: delivery.String()})
	})
	if err != nil {
		// 5xx makes the delivery fail; the reconciler requests redelivery.
		h.log.Error("webhook not stored", "delivery", delivery, "event", event, "err", err)
		http.Error(w, "could not store the event", http.StatusServiceUnavailable)
		return
	}
	h.log.Info("webhook stored", "delivery", delivery, "event", event, "action", env.Action, "duplicate", dup)
	w.WriteHeader(http.StatusAccepted)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RiverEnqueuer inserts gh_event jobs with a River client.
type RiverEnqueuer struct{ Client *river.Client[pgx.Tx] }

// InsertGHEvent implements Enqueuer.
func (e RiverEnqueuer) InsertGHEvent(ctx context.Context, tx pgx.Tx, args store.GHEventArgs) error {
	_, err := e.Client.InsertTx(ctx, tx, args, nil)
	return err
}
