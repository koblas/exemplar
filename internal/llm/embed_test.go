package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

const testEmbedModel = "voyage-code-3"

// fakeVoyage serves the Voyage embeddings API: dims-long vectors, or
// status with a Voyage error body when status is not 200.
type fakeVoyage struct {
	dims   int
	status int
	mu     sync.Mutex
	got    []voyageRequest
	auth   []string
}

func (f *fakeVoyage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req voyageRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.got = append(f.got, req)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.URL.Path != "/v1/embeddings" {
		http.NotFound(w, r)
		return
	}
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"detail":"fake voyage says no"}`))
		return
	}
	resp := embedResponse{Object: "list", Model: req.Model, Usage: embedUsage{TotalTokens: 7 * len(req.Input)}}
	// Reversed, so the provider has to order by index.
	for i := range slices.Backward(req.Input) {
		v := make([]float32, f.dims)
		v[i%f.dims] = 1
		resp.Data = append(resp.Data, embedDatum{Object: "embedding", Index: i, Embedding: v})
	}
	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson // finite test floats always encode
}

func (f *fakeVoyage) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func newEmbedGateway(t *testing.T, fv *fakeVoyage, rec Recorder) *Gateway {
	t.Helper()
	srv := httptest.NewServer(fv)
	t.Cleanup(srv.Close)
	env := map[string]string{EnvVoyageAPIKey: "voyage-test-key", EnvVoyageBaseURL: srv.URL}
	p := schema.Providers{
		Generator: []schema.ProviderModel{{Provider: ProviderClaude, Standard: "claude-sonnet-5-5", High: "claude-opus-5-5"}},
		Verifier:  []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-opus-5-5"}},
		Light:     []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-haiku-5-5"}},
		Embed:     &schema.EmbedModel{Provider: ProviderVoyage, Model: testEmbedModel},
	}
	emb := BuildEmbedder(t.Context(), p.Embed, Settings{Getenv: func(k string) string { return env[k] }}, nil)
	if emb == nil {
		t.Fatal("embedder not built")
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGateway(Config{
		Providers: p, Clients: map[string]Provider{ProviderClaude: &fakeProvider{name: ProviderClaude}},
		Embedder: emb, Scanner: sc, Token: "gw-token", Recorder: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func callEmbed(t *testing.T, g *Gateway, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("X-Api-Key", "gw-token")
	req.Header.Set(HeaderStage, StageEmbed)
	req.Header.Set(HeaderCaller, "learning")
	req.Header.Set(HeaderDataClasses, "review_comments")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	return w
}

func TestEmbedRoutesToVoyageAndRecords(t *testing.T) {
	fv := &fakeVoyage{dims: EmbedDimensions}
	rec := &memRecorder{}
	g := newEmbedGateway(t, fv, rec)
	w := callEmbed(t, g, "/v1/embeddings", nil, `{"model":"ignored","input":["Set timeouts: why","Close files: why"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got embedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 2 || got.Model != testEmbedModel || got.Usage.TotalTokens != 14 {
		t.Fatalf("response %+v", got)
	}
	for i, d := range got.Data {
		if d.Index != i || len(d.Embedding) != EmbedDimensions || d.Embedding[i] != 1 {
			t.Fatalf("datum %d: index %d, %d dims, out of order", i, d.Index, len(d.Embedding))
		}
	}
	req := fv.got[0]
	if req.Model != testEmbedModel || req.InputType != "document" || req.OutputDimension != EmbedDimensions || fv.auth[0] != "Bearer voyage-test-key" {
		t.Fatalf("voyage request %+v auth %q", req, fv.auth[0])
	}
	if w.Header().Get(HeaderProvider) != ProviderVoyage {
		t.Fatalf("provider header %q", w.Header().Get(HeaderProvider))
	}
	if len(rec.calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(rec.calls))
	}
	c := rec.calls[0]
	if c.Stage != StageEmbed || c.Provider != ProviderVoyage || c.Model != testEmbedModel || c.Caller != "learning" ||
		c.Usage.InputTokens != 14 || len(c.DataClasses) != 1 || c.DataClasses[0] != "review_comments" {
		t.Fatalf("recorded call %+v", c)
	}
	if want := 14 * 0.18 / 1e6; !c.PriceKnown || math.Abs(c.CostUSD-want) > 1e-12 {
		t.Fatalf("cost %v (known %v), want %v", c.CostUSD, c.PriceKnown, want)
	}
}

