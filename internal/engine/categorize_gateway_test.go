package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/schema"
)

// providerFake answers one provider's API with a fixed categorization and
// counts the calls it served.
type providerFake struct {
	mu     sync.Mutex
	calls  int
	status int
	bodies []map[string]any
}

func (f *providerFake) record(r *http.Request) int {
	data, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.bodies = append(f.bodies, m)
	return f.status
}

// claudeAPI answers like the Messages API.
func claudeAPI(f *providerFake, answer string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status := f.record(r); status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
			return
		}
		text, _ := json.Marshal(answer)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
			`"content":[{"type":"text","text":`+string(text)+`}],"stop_reason":"end_turn","usage":{"input_tokens":40,"output_tokens":9}}`)
	})
}

// openAIAPI answers like Chat Completions.
func openAIAPI(f *providerFake, answer string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status := f.record(r); status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"busy","type":"server_error"}}`)
			return
		}
		text, _ := json.Marshal(answer)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-test","choices":[`+
			`{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":`+string(text)+`}}],`+
			`"usage":{"prompt_tokens":40,"completion_tokens":9}}`)
	})
}

// gatewayEngine starts exemplar-gateway in process on the given providers,
// with real Claude and OpenAI clients on the fakes, and an engine whose
// harness calls it over HTTP.
func gatewayEngine(t *testing.T, e env, providers schema.Providers, claude, openai http.Handler) *Engine {
	t.Helper()
	claudeSrv, openaiSrv := httptest.NewServer(claude), httptest.NewServer(openai)
	t.Cleanup(claudeSrv.Close)
	t.Cleanup(openaiSrv.Close)
	settings := llm.Settings{Getenv: func(k string) string {
		return map[string]string{
			llm.EnvClaudeAPIKey: "sk-ant", llm.EnvClaudeBaseURL: claudeSrv.URL,
			llm.EnvOpenAIAPIKey: "sk-oai", llm.EnvOpenAIBaseURL: openaiSrv.URL,
		}[k]
	}}
	clients, err := llm.BuildProviders(t.Context(), providers, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := llm.NewGateway(llm.Config{Providers: providers, Clients: clients, Scanner: e.scanner, Token: "gw-token"})
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(g)
	t.Cleanup(gw.Close)
	h := NewHarness(HarnessConfig{GatewayURL: gw.URL, GatewayToken: "gw-token"})
	return New(h, e.scanner, compiled.Runner{ASTGrep: "ast-grep-not-installed"})
}

func categorizerProviders(a, b schema.ProviderModel) schema.Providers {
	claude := []schema.ProviderModel{{Provider: llm.ProviderClaude, Model: "claude-sonnet-5-5"}}
	return schema.Providers{Generator: claude, Verifier: claude, Light: claude, Categorizer: &schema.Categorizer{A: a, B: b}}
}

// TestCategorizeAcrossFamiliesThroughGateway is FR-E10's Stage 2 case end
// to end: side A on Claude, side B on OpenAI, each through the gateway's
// routing, secret scan and translation. The families come from the
// gateway's response headers, so the pair is independent and the
// confidence is nonzero; the same pair on one family stays at 0.
func TestCategorizeAcrossFamiliesThroughGateway(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fix a typo in the README", "Docs only.")
	const (
		answerA = `{"risk": "low", "p_low": 0.93, "reasons": ["documentation only"]}`
		answerB = `{"risk": "low", "p_low": 0.88, "reasons": ["no code paths change"]}`
	)
	claudeSide := schema.ProviderModel{Provider: llm.ProviderClaude, Model: "claude-haiku-5-5"}
	openaiSide := schema.ProviderModel{Provider: llm.ProviderOpenAI, Model: "gpt-test"}

	claudeFake, openaiFake := &providerFake{}, &providerFake{}
	eng := gatewayEngine(t, e, categorizerProviders(claudeSide, openaiSide), claudeAPI(claudeFake, answerA), openAIAPI(openaiFake, answerB))
	got, runs := eng.Categorize(t.Context(), e.prep, "production")
	if !got.A.OK || !got.B.OK || got.ReviewConfidence != 0.88 || got.NotIndependent || len(runs) != 2 {
		t.Fatalf("categorize = %+v", got)
	}
	if got.A.Family != llm.FamilyAnthropic || got.B.Family != llm.FamilyOpenAI || got.B.Model != "gpt-test" {
		t.Errorf("sides = %+v / %+v", got.A, got.B)
	}
	if claudeFake.calls != 1 || openaiFake.calls != 1 {
		t.Errorf("calls: claude %d, openai %d", claudeFake.calls, openaiFake.calls)
	}
	// Side B's request reached OpenAI as a strict JSON-schema completion.
	rf, _ := openaiFake.bodies[0]["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || openaiFake.bodies[0]["model"] != "gpt-test" {
		t.Errorf("openai request = %v", openaiFake.bodies[0])
	}

	// Both sides on Claude: same family, so no confidence whatever they say.
	eng = gatewayEngine(t, e, categorizerProviders(claudeSide, claudeSide), claudeAPI(&providerFake{}, answerA), openAIAPI(&providerFake{}, answerB))
	got, _ = eng.Categorize(t.Context(), e.prep, "production")
	if got.ReviewConfidence != 0 || !got.NotIndependent || !strings.Contains(got.Why, "independent") {
		t.Errorf("same family = %+v", got)
	}

	// Side B's provider down: categorizer sides have no failover, so the
	// side fails and the confidence is 0.
	eng = gatewayEngine(t, e, categorizerProviders(claudeSide, openaiSide), claudeAPI(&providerFake{}, answerA), openAIAPI(&providerFake{status: http.StatusServiceUnavailable}, answerB))
	got, _ = eng.Categorize(t.Context(), e.prep, "production")
	if got.ReviewConfidence != 0 || got.B.OK || !strings.Contains(got.B.Detail, "unavailable") {
		t.Errorf("side b down = %+v", got)
	}
}
