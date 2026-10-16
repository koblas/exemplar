package llm

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Message Batches: the learning plane's extraction runs as
// asynchronous batches at half price. The gateway serves the Anthropic
// batch API so any SDK client pointed at it works unchanged:
//
//	POST /v1/messages/batches                create (same checks as /v1/messages)
//	GET  /v1/messages/batches/{id}           status
//	POST /v1/messages/batches/{id}/cancel    cancel
//	GET  /v1/messages/batches/{id}/results   results (JSONL, streamed)
//
// A batch goes to the first provider in its stage's list that supports
// batches. Batches do not fail over: they are asynchronous, so a failed
// create is retried by the caller rather than moved to another provider.

// BatchProvider is implemented by providers that support Message Batches.
// Bodies are Anthropic batch JSON, passed through unchanged.
type BatchProvider interface {
	Provider
	CreateBatch(ctx context.Context, body []byte) (raw []byte, id string, err error)
	GetBatch(ctx context.Context, id string) ([]byte, error)
	CancelBatch(ctx context.Context, id string) ([]byte, error)
	// StreamResults calls fn for each JSONL result in order.
	StreamResults(ctx context.Context, id string, fn func(BatchResult) error) error
}

// BatchResult is one result line: its raw JSON plus the parsed fields the
// gateway accounts for.
type BatchResult struct {
	Raw       []byte
	Succeeded bool
	Usage     Usage
}

// BatchRecord is what the gateway remembers about a batch.
type BatchRecord struct {
	ID          string
	Provider    string
	Model       string
	Stage       string
	Caller      string
	Repo        string
	DataClasses []string
	Requests    int
}

// BatchStore persists batch records. ClaimAccounting returns true exactly
// once per batch, so usage is booked once even if results are read twice.
type BatchStore interface {
	SaveBatch(ctx context.Context, b BatchRecord) error
	LookupBatch(ctx context.Context, id string) (BatchRecord, bool, error)
	ClaimAccounting(ctx context.Context, id string) (bool, error)
}

// MemoryBatchStore keeps batch records in memory. It is for single-replica
// development; deployments use the Postgres store.
type MemoryBatchStore struct {
	mu        sync.Mutex
	batches   map[string]BatchRecord
	accounted map[string]bool
}

// NewMemoryBatchStore returns an empty in-memory store.
func NewMemoryBatchStore() *MemoryBatchStore {
	return &MemoryBatchStore{batches: map[string]BatchRecord{}, accounted: map[string]bool{}}
}

// SaveBatch implements BatchStore.
func (m *MemoryBatchStore) SaveBatch(_ context.Context, b BatchRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.batches[b.ID]; !ok {
		m.batches[b.ID] = b
	}
	return nil
}

// LookupBatch implements BatchStore.
func (m *MemoryBatchStore) LookupBatch(_ context.Context, id string) (BatchRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	return b, ok, nil
}

// ClaimAccounting implements BatchStore.
func (m *MemoryBatchStore) ClaimAccounting(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accounted[id] {
		return false, nil
	}
	m.accounted[id] = true
	return true, nil
}

// Batch limits, from the Message Batches API.
const (
	maxBatchRequests = 100_000
	maxBatchBytes    = 256 << 20
)

// BatchDiscount is the batch price multiplier: every token, including
// cache reads and writes, is billed at half the standard rate.
const BatchDiscount = 0.5

// Handler serves single calls, batches and nothing else.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/messages", g)
	mux.HandleFunc("POST /v1/messages/batches", g.createBatch)
	mux.HandleFunc("GET /v1/messages/batches/{id}", g.getBatch)
	mux.HandleFunc("POST /v1/messages/batches/{id}/cancel", g.cancelBatch)
	mux.HandleFunc("GET /v1/messages/batches/{id}/results", g.batchResults)
	return mux
}

func (g *Gateway) authorized(w http.ResponseWriter, r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Api-Key")), []byte(g.cfg.Token)) != 1 {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid gateway token")
		return false
	}
	return true
}

