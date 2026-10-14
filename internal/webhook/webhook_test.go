package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

var secret = []byte("0123456789abcdef-webhook-secret")

const prPayload = `{"action": "synchronize", "number": 7, "installation": {"id": 42}, "repository": {"full_name": "Acme/App"},
	"pull_request": {"state": "open", "head": {"sha": "abc"}}}`

func sign(body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func request(body, sig, event, delivery string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-GitHub-Delivery", delivery)
	return r
}

const delivery = "72d3162e-cc78-11e3-81ab-4c9367dc0958"

func serve(h *Handler, r *http.Request) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// Rejections happen before the store is touched, so they need none.
func TestRejections(t *testing.T) {
	h := &Handler{secret: secret, log: slog.New(slog.DiscardHandler)}
	body := []byte(prPayload)
	tests := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"wrong signature", request(prPayload, sign([]byte("other")), "pull_request", delivery), http.StatusUnauthorized},
		{"no signature", request(prPayload, "", "pull_request", delivery), http.StatusUnauthorized},
		{"sha1 only", func() *http.Request {
			r := request(prPayload, "", "pull_request", delivery)
			r.Header.Set("X-Hub-Signature", "sha1=0000")
			return r
		}(), http.StatusUnauthorized},
		{"bad delivery id", request(prPayload, sign(body), "pull_request", "not-a-uuid"), http.StatusBadRequest},
		{"no event type", request(prPayload, sign(body), "", delivery), http.StatusBadRequest},
		{"ping has no installation", request(`{"zen": "hi"}`, sign([]byte(`{"zen": "hi"}`)), "ping", delivery), http.StatusAccepted},
		{"GET", httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/webhook", nil), http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		if got := serve(h, tt.req); got != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, got, tt.want)
		}
	}
	big := bytes.Repeat([]byte("a"), MaxPayload+1)
	if got := serve(h, request(string(big), sign(big), "pull_request", delivery)); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized payload: %d", got)
	}
	if _, err := New([]byte("short"), nil, nil, nil); err == nil {
		t.Error("short secret accepted")
	}
}

type recorder struct {
	jobs []store.GHEventArgs
	fail bool
}

func (r *recorder) InsertGHEvent(_ context.Context, _ pgx.Tx, a store.GHEventArgs) error {
	if r.fail {
		return errors.New("queue down")
	}
	r.jobs = append(r.jobs, a)
	return nil
}

func TestStoresAndEnqueuesOnce(t *testing.T) {
	st := storetest.New(t)
	rec := &recorder{}
	h, err := New(secret, st, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(prPayload)
	for range 2 { // the second is a redelivery
		if got := serve(h, request(prPayload, sign(body), "pull_request", delivery)); got != http.StatusAccepted {
			t.Fatalf("status %d", got)
		}
	}
	if len(rec.jobs) != 1 || rec.jobs[0].DeliveryID != delivery {
		t.Fatalf("jobs = %+v", rec.jobs)
	}
	var event, action, repo string
	var inst int64
	if err := st.Pool.QueryRow(t.Context(), `SELECT event, action, installation_id, repo_full_name FROM gh_events WHERE delivery_id = $1`, delivery).
		Scan(&event, &action, &inst, &repo); err != nil {
		t.Fatal(err)
	}
	if event != "pull_request" || action != "synchronize" || inst != 42 || repo != "acme/app" {
		t.Errorf("row = %s %s %d %s", event, action, inst, repo)
	}

	// If the enqueue fails, the event is not stored either (one
	// transaction), and GitHub sees a failed delivery to redeliver.
	rec.fail = true
	other := "8a1f0c3e-cc78-11e3-81ab-4c9367dc0958"
	if got := serve(h, request(prPayload, sign(body), "pull_request", other)); got != http.StatusServiceUnavailable {
		t.Fatalf("failed enqueue: %d", got)
	}
	var n int
	_ = st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM gh_events WHERE delivery_id = $1`, other).Scan(&n)
	if n != 0 {
		t.Error("event stored without its job")
	}
}
