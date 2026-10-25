package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

// fakeChatAPI records requests and answers like Chat Completions.
type fakeChatAPI struct {
	mu     sync.Mutex
	path   string
	header http.Header
	body   map[string]any
	status int
	answer string // the choice; a text answer when empty
}

func (f *fakeChatAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.path, f.header = r.URL.Path, r.Header.Clone()
	f.body = nil
	_ = json.Unmarshal(data, &f.body)
	status, answer := f.status, f.answer
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_exceeded","code":null}}`)
		return
	}
	if answer == "" {
		answer = `{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}`
	}
	_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-test","choices":[`+answer+`],`+
		`"usage":{"prompt_tokens":120,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":100}}}`)
}

func (f *fakeChatAPI) setStatus(code int) {
	f.mu.Lock()
	f.status = code
	f.mu.Unlock()
}

// toolTurnRequest is what the engine harness sends on a tool turn: cached
// system and context blocks, a strict tool, an output schema, then the
// model's tool call and its result.
const toolTurnRequest = `{"model":"gpt-test","max_tokens":1000,
"system":[{"type":"text","text":"You verify findings.","cache_control":{"type":"ephemeral"}}],
"tools":[{"name":"read_file","description":"Read a file","strict":true,
  "input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}],
"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"verdict":{"type":"string"},"note":{"type":"string"}},"required":["verdict"],"additionalProperties":false}}},
"messages":[
  {"role":"user","content":[{"type":"text","text":"the diff","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Verify."}]},
  {"role":"assistant","content":[{"type":"text","text":"Reading."},{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"main.go"}}]},
  {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"package main","is_error":false},
    {"type":"tool_result","tool_use_id":"call_2","content":[{"type":"text","text":"no such file"}],"is_error":true}]}
]}`

