package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/telemetry"
)

// StageEmbed is the embedding stage, served on POST /v1/embeddings only.
const StageEmbed = "embed"

// EmbedDimensions is the vector size extractions store (halfvec(1024)).
const EmbedDimensions = 1024

// maxEmbedInputs bounds texts per call (Voyage accepts up to 1000).
const maxEmbedInputs = 1000

// EmbedResult is a provider's vectors, one per input in input order.
type EmbedResult struct {
	Vectors [][]float32
	Usage   Usage
}

// Embedder turns texts into document vectors of EmbedDimensions.
type Embedder interface {
	Name() string
	Embed(ctx context.Context, model string, input []string) (*EmbedResult, error)
}

// EmbedSpec describes one embedding provider. Like registry, embedRegistry
// is the hardcoded allowlist: config can only name a provider listed here.
type EmbedSpec struct {
	ValidateModel func(model string) error
	New           func(ctx context.Context, s Settings) (Embedder, error)
}

var embedRegistry = map[string]EmbedSpec{
	ProviderVoyage: voyageSpec,
}

var (
	errEmbedDisabled  = errors.New("no embedding provider is configured or built")
	errEmbedDimension = errors.New("unexpected embedding dimension")
	errEmbedCount     = errors.New("embedding count does not match input")
)

// ValidateEmbed checks the embed entry, when there is one, names an
// approved provider and a model it can serve.
func ValidateEmbed(e *schema.EmbedModel) error {
	if e == nil {
		return nil
	}
	spec, ok := embedRegistry[e.Provider]
	if !ok {
		return fmt.Errorf("llm: embedding provider %q is %w", e.Provider, errNotApproved)
	}
	if err := spec.ValidateModel(e.Model); err != nil {
		return fmt.Errorf("llm: embedding provider %s cannot serve %q: %w", e.Provider, e.Model, err)
	}
	return nil
}

// BuildEmbedder constructs the configured embedding provider, or returns
// nil when there is none to build. Missing credentials are a warning, not
// an error: reviews do not depend on embeddings, so the gateway starts and
// /v1/embeddings answers 503. An unapproved entry is rejected earlier, by
// ValidateProviders.
func BuildEmbedder(ctx context.Context, e *schema.EmbedModel, s Settings, log *slog.Logger) Embedder {
	if log == nil {
		log = slog.Default()
	}
	if e == nil {
		return nil
	}
	if err := ValidateEmbed(e); err != nil {
		log.Warn("embedding provider skipped: /v1/embeddings will answer 503", "err", err)
		return nil
	}
	emb, err := embedRegistry[e.Provider].New(ctx, s)
	if err != nil {
		log.Warn("embedding provider skipped: /v1/embeddings will answer 503", "provider", e.Provider, "err", err)
		return nil
	}
	return emb
}

type embedRequest struct {
	Input []string `json:"input"`
}

type embedDatum struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type embedUsage struct {
	TotalTokens int `json:"total_tokens"`
}

// embedResponse is the body of POST /v1/embeddings, the shape Voyage and
// OpenAI use.
type embedResponse struct {
	Object string       `json:"object"`
	Data   []embedDatum `json:"data"`
	Model  string       `json:"model"`
	Usage  embedUsage   `json:"usage"`
}

// serveEmbeddings handles POST /v1/embeddings: {"input": [texts]} in,
// document vectors out. The model comes from providers.embed, never the
// caller. The same token, headers, secret scan, rate limits and
// model_calls recording apply as on /v1/messages.
func (g *Gateway) serveEmbeddings(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(w, r) {
		return
	}
	meta, err := parseMeta(r.Header)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if meta.stage != StageEmbed {
		writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("/v1/embeddings serves only %s %s", HeaderStage, StageEmbed))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
		return
	}
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
	var req embedRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "input must be a list of strings")
		return
	}
	if len(req.Input) == 0 || len(req.Input) > maxEmbedInputs {
		writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("input must hold 1 to %d texts", maxEmbedInputs))
		return
	}
	if slices.ContainsFunc(req.Input, func(s string) bool { return strings.TrimSpace(s) == "" }) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "input texts must not be empty")
		return
	}
	if g.cfg.Providers.Embed == nil || g.cfg.Embedder == nil {
		writeError(w, http.StatusServiceUnavailable, "exemplar_all_providers_unavailable", errEmbedDisabled.Error())
		return
	}
	route := Route{Provider: g.cfg.Providers.Embed.Provider, Model: g.cfg.Providers.Embed.Model}
	if err := g.limiter("caller:" + meta.caller).Wait(r.Context()); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "caller rate limit")
		return
	}
	if err := g.limiter("model:" + route.Provider + "/" + route.Model).Wait(r.Context()); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limit_error", "provider rate limit")
		return
	}
	start := time.Now()
	res, err := g.cfg.Embedder.Embed(r.Context(), route.Model, req.Input)
	if err == nil {
		err = checkVectors(res, len(req.Input))
	}
	if err != nil {
		status := 0
		if pe, ok := errors.AsType[*ProviderError](err); ok {
			status = pe.Status
		}
		telemetry.ModelCall(r.Context(), route.Provider, route.Model, meta.stage, meta.caller, telemetry.StatusOutcome(status), time.Since(start))
		g.log.Error("embedding call failed", "provider", route.Provider, "model", route.Model, "err", err)
		if IsRetryable(err) {
			writeError(w, http.StatusServiceUnavailable, "exemplar_all_providers_unavailable", "the embedding provider is unavailable")
			return
		}
		g.writeProviderError(w, err)
		return
	}
	latency := time.Since(start)
	telemetry.ModelCall(r.Context(), route.Provider, route.Model, meta.stage, meta.caller, telemetry.OutcomeOK, latency)
	g.recordEmbed(r.Context(), meta, route, res.Usage, latency)

	out := embedResponse{Object: "list", Model: route.Model, Usage: embedUsage{TotalTokens: res.Usage.InputTokens}}
	out.Data = make([]embedDatum, len(res.Vectors))
	for i, v := range res.Vectors {
		out.Data[i] = embedDatum{Object: "embedding", Index: i, Embedding: v}
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(HeaderProvider, route.Provider)
	h.Set(HeaderModel, route.Model)
	w.WriteHeader(http.StatusOK)
	//nolint:errchkjson // floats and strings always encode; a failed write to the client has no recovery
	_ = json.NewEncoder(w).Encode(out)
}

