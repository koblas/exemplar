package llm

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/telemetry"
)

// Request headers callers set on every gateway call.
const (
	HeaderStage       = "X-Exemplar-Stage"
	HeaderTier        = "X-Exemplar-Tier"
	HeaderReviewID    = "X-Exemplar-Review-Id"
	HeaderRuleRunID   = "X-Exemplar-Rule-Run-Id"
	HeaderCaller      = "X-Exemplar-Caller"
	HeaderRepo        = "X-Exemplar-Repo"
	HeaderDataClasses = "X-Exemplar-Data-Classes"
)

// Response headers the gateway adds.
const (
	HeaderProvider = "X-Exemplar-Provider"
	HeaderModel    = "X-Exemplar-Model"
	HeaderFamily   = "X-Exemplar-Family"
	HeaderFailover = "X-Exemplar-Failover"
	HeaderBudget   = "X-Exemplar-Budget"
	HeaderCostUSD  = "X-Exemplar-Cost-Usd"
)

// Budget signals: the orchestrator sheds work at warn.
const (
	BudgetOK       = "ok"
	BudgetWarn     = "warn"
	BudgetExceeded = "exceeded"
)

// DataClasses are the kinds of repository data a call may carry.
var DataClasses = []string{"diff", "file_context", "search_context", "ticket", "review_comments", "pr_text"}

var callers = []string{"production", "playground", "cli", "agent", "eval", "baseline", "learning"}

// Call is one model call as recorded in model_calls.
type Call struct {
	At          time.Time
	ReviewID    *int64
	RuleRunID   *int64
	BatchID     *string
	Repo        string
	Caller      string
	Stage       string
	Provider    string
	Model       string
	Failover    bool
	DataClasses []string
	Usage       Usage
	CostUSD     float64
	PriceKnown  bool
	Latency     time.Duration
}

// Recorder persists calls. RecordCall failures are logged, never returned
// to the caller: accounting must not block a review.
type Recorder interface {
	RecordCall(ctx context.Context, c Call) error
}

// Config configures a Gateway.
type Config struct {
	Providers schema.Providers
	// Clients are the constructed providers, keyed by provider name. Only
	// approved providers may appear.
	Clients map[string]Provider
	Scanner *redact.Scanner
	// Token is the shared secret callers send as x-api-key. Required.
	Token          string
	SoftCeilingUSD float64
	// RequestsPerMinute caps calls per provider+model and per caller.
	RequestsPerMinute int
	Recorder          Recorder
	// SpendLoader returns prior spend for a review (e.g. from model_calls)
	// the first time the gateway sees it. Optional.
	SpendLoader func(ctx context.Context, reviewID int64) (float64, error)
	// Batches records Message Batches; nil keeps them in memory (one replica only).
	Batches BatchStore
	Logger  *slog.Logger
}

// Gateway is the only component holding model provider keys.
type Gateway struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	spend    map[int64]float64
	limiters map[string]*rate.Limiter

	memBatches *MemoryBatchStore
}

// NewGateway validates cfg and returns a Gateway.
func NewGateway(cfg Config) (*Gateway, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("llm: gateway token %w", errRequired)
	}
	if cfg.Scanner == nil {
		return nil, fmt.Errorf("llm: secret scanner %w", errRequired)
	}
	if err := ValidateProviders(cfg.Providers); err != nil {
		return nil, err
	}
	for name := range cfg.Clients {
		if _, ok := registry[name]; !ok {
			return nil, fmt.Errorf("llm: provider %q is %w", name, errNotApproved)
		}
	}
	if cfg.RequestsPerMinute <= 0 {
		cfg.RequestsPerMinute = 600
	}
	if cfg.SoftCeilingUSD <= 0 {
		cfg.SoftCeilingUSD = 10
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{cfg: cfg, log: log, spend: map[int64]float64{}, limiters: map[string]*rate.Limiter{}}, nil
}

// maxRequestBytes bounds request bodies; diffs are capped well below this.
const maxRequestBytes = 32 << 20