type batchRequest struct {
	CustomID string          `json:"custom_id"`
	Params   json.RawMessage `json:"params"`
}

func (g *Gateway) createBatch(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(w, r) {
		return
	}
	meta, err := parseMeta(r.Header)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBatchBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "batch body too large")
		return
	}
	var in struct {
		Requests []batchRequest `json:"requests"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "batch body must be {\"requests\": [...]}")
		return
	}
	if len(in.Requests) == 0 || len(in.Requests) > maxBatchRequests {
		writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("a batch holds 1 to %d requests", maxBatchRequests))
		return
	}

	routes, err := Routes(g.cfg.Providers, meta.stage, meta.tier)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var (
		route Route
		bp    BatchProvider
	)
	for _, rt := range routes {
		if p, ok := g.cfg.Clients[rt.Provider].(BatchProvider); ok {
			route, bp = rt, p
			break
		}
	}
	if bp == nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "no provider for this stage supports Message Batches")
		return
	}

	// Same checks as a single call, per request: unique ids, a JSON
	// object for params, the routed model, and the second secret scan.
	seen := map[string]bool{}
	for i := range in.Requests {
		req := &in.Requests[i]
		if req.CustomID == "" || seen[req.CustomID] {
			writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("request %d: custom_id must be set and unique", i))
			return
		}
		seen[req.CustomID] = true
		matches, err := g.scanRequest(req.Params)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("request %s: params are not valid JSON", req.CustomID))
			return
		}
		if len(matches) > 0 {
			g.log.Warn("refused batch with unredacted secret", "caller", meta.caller, "stage", meta.stage, "custom_id", req.CustomID)
			writeError(w, http.StatusUnprocessableEntity, "exemplar_unredacted_secret", "request "+req.CustomID+" contains an unredacted secret")
			return
		}
		if req.Params, err = withModel(req.Params, route.Model); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("request %s: %v", req.CustomID, err))
			return
		}
	}
	routed, err := json.Marshal(in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", "could not encode batch")
		return
	}
	if err := g.limiter("caller:" + meta.caller).Wait(r.Context()); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "caller rate limit")
		return
	}

	raw, id, err := bp.CreateBatch(r.Context(), routed)
	if err != nil {
		g.writeProviderError(w, err)
		return
	}
	rec := BatchRecord{
		ID: id, Provider: route.Provider, Model: route.Model, Stage: meta.stage, Caller: meta.caller,
		Repo: meta.repo, DataClasses: meta.dataClasses, Requests: len(in.Requests),
	}
	if err := g.batches().SaveBatch(context.WithoutCancel(r.Context()), rec); err != nil {
		// The batch exists at the provider; without the record its usage
		// cannot be booked. Surface it rather than lose track silently.
		g.log.Error("save batch record", "batch_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "api_error", "batch created but not recorded; id "+id)
		return
	}
	g.log.Info("batch created", "batch_id", id, "provider", route.Provider, "model", route.Model, "requests", len(in.Requests), "caller", meta.caller)
	g.writeBatchJSON(w, route, raw)
}

func (g *Gateway) lookup(w http.ResponseWriter, r *http.Request) (BatchRecord, BatchProvider, bool) {
	if !g.authorized(w, r) {
		return BatchRecord{}, nil, false
	}
	id := r.PathValue("id")
	rec, ok, err := g.batches().LookupBatch(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", "batch lookup failed")
		return BatchRecord{}, nil, false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found_error", "unknown batch "+id)
		return BatchRecord{}, nil, false
	}
	bp, ok := g.cfg.Clients[rec.Provider].(BatchProvider)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "api_error", "provider "+rec.Provider+" is not configured")
		return BatchRecord{}, nil, false
	}
	return rec, bp, true
}

func (g *Gateway) getBatch(w http.ResponseWriter, r *http.Request) {
	rec, bp, ok := g.lookup(w, r)
	if !ok {
		return
	}
	raw, err := bp.GetBatch(r.Context(), rec.ID)
	if err != nil {
		g.writeProviderError(w, err)
		return
	}
	g.writeBatchJSON(w, Route{Provider: rec.Provider, Model: rec.Model}, raw)
}

func (g *Gateway) cancelBatch(w http.ResponseWriter, r *http.Request) {
	rec, bp, ok := g.lookup(w, r)
	if !ok {
		return
	}
	raw, err := bp.CancelBatch(r.Context(), rec.ID)
	if err != nil {
		g.writeProviderError(w, err)
		return
	}
	g.writeBatchJSON(w, Route{Provider: rec.Provider, Model: rec.Model}, raw)
}

// batchResults streams results through and, once the stream completes,
// books the batch's usage to model_calls (once per batch).
func (g *Gateway) batchResults(w http.ResponseWriter, r *http.Request) {
	rec, bp, ok := g.lookup(w, r)
	if !ok {
		return
	}
	var (
		total     Usage
		succeeded int
		started   bool
	)
	flusher, _ := w.(http.Flusher)
	err := bp.StreamResults(r.Context(), rec.ID, func(res BatchResult) error {
		if !started {
			w.Header().Set("Content-Type", "application/x-jsonl")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		if res.Succeeded {
			succeeded++
			total.InputTokens += res.Usage.InputTokens
			total.OutputTokens += res.Usage.OutputTokens
			total.CacheReadTokens += res.Usage.CacheReadTokens
			total.CacheWriteTokens += res.Usage.CacheWriteTokens
		}
		if _, err := w.Write(append(res.Raw, '\n')); err != nil {
			return err
		}
		if flusher != nil && succeeded%500 == 0 {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
		if !started {
			g.writeProviderError(w, err)
			return
		}
		// Mid-stream failure: the client sees a truncated stream. Nothing
		// is booked, so a later full read accounts the batch.
		g.log.Error("batch results stream failed", "batch_id", rec.ID, "err", err)
		return
	}
	if !started {
		w.Header().Set("Content-Type", "application/x-jsonl")
		w.WriteHeader(http.StatusOK)
	}
	g.accountBatch(context.WithoutCancel(r.Context()), rec, total, succeeded)
}

func (g *Gateway) accountBatch(ctx context.Context, rec BatchRecord, total Usage, succeeded int) {
	claimed, err := g.batches().ClaimAccounting(ctx, rec.ID)
	if err != nil {
		g.log.Error("claim batch accounting", "batch_id", rec.ID, "err", err)
		return
	}
	if !claimed {
		return
	}
	cost, known := Cost(rec.Provider, rec.Model, total)
	cost *= BatchDiscount
	if !known {
		g.log.Warn("no price for batch model; cost recorded as 0", "provider", rec.Provider, "model", rec.Model)
	}
	g.log.Info("batch accounted", "batch_id", rec.ID, "succeeded", succeeded, "requests", rec.Requests, "cost_usd", cost)
	if g.cfg.Recorder == nil {
		return
	}
	id := rec.ID
	call := Call{
		At: time.Now(), BatchID: &id, Repo: rec.Repo, Caller: rec.Caller, Stage: rec.Stage,
		Provider: rec.Provider, Model: rec.Model, DataClasses: rec.DataClasses,
		Usage: total, CostUSD: cost, PriceKnown: known,
	}
	if err := g.cfg.Recorder.RecordCall(ctx, call); err != nil {
		g.log.Error("record batch usage", "batch_id", rec.ID, "err", err)
	}
}

func (g *Gateway) writeBatchJSON(w http.ResponseWriter, route Route, raw []byte) {
	family, _ := Family(route.Provider, route.Model)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(HeaderProvider, route.Provider)
	h.Set(HeaderModel, route.Model)
	h.Set(HeaderFamily, family)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw) //nolint:gosec // G705: upstream batch JSON served as application/json to API clients, never rendered as HTML
}

func (g *Gateway) batches() BatchStore {
	if g.cfg.Batches != nil {
		return g.cfg.Batches
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.memBatches == nil {
		g.memBatches = NewMemoryBatchStore()
	}
	return g.memBatches
}

var errNoBatchID = errors.New("provider returned a batch without an id")