func checkVectors(res *EmbedResult, n int) error {
	if len(res.Vectors) != n {
		return &ProviderError{Status: http.StatusOK, Err: fmt.Errorf("%w: %d vectors for %d texts", errEmbedCount, len(res.Vectors), n)}
	}
	for _, v := range res.Vectors {
		if len(v) != EmbedDimensions {
			return &ProviderError{Status: http.StatusOK, Err: fmt.Errorf("%w: %d, want %d", errEmbedDimension, len(v), EmbedDimensions)}
		}
	}
	return nil
}

func (g *Gateway) recordEmbed(ctx context.Context, meta callMeta, route Route, usage Usage, latency time.Duration) {
	cost, known := Cost(route.Provider, route.Model, usage)
	if !known {
		g.log.Warn("no price for model; cost recorded as 0", "provider", route.Provider, "model", route.Model)
	}
	if g.cfg.Recorder == nil {
		return
	}
	call := Call{
		At: time.Now(), Repo: meta.repo, Caller: meta.caller, Stage: meta.stage,
		Provider: route.Provider, Model: route.Model, DataClasses: meta.dataClasses,
		Usage: usage, CostUSD: cost, PriceKnown: known, Latency: latency,
	}
	if err := g.cfg.Recorder.RecordCall(context.WithoutCancel(ctx), call); err != nil {
		g.log.Error("record model call", "err", err)
	}
}

// ProviderVoyage is Voyage AI's embeddings API.
const ProviderVoyage = "voyage"

// Voyage provider settings.
const (
	// EnvVoyageAPIKey holds the Voyage API key (Secret exemplar-providers).
	EnvVoyageAPIKey = "VOYAGE_API_KEY" //nolint:gosec // G101: the environment variable name, not a credential
	// EnvVoyageBaseURL overrides https://api.voyageai.com (tests, private endpoints).
	EnvVoyageBaseURL = "EXEMPLAR_VOYAGE_BASE_URL"
)

const voyageDefaultBaseURL = "https://api.voyageai.com"

var voyageSpec = EmbedSpec{
	ValidateModel: func(model string) error {
		if !strings.HasPrefix(model, "voyage-") {
			return fmt.Errorf("%w: voyage models are named voyage-*", errModelName)
		}
		return nil
	},
	New: func(_ context.Context, s Settings) (Embedder, error) {
		key := s.env(EnvVoyageAPIKey)
		if key == "" {
			return nil, fmt.Errorf("%s %w", EnvVoyageAPIKey, errRequired)
		}
		base := s.env(EnvVoyageBaseURL)
		if base == "" {
			base = voyageDefaultBaseURL
		}
		hc := s.HTTPClient
		if hc == nil {
			hc = &http.Client{Timeout: 2 * time.Minute}
		}
		return &voyageEmbedder{key: key, url: strings.TrimRight(base, "/") + "/v1/embeddings", http: hc}, nil
	},
}

type voyageEmbedder struct {
	key, url string
	http     *http.Client
}

func (v *voyageEmbedder) Name() string { return ProviderVoyage }

type voyageRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension"`
}

func (v *voyageEmbedder) Embed(ctx context.Context, model string, input []string) (*EmbedResult, error) {
	body, err := json.Marshal(voyageRequest{Input: input, Model: model, InputType: "document", OutputDimension: EmbedDimensions})
	if err != nil {
		return nil, fmt.Errorf("encode voyage request: %w", err)
	}
	//nolint:gosec // G704: the URL is the operator's EXEMPLAR_VOYAGE_BASE_URL or the fixed default, not request data
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.key)
	resp, err := v.http.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return nil, &ProviderError{Retryable: ctx.Err() == nil, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBytes))
	if err != nil {
		return nil, &ProviderError{Status: resp.StatusCode, Retryable: ctx.Err() == nil, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{Status: resp.StatusCode, Retryable: retryableStatus(resp.StatusCode), Body: voyageErrorBody(raw)}
	}
	var parsed embedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &ProviderError{Status: http.StatusOK, Err: fmt.Errorf("decode voyage response: %w", err)}
	}
	vectors := make([][]float32, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(vectors) || vectors[d.Index] != nil {
			return nil, &ProviderError{Status: http.StatusOK, Err: fmt.Errorf("%w: bad index %d", errEmbedCount, d.Index)}
		}
		vectors[d.Index] = d.Embedding
	}
	return &EmbedResult{Vectors: vectors, Usage: Usage{InputTokens: parsed.Usage.TotalTokens}}, nil
}

// voyageErrorBody rewraps a Voyage error ({"detail": "..."}) as an
// Anthropic-style error body, the one error shape gateway callers parse.
func voyageErrorBody(raw []byte) []byte {
	var e struct {
		Detail string `json:"detail"`
	}
	msg := "voyage request failed"
	if json.Unmarshal(raw, &e) == nil && e.Detail != "" {
		msg = e.Detail
	}
	out, err := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": msg}})
	if err != nil {
		return nil
	}
	return out
}