func TestEmbeddingModel(t *testing.T) {
	g := newEmbedGateway(t, &fakeVoyage{dims: EmbedDimensions}, nil)
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/embeddings/model", nil)
		req.Header.Set("X-Api-Key", token)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, req)
		return w
	}
	if w := get("nope"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d", w.Code)
	}
	w := get("gw-token")
	var got struct {
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		Dimensions int    `json:"dimensions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got.Provider != ProviderVoyage || got.Model != testEmbedModel || got.Dimensions != EmbedDimensions {
		t.Fatalf("model %+v", got)
	}
}

func TestEmbedRefusesSecrets(t *testing.T) {
	fv := &fakeVoyage{dims: EmbedDimensions}
	rec := &memRecorder{}
	g := newEmbedGateway(t, fv, rec)
	sum := sha256.Sum256([]byte("embed test token"))
	tok := "ghp_" + hex.EncodeToString(sum[:])[:36]
	w := callEmbed(t, g, "/v1/embeddings", nil, `{"input":["Never log tokens like `+tok+`"]}`)
	if w.Code != http.StatusUnprocessableEntity || fv.calls() != 0 || len(rec.calls) != 0 {
		t.Fatalf("status %d, voyage calls %d, recorded %d; want 422 and nothing sent", w.Code, fv.calls(), len(rec.calls))
	}
	if strings.Contains(w.Body.String(), tok) {
		t.Fatal("error response echoes the secret")
	}
}

func TestEmbedRejects(t *testing.T) {
	tests := []struct {
		name    string
		dims    int
		status  int
		path    string
		headers map[string]string
		body    string
		want    int
	}{
		{"generator stage on embeddings", EmbedDimensions, 0, "/v1/embeddings", map[string]string{HeaderStage: StageGenerator}, `{"input":["a"]}`, 400},
		{"embed stage on messages", EmbedDimensions, 0, "/v1/messages", nil, msgBody, 400},
		{"bad token", EmbedDimensions, 0, "/v1/embeddings", map[string]string{"X-Api-Key": "nope"}, `{"input":["a"]}`, 401},
		{"no data classes", EmbedDimensions, 0, "/v1/embeddings", map[string]string{HeaderDataClasses: ""}, `{"input":["a"]}`, 400},
		{"no input", EmbedDimensions, 0, "/v1/embeddings", nil, `{"input":[]}`, 400},
		{"blank text", EmbedDimensions, 0, "/v1/embeddings", nil, `{"input":["a"," "]}`, 400},
		{"input not strings", EmbedDimensions, 0, "/v1/embeddings", nil, `{"input":[1]}`, 400},
		{"wrong dimension", 512, 0, "/v1/embeddings", nil, `{"input":["a"]}`, 502},
		{"voyage 400 passes through", EmbedDimensions, 400, "/v1/embeddings", nil, `{"input":["a"]}`, 400},
		{"voyage 500 is unavailable", EmbedDimensions, 500, "/v1/embeddings", nil, `{"input":["a"]}`, 503},
		{"voyage 429 is unavailable", EmbedDimensions, 429, "/v1/embeddings", nil, `{"input":["a"]}`, 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &memRecorder{}
			g := newEmbedGateway(t, &fakeVoyage{dims: tt.dims, status: tt.status}, rec)
			w := callEmbed(t, g, tt.path, tt.headers, tt.body)
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("recorded %d calls for a failed request", len(rec.calls))
			}
		})
	}
}

func TestEmbedNotConfigured(t *testing.T) {
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	p := testProviders()
	withSecondProvider(t)
	for _, name := range []string{"no embed entry", "no key"} {
		t.Run(name, func(t *testing.T) {
			if name == "no key" {
				p.Embed = &schema.EmbedModel{Provider: ProviderVoyage, Model: testEmbedModel}
			}
			emb := BuildEmbedder(t.Context(), p.Embed, Settings{Getenv: func(string) string { return "" }}, nil)
			if emb != nil {
				t.Fatalf("BuildEmbedder = %v, want nil", emb)
			}
			g, err := NewGateway(Config{Providers: p, Embedder: emb, Scanner: sc, Token: "gw-token"})
			if err != nil {
				t.Fatal(err)
			}
			if w := callEmbed(t, g, "/v1/embeddings", nil, `{"input":["a"]}`); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503", w.Code)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/embeddings/model", nil)
			req.Header.Set("X-Api-Key", "gw-token")
			w := httptest.NewRecorder()
			g.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("model: status %d, want 503", w.Code)
			}
		})
	}
}

func TestValidateEmbed(t *testing.T) {
	tests := []struct {
		name string
		e    *schema.EmbedModel
		ok   bool
	}{
		{"unset", nil, true},
		{"voyage", &schema.EmbedModel{Provider: ProviderVoyage, Model: testEmbedModel}, true},
		{"unapproved provider", &schema.EmbedModel{Provider: "openai", Model: "text-embedding-3-large"}, false},
		{"claude is not an embedder", &schema.EmbedModel{Provider: ProviderClaude, Model: "claude-haiku-5-5"}, false},
		{"bad model name", &schema.EmbedModel{Provider: ProviderVoyage, Model: "code-3"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := schema.Providers{
				Generator: []schema.ProviderModel{{Provider: ProviderClaude, Standard: "claude-sonnet-5-5", High: "claude-opus-5-5"}},
				Verifier:  []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-opus-5-5"}},
				Light:     []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-haiku-5-5"}},
				Embed:     tt.e,
			}
			if err := ValidateProviders(p); (err == nil) != tt.ok {
				t.Fatalf("ValidateProviders err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