// ServeHTTP handles POST /v1/messages.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		writeError(w, http.StatusNotFound, "not_found_error", "only POST /v1/messages is served")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Api-Key")), []byte(g.cfg.Token)) != 1 {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid gateway token")
		return
	}
	meta, err := parseMeta(r.Header)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
		return
	}

	// Second secret scan: the worker redacts; the gateway refuses.
	if matches, err := g.scanRequest(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "request body is not valid JSON")
		return
	} else if len(matches) > 0 {
		rules := make([]string, 0, len(matches))
		for _, m := range matches {
			rules = append(rules, m.RuleID)
		}
		g.log.Warn("refused request with unredacted secret", "caller", meta.caller, "repo", meta.repo, "stage", meta.stage, "rules", rules)
		writeError(w, http.StatusUnprocessableEntity, "exemplar_unredacted_secret", "request contains an unredacted secret")
		return
	}

	routes, err := Routes(g.cfg.Providers, meta.stage, meta.tier)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	if meta.reviewID != nil {
		g.loadSpend(r.Context(), *meta.reviewID)
	}
	if err := g.limiter("caller:" + meta.caller).Wait(r.Context()); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "caller rate limit")
		return
	}

	var lastErr error
	for i, route := range routes {
		client, ok := g.cfg.Clients[route.Provider]
		if !ok {
			lastErr = fmt.Errorf("provider %s %w", route.Provider, errNoClient)
			continue
		}
		routed, err := withModel(body, route.Model)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		if err := g.limiter("model:" + route.Provider + "/" + route.Model).Wait(r.Context()); err != nil {
			writeError(w, http.StatusTooManyRequests, "rate_limit_error", "provider rate limit")
			return
		}
		start := time.Now()
		resp, err := client.Send(r.Context(), Request{Body: routed, Beta: r.Header.Get("Anthropic-Beta")})
		if err != nil {
			status := 0
			if pe, ok := errors.AsType[*ProviderError](err); ok {
				status = pe.Status
			}
			telemetry.ModelCall(r.Context(), route.Provider, route.Model, meta.stage, meta.caller, telemetry.StatusOutcome(status), time.Since(start))
			lastErr = err
			if IsRetryable(err) && r.Context().Err() == nil {
				g.log.Warn("provider failed; failing over", "provider", route.Provider, "model", route.Model, "stage", meta.stage, "err", err)
				continue
			}
			g.writeProviderError(w, err)
			return
		}
		telemetry.ModelCall(r.Context(), route.Provider, route.Model, meta.stage, meta.caller, telemetry.OutcomeOK, time.Since(start))
		g.finish(r.Context(), w, meta, route, i > 0, resp, time.Since(start))
		return
	}
	g.log.Error("every provider failed", "stage", meta.stage, "err", lastErr)
	writeError(w, http.StatusServiceUnavailable, "exemplar_all_providers_unavailable", "every provider for this stage is unavailable")
}

func (g *Gateway) finish(ctx context.Context, w http.ResponseWriter, meta callMeta, route Route, failover bool, resp *Response, latency time.Duration) {
	cost, known := Cost(route.Provider, route.Model, resp.Usage)
	if !known {
		g.log.Warn("no price for model; cost recorded as 0", "provider", route.Provider, "model", route.Model)
	}
	budget := BudgetOK
	if meta.reviewID != nil {
		budget = g.addSpend(*meta.reviewID, cost)
	}
	if g.cfg.Recorder != nil {
		call := Call{
			At: time.Now(), ReviewID: meta.reviewID, RuleRunID: meta.ruleRunID, Repo: meta.repo,
			Caller: meta.caller, Stage: meta.stage, Provider: route.Provider, Model: route.Model,
			Failover: failover, DataClasses: meta.dataClasses, Usage: resp.Usage, CostUSD: cost,
			PriceKnown: known, Latency: latency,
		}
		if err := g.cfg.Recorder.RecordCall(context.WithoutCancel(ctx), call); err != nil {
			g.log.Error("record model call", "err", err)
		}
	}
	family, _ := Family(route.Provider, route.Model)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(HeaderProvider, route.Provider)
	h.Set(HeaderModel, route.Model)
	h.Set(HeaderFamily, family)
	h.Set(HeaderFailover, strconv.FormatBool(failover))
	h.Set(HeaderBudget, budget)
	h.Set(HeaderCostUSD, strconv.FormatFloat(cost, 'f', 6, 64))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp.Body)
}