func TestOpenAITranslatesRequest(t *testing.T) {
	t.Parallel()
	fake := &fakeChatAPI{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p, err := NewProvider(t.Context(), ProviderOpenAI, envOf(map[string]string{EnvOpenAIAPIKey: "sk-oai", EnvOpenAIBaseURL: srv.URL + "/v1/"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Send(t.Context(), Request{Body: []byte(toolTurnRequest), Beta: "ignored-beta"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.path != "/v1/chat/completions" || fake.header.Get("Authorization") != "Bearer sk-oai" || fake.header.Get("Anthropic-Beta") != "" {
		t.Fatalf("request = %s %v", fake.path, fake.header)
	}
	want := `{
"model":"gpt-test","max_completion_tokens":1000,
"messages":[
  {"role":"system","content":"You verify findings."},
  {"role":"user","content":"the diff\n\nVerify."},
  {"role":"assistant","content":"Reading.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},
  {"role":"tool","tool_call_id":"call_1","content":"package main"},
  {"role":"tool","tool_call_id":"call_2","content":"Error: no such file"}
],
"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file","strict":true,
  "parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}}],
"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":false,
  "schema":{"type":"object","properties":{"verdict":{"type":"string"},"note":{"type":"string"}},"required":["verdict"],"additionalProperties":false}}}
}`
	var wantBody map[string]any
	if err := json.Unmarshal([]byte(want), &wantBody); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(fake.body)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := json.Marshal(wantBody)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(exp) {
		t.Errorf("chat request:\n got %s\nwant %s", got, exp)
	}
}

func TestOpenAITranslatesResponse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, answer, stop string
		content            string
	}{
		{
			"text", `{"finish_reason":"stop","message":{"role":"assistant","content":"{\"risk\":\"low\"}"}}`, "end_turn",
			`[{"text":"{\"risk\":\"low\"}","type":"text"}]`,
		},
		{
			"tool call", `{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,` +
				`"tool_calls":[{"id":"call_9","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}`, "tool_use",
			`[{"id":"call_9","input":{"path":"a.go"},"name":"read_file","type":"tool_use"}]`,
		},
		{
			"truncated", `{"finish_reason":"length","message":{"role":"assistant","content":"{\"ri"}}`, "max_tokens",
			`[{"text":"{\"ri","type":"text"}]`,
		},
		{"refusal", `{"finish_reason":"stop","message":{"role":"assistant","content":null,"refusal":"I can't help"}}`, "refusal", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeChatAPI{answer: tc.answer}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			p, err := NewProvider(t.Context(), ProviderOpenAI, envOf(map[string]string{EnvOpenAIAPIKey: "k", EnvOpenAIBaseURL: srv.URL}))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := p.Send(t.Context(), Request{Body: []byte(msgBody)})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			var msg struct {
				Type       string          `json:"type"`
				StopReason string          `json:"stop_reason"`
				Content    json.RawMessage `json:"content"`
				Usage      Usage           `json:"usage"`
			}
			if err := json.Unmarshal(resp.Body, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != "message" || msg.StopReason != tc.stop || string(msg.Content) != tc.content {
				t.Errorf("message = %s", resp.Body)
			}
			// Messages usage excludes cache reads from input.
			wantUsage := Usage{InputTokens: 20, OutputTokens: 7, CacheReadTokens: 100}
			if resp.Usage != wantUsage || msg.Usage != wantUsage {
				t.Errorf("usage = %+v / %+v, want %+v", resp.Usage, msg.Usage, wantUsage)
			}
		})
	}
}

func TestOpenAIErrors(t *testing.T) {
	t.Parallel()
	fake := &fakeChatAPI{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p, err := NewProvider(t.Context(), ProviderOpenAI, envOf(map[string]string{EnvOpenAIAPIKey: "k", EnvOpenAIBaseURL: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		fake.setStatus(code)
		if _, err := p.Send(t.Context(), Request{Body: []byte(msgBody)}); !IsRetryable(err) {
			t.Errorf("%d should fail over: %v", code, err)
		}
	}
	fake.setStatus(http.StatusBadRequest)
	_, err = p.Send(t.Context(), Request{Body: []byte(msgBody)})
	var pe *ProviderError
	if IsRetryable(err) || !asProviderError(err, &pe) || pe.Status != 400 {
		t.Fatalf("400 should pass through, not fail over: %v", err)
	}
	// The body is rewritten to the Messages error shape callers parse.
	if string(pe.Body) != `{"error":{"message":"slow down","type":"invalid_request_error"},"type":"error"}` {
		t.Errorf("error body = %s", pe.Body)
	}

	fake.setStatus(0)
	_, err = p.Send(t.Context(), Request{Body: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`)})
	if IsRetryable(err) || !asProviderError(err, &pe) || pe.Status != 400 || !strings.Contains(string(pe.Body), "unsupported content block") {
		t.Errorf("unsupported block = %v", err)
	}

	if _, err := NewProvider(t.Context(), ProviderOpenAI, envOf(nil)); err == nil || !strings.Contains(err.Error(), EnvOpenAIAPIKey) {
		t.Errorf("missing key = %v", err)
	}
}

func TestOpenAIModelsAndFamily(t *testing.T) {
	t.Parallel()
	for model, ok := range map[string]bool{"gpt-5.5": true, "gpt-4.1-mini": true, "o4-mini": true, "o3": true, "claude-opus-5-5": false, "omni": false, "": false} {
		if err := openAISpec.ValidateModel(model); (err == nil) != ok {
			t.Errorf("ValidateModel(%q) = %v", model, err)
		}
	}
	if f, _ := Family(ProviderOpenAI, "gpt-5.5"); f != FamilyOpenAI {
		t.Errorf("family = %q", f)
	}
}

func TestStrictCompatible(t *testing.T) {
	t.Parallel()
	closed := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"a"}, "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	open := map[string]any{"type": "object", "required": []string{"a"}, "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	optional := map[string]any{"type": "object", "additionalProperties": false, "required": []string{}, "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	nested := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"xs"},
		"properties": map[string]any{"xs": map[string]any{"type": "array", "items": open}},
	}
	for name, tc := range map[string]struct {
		schema map[string]any
		want   bool
	}{"closed": {closed, true}, "open": {open, false}, "optional": {optional, false}, "nested open": {nested, false}} {
		if got := strictCompatible(tc.schema); got != tc.want {
			t.Errorf("%s: strictCompatible = %v", name, got)
		}
	}
}

// TestGatewayFailsOverAcrossFamilies runs real providers on fakes behind the
// gateway: a failing first provider hands the call to the other family, and
// the response and model_calls record carry the family that answered.
func TestGatewayFailsOverAcrossFamilies(t *testing.T) {
	t.Parallel()
	claudeAPI, openaiAPI := &fakeMessagesAPI{}, &fakeChatAPI{}
	claudeSrv, openaiSrv := httptest.NewServer(claudeAPI), httptest.NewServer(openaiAPI)
	defer claudeSrv.Close()
	defer openaiSrv.Close()
	env := envOf(map[string]string{
		EnvClaudeAPIKey: "sk-ant", EnvClaudeBaseURL: claudeSrv.URL,
		EnvOpenAIAPIKey: "sk-oai", EnvOpenAIBaseURL: openaiSrv.URL,
	})
	providers := schema.Providers{
		Generator: []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-sonnet-5-5"}, {Provider: ProviderOpenAI, Model: "gpt-test"}},
		Verifier:  []schema.ProviderModel{{Provider: ProviderOpenAI, Model: "gpt-test"}, {Provider: ProviderClaude, Model: "claude-opus-5-5"}},
		Light:     []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-haiku-5-5"}},
	}
	clients, err := BuildProviders(t.Context(), providers, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	rec := &memRecorder{}
	g, err := NewGateway(Config{Providers: providers, Clients: clients, Scanner: sc, Token: "gw-token", Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, stage, provider, family, failover string
		claudeStatus, openaiStatus              int
	}{
		{"verifier on openai", StageVerifier, ProviderOpenAI, FamilyOpenAI, "false", 0, 0},
		{"openai down, verifier fails over to claude", StageVerifier, ProviderClaude, FamilyAnthropic, "true", 0, http.StatusServiceUnavailable},
		{"claude overloaded, generator fails over to openai", StageGenerator, ProviderOpenAI, FamilyOpenAI, "true", 529, 0},
	} {
		claudeAPI.mu.Lock()
		claudeAPI.status = tc.claudeStatus
		claudeAPI.mu.Unlock()
		openaiAPI.setStatus(tc.openaiStatus)
		w := call(t, g, map[string]string{HeaderStage: tc.stage}, msgBody)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.name, w.Code, w.Body)
		}
		h := w.Header()
		if h.Get(HeaderProvider) != tc.provider || h.Get(HeaderFamily) != tc.family || h.Get(HeaderFailover) != tc.failover {
			t.Errorf("%s: headers = %v", tc.name, h)
		}
		last := rec.calls[len(rec.calls)-1]
		if last.Provider != tc.provider || last.Stage != tc.stage || last.Failover != (tc.failover == "true") {
			t.Errorf("%s: recorded %+v", tc.name, last)
		}
	}
	// OpenAI calls are recorded with Messages-convention usage; prices
	// for the provider are not in the table, so cost is reported unknown.
	first := rec.calls[0]
	if first.Usage != (Usage{InputTokens: 20, OutputTokens: 7, CacheReadTokens: 100}) || first.PriceKnown {
		t.Errorf("openai call = %+v", first)
	}

	// Both families down: the stage is unavailable.
	claudeAPI.mu.Lock()
	claudeAPI.status = 529
	claudeAPI.mu.Unlock()
	openaiAPI.setStatus(http.StatusServiceUnavailable)
	if w := call(t, g, map[string]string{HeaderStage: StageVerifier}, msgBody); w.Code != http.StatusServiceUnavailable {
		t.Errorf("all down: status %d", w.Code)
	}
}
