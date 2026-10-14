package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/koblas/exemplar/internal/schema"
)

// fakeMessagesAPI records requests and answers like the Messages API.
type fakeMessagesAPI struct {
	mu     sync.Mutex
	path   string
	header http.Header
	body   map[string]any
	status int
}

func (f *fakeMessagesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.path, f.header = r.URL.Path, r.Header.Clone()
	_ = json.Unmarshal(data, &f.body)
	status := f.status
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
		return
	}
	_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":5,"cache_creation_input_tokens":0}}`)
}

func envOf(m map[string]string) Settings {
	return Settings{Getenv: func(k string) string { return m[k] }}
}

const rawRequest = `{"model":"claude-sonnet-5-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}}`

func TestClaudeProvider(t *testing.T) {
	fake := &fakeMessagesAPI{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	p, err := NewProvider(t.Context(), ProviderClaude, envOf(map[string]string{EnvClaudeAPIKey: "sk-test", EnvClaudeBaseURL: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Send(t.Context(), Request{Body: []byte(rawRequest), Beta: "some-beta-2026-01-01"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.path != "/v1/messages" || fake.header.Get("X-Api-Key") != "sk-test" || fake.header.Get("Anthropic-Beta") != "some-beta-2026-01-01" {
		t.Fatalf("request = %s %v", fake.path, fake.header)
	}
	if fake.body["model"] != "claude-sonnet-5-5" || fake.body["output_config"] == nil {
		t.Fatalf("body not passed through: %v", fake.body)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.CacheReadTokens != 5 || !strings.Contains(string(resp.Body), `"msg_1"`) {
		t.Fatalf("response = %+v", resp)
	}

	fake.status = 529
	_, err = p.Send(t.Context(), Request{Body: []byte(rawRequest)})
	if !IsRetryable(err) {
		t.Fatalf("529 should fail over: %v", err)
	}
	fake.status = 400
	_, err = p.Send(t.Context(), Request{Body: []byte(rawRequest)})
	var pe *ProviderError
	if IsRetryable(err) || !asProviderError(err, &pe) || pe.Status != 400 || !strings.Contains(string(pe.Body), "overloaded_error") {
		t.Fatalf("400 should pass through, not fail over: %v", err)
	}

	if _, err := NewProvider(t.Context(), ProviderClaude, envOf(nil)); err == nil {
		t.Fatal("claude provider built without an API key")
	}
}

func TestBedrockProviderSigV4(t *testing.T) {
	fake := &fakeMessagesAPI{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	p, err := NewProvider(t.Context(), ProviderBedrock, envOf(map[string]string{
		EnvBedrockRegion: "us-west-2", EnvBedrockBaseURL: srv.URL,
		"AWS_ACCESS_KEY_ID": "AKIDTEST", "AWS_SECRET_ACCESS_KEY": "secret-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(rawRequest, "claude-sonnet-5-5", "anthropic.claude-sonnet-5-5", 1)
	resp, err := p.Send(t.Context(), Request{Body: []byte(body)})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	auth := fake.header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !strings.Contains(auth, "/us-west-2/bedrock-mantle/aws4_request") {
		t.Fatalf("request not SigV4-signed for bedrock-mantle in us-west-2: %q", auth)
	}
	if fake.header.Get("X-Api-Key") != "" {
		t.Fatal("bedrock request carried an x-api-key")
	}
	if fake.path != "/v1/messages" || fake.body["model"] != "anthropic.claude-sonnet-5-5" {
		t.Fatalf("request = %s %v", fake.path, fake.body["model"])
	}
	if resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}

	if _, err := NewProvider(t.Context(), ProviderBedrock, envOf(nil)); err == nil {
		t.Fatal("bedrock provider built without a region")
	}
}

func TestRoutesValidateModelsPerProvider(t *testing.T) {
	good := schema.Providers{
		Generator: []schema.ProviderModel{
			{Provider: ProviderClaude, Standard: "claude-sonnet-5-5", High: "claude-opus-5-5"},
			{Provider: ProviderBedrock, Standard: "anthropic.claude-sonnet-5-5", High: "us.anthropic.claude-opus-5-5"},
		},
		Verifier: []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-opus-5-5"}},
		Light:    []schema.ProviderModel{{Provider: ProviderBedrock, Model: "anthropic.claude-haiku-5-5"}},
	}
	if err := ValidateProviders(good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if got := ConfiguredProviders(good); strings.Join(got, ",") != "bedrock,claude" {
		t.Fatalf("ConfiguredProviders = %v", got)
	}
	bad := good
	bad.Light = []schema.ProviderModel{{Provider: ProviderBedrock, Model: "claude-haiku-5-5"}} // missing anthropic. prefix
	if err := ValidateProviders(bad); err == nil {
		t.Fatal("bedrock route with a Claude API model id accepted")
	}
	bad.Light = []schema.ProviderModel{{Provider: ProviderClaude, Model: "anthropic.claude-haiku-5-5"}}
	if err := ValidateProviders(bad); err == nil {
		t.Fatal("claude route with a Bedrock model id accepted")
	}
}

func TestBedrockIsSameFamilyAsClaude(t *testing.T) {
	a, _ := Family(ProviderClaude, "claude-opus-5-5")
	b, _ := Family(ProviderBedrock, "anthropic.claude-opus-5-5")
	if a != b || a != FamilyAnthropic {
		t.Fatalf("families = %q, %q; Claude on Bedrock must count as the same family", a, b)
	}
}

func asProviderError(err error, out **ProviderError) bool {
	pe, ok := err.(*ProviderError) //nolint:errorlint // test helper on a known concrete type
	*out = pe
	return ok
}