func (g *Gateway) writeProviderError(w http.ResponseWriter, err error) {
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Status >= 400 && len(pe.Body) > 0 {
		// Non-retryable provider errors (e.g. 400) pass through so the
		// caller sees the real reason.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(pe.Status)
		_, _ = w.Write(pe.Body)
		return
	}
	writeError(w, http.StatusBadGateway, "api_error", "provider call failed")
}

type callMeta struct {
	stage, tier, caller, repo string
	reviewID, ruleRunID       *int64
	dataClasses               []string
}

var (
	errNoClient      = errors.New("has no client configured")
	errInvalidHeader = errors.New("invalid header")
)

func parseMeta(h http.Header) (callMeta, error) {
	m := callMeta{
		stage:  h.Get(HeaderStage),
		tier:   h.Get(HeaderTier),
		caller: h.Get(HeaderCaller),
		repo:   h.Get(HeaderRepo),
	}
	if m.stage == "" {
		return m, fmt.Errorf("%s %w", HeaderStage, errRequired)
	}
	if !slices.Contains(callers, m.caller) {
		return m, fmt.Errorf("%w: %s must be one of %v", errInvalidHeader, HeaderCaller, callers)
	}
	for _, p := range []struct {
		name string
		dst  **int64
	}{{HeaderReviewID, &m.reviewID}, {HeaderRuleRunID, &m.ruleRunID}} {
		if v := h.Get(p.name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return m, fmt.Errorf("%w: %s must be a positive integer", errInvalidHeader, p.name)
			}
			*p.dst = &n
		}
	}
	raw := h.Get(HeaderDataClasses)
	if raw == "" {
		return m, fmt.Errorf("%s %w (FR-H4)", HeaderDataClasses, errRequired)
	}
	for c := range strings.SplitSeq(raw, ",") {
		c = strings.TrimSpace(c)
		if !slices.Contains(DataClasses, c) {
			return m, fmt.Errorf("%w: unknown data class %q", errInvalidHeader, c)
		}
		if !slices.Contains(m.dataClasses, c) {
			m.dataClasses = append(m.dataClasses, c)
		}
	}
	return m, nil
}

// scanRequest scans every JSON string in the body. Scanning decoded
// strings, not raw JSON, catches secrets that JSON escaping would split.
func (g *Gateway) scanRequest(body []byte) ([]redact.Match, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	var matches []redact.Match
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			matches = append(matches, g.cfg.Scanner.Scan(t)...)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return matches, nil
}

// withModel sets the routed model on the request body.
func withModel(body []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("request body is not a JSON object: %w", err)
	}
	enc, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("encode model: %w", err)
	}
	m["model"] = enc
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	return out, nil
}

func (g *Gateway) limiter(key string) *rate.Limiter {
	g.mu.Lock()
	defer g.mu.Unlock()
	lim, ok := g.limiters[key]
	if !ok {
		perSec := rate.Limit(float64(g.cfg.RequestsPerMinute) / 60)
		lim = rate.NewLimiter(perSec, max(1, g.cfg.RequestsPerMinute/10))
		g.limiters[key] = lim
	}
	return lim
}

func (g *Gateway) loadSpend(ctx context.Context, reviewID int64) {
	g.mu.Lock()
	_, seen := g.spend[reviewID]
	g.mu.Unlock()
	if seen || g.cfg.SpendLoader == nil {
		return
	}
	prior, err := g.cfg.SpendLoader(ctx, reviewID)
	if err != nil {
		g.log.Warn("load prior spend", "review_id", reviewID, "err", err)
		return
	}
	g.mu.Lock()
	if _, seen := g.spend[reviewID]; !seen {
		g.spend[reviewID] = prior
	}
	g.mu.Unlock()
}

func (g *Gateway) addSpend(reviewID int64, cost float64) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spend[reviewID] += cost
	return budgetSignal(g.spend[reviewID], g.cfg.SoftCeilingUSD)
}

func budgetSignal(spend, ceiling float64) string {
	switch {
	case spend >= ceiling:
		return BudgetExceeded
	case spend >= 0.8*ceiling:
		return BudgetWarn
	}
	return BudgetOK
}

func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errchkjson // a fixed map of strings always encodes; a failed write to the client has no recovery
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}
