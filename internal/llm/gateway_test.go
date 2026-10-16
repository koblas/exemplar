package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

type fakeProvider struct {
	name  string
	fail  error
	usage Usage
	mu    sync.Mutex
	got   []map[string]any
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Send(_ context.Context, req Request) (*Response, error) {
	var m map[string]any
	_ = json.Unmarshal(req.Body, &m)
	f.mu.Lock()
	f.got = append(f.got, m)
	f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	body, err := json.Marshal(map[string]any{"type": "message", "content": []any{}, "usage": f.usage})
	if err != nil {
		return nil, err
	}
	return &Response{Status: 200, Body: body, Usage: f.usage}, nil
}

type memRecorder struct {
	mu    sync.Mutex
	calls []Call
}

func (m *memRecorder) RecordCall(_ context.Context, c Call) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, c)
	return nil
}

// withSecondProvider approves a test-only second provider family for the
// duration of the test, so failover across families can be exercised.
func withSecondProvider(t *testing.T) {
	t.Helper()
	registry["fake-b"] = Spec{
		Family:        func(string) string { return "fake-b" },
		ValidateModel: func(string) error { return nil },
		New:           func(context.Context, Settings) (Provider, error) { return &fakeProvider{name: "fake-b"}, nil },
	}
	t.Cleanup(func() { delete(registry, "fake-b") })
}

func testProviders() schema.Providers {
	return schema.Providers{
		Generator: []schema.ProviderModel{{Provider: ProviderClaude, Standard: "claude-sonnet-5-5", High: "claude-opus-5-5"}, {Provider: "fake-b", Model: "b-standard"}},
		Verifier:  []schema.ProviderModel{{Provider: "fake-b", Model: "b-standard"}, {Provider: ProviderClaude, Model: "claude-opus-5-5"}},
		Light:     []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-haiku-5-5"}},
	}
}

func newTestGateway(t *testing.T, a, b *fakeProvider, rec Recorder, ceiling float64) *Gateway {
	t.Helper()
	withSecondProvider(t)
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGateway(Config{
		Providers: testProviders(),
		Clients:   map[string]Provider{ProviderClaude: a, "fake-b": b},
		Scanner:   sc, Token: "gw-token", SoftCeilingUSD: ceiling, Recorder: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func call(t *testing.T, g *Gateway, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("X-Api-Key", "gw-token")
	req.Header.Set(HeaderStage, StageGenerator)
	req.Header.Set(HeaderCaller, "eval")
	req.Header.Set(HeaderDataClasses, "diff")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	return w
}

const msgBody = `{"model":"tier","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`

func TestGatewayRoutesTierAndRecords(t *testing.T) {
	a := &fakeProvider{name: ProviderClaude, usage: Usage{InputTokens: 1000, OutputTokens: 100}}
	b := &fakeProvider{name: "fake-b"}
	rec := &memRecorder{}
	g := newTestGateway(t, a, b, rec, 10)

	w := call(t, g, map[string]string{HeaderTier: TierHigh, HeaderReviewID: "5", HeaderDataClasses: "diff, pr_text"}, msgBody)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := a.got[0]["model"]; got != "claude-opus-5-5" {
		t.Errorf("routed model = %v, want claude-opus-5-5 for tier high", got)
	}
	if w.Header().Get(HeaderFamily) != FamilyAnthropic || w.Header().Get(HeaderFailover) != "false" {
		t.Errorf("headers = %v", w.Header())
	}
	if len(rec.calls) != 1 {
		t.Fatalf("recorded %d calls", len(rec.calls))
	}
	c := rec.calls[0]
	wantCost := (1000*4.0 + 100*20.0) / 1e6
	if c.CostUSD != wantCost || !c.PriceKnown || *c.ReviewID != 5 || strings.Join(c.DataClasses, ",") != "diff,pr_text" {
		t.Errorf("call = %+v, want cost %v", c, wantCost)
	}
}

func TestGatewayFailover(t *testing.T) {
	a := &fakeProvider{name: ProviderClaude, fail: &ProviderError{Status: 529, Retryable: true}}
	b := &fakeProvider{name: "fake-b"}
	rec := &memRecorder{}
	g := newTestGateway(t, a, b, rec, 10)

	w := call(t, g, nil, msgBody)
	if w.Code != 200 || w.Header().Get(HeaderProvider) != "fake-b" || w.Header().Get(HeaderFailover) != "true" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	if !rec.calls[0].Failover {
		t.Error("failover not recorded")
	}

	// A non-retryable 400 does not fail over; the caller sees it.
	a.fail = &ProviderError{Status: 400, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)}
	before := len(b.got)
	w = call(t, g, nil, msgBody)
	if w.Code != 400 || len(b.got) != before {
		t.Fatalf("400 handling: status %d, provider B calls %d->%d", w.Code, before, len(b.got))
	}

	// Every provider down: 503, which the orchestrator treats as incomplete.
	b.fail = &ProviderError{Status: 503, Retryable: true}
	a.fail = &ProviderError{Status: 503, Retryable: true}
	if w = call(t, g, nil, msgBody); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("all down: status %d", w.Code)
	}
}

func TestGatewayRefusesSecrets(t *testing.T) {
	a := &fakeProvider{name: ProviderClaude}
	g := newTestGateway(t, a, &fakeProvider{name: "fake-b"}, nil, 10)
	sum := sha256.Sum256([]byte("gateway test token"))
	tok := "ghp_" + hex.EncodeToString(sum[:])[:36]
	body := `{"model":"x","max_tokens":10,"messages":[{"role":"user","content":"const t = \"` + tok + `\"\n"}]}`
	w := call(t, g, nil, body)
	if w.Code != http.StatusUnprocessableEntity || len(a.got) != 0 {
		t.Fatalf("status %d, provider calls %d; want 422 and nothing sent", w.Code, len(a.got))
	}
	if strings.Contains(w.Body.String(), tok) {
		t.Fatal("error response echoes the secret")
	}
}

func TestGatewayRejectsBadRequests(t *testing.T) {
	g := newTestGateway(t, &fakeProvider{name: ProviderClaude}, &fakeProvider{name: "fake-b"}, nil, 10)
	tests := []struct {
		name    string
		headers map[string]string
		body    string
		want    int
	}{
		{"wrong token", map[string]string{"x-api-key": "nope"}, msgBody, 401},
		{"no data classes", map[string]string{HeaderDataClasses: ""}, msgBody, 400},
		{"unknown data class", map[string]string{HeaderDataClasses: "secrets"}, msgBody, 400},
		{"unknown caller", map[string]string{HeaderCaller: "someone"}, msgBody, 400},
		{"unknown stage", map[string]string{HeaderStage: "planner"}, msgBody, 400},
		{"bad review id", map[string]string{HeaderReviewID: "-1"}, msgBody, 400},
		{"not json", nil, "nope", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := call(t, g, tt.headers, tt.body); w.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
		})
	}
}

func TestGatewayBudgetSignal(t *testing.T) {
	// Each call costs 1000*2/1e6 = $0.002 on the standard tier.
	a := &fakeProvider{name: ProviderClaude, usage: Usage{InputTokens: 1000}}
	g := newTestGateway(t, a, &fakeProvider{name: "fake-b"}, nil, 0.005)
	h := map[string]string{HeaderReviewID: "9"}
	want := []string{BudgetOK, BudgetWarn, BudgetExceeded}
	for i, w := range want {
		resp := call(t, g, h, msgBody)
		if got := resp.Header().Get(HeaderBudget); got != w {
			t.Fatalf("call %d budget = %s, want %s", i+1, got, w)
		}
	}
}

func TestUnapprovedProvidersRefused(t *testing.T) {
	p := testProviders() // names fake-b, which is not approved outside withSecondProvider
	if err := ValidateProviders(p); err == nil {
		t.Fatal("unapproved provider accepted")
	}
	sc, _ := redact.New()
	_, err := NewGateway(Config{Providers: schema.Providers{
		Generator: []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-m"}},
		Verifier:  []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-m"}},
		Light:     []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-m"}},
	}, Clients: map[string]Provider{"rogue": &fakeProvider{name: "rogue"}}, Scanner: sc, Token: "t"})
	if err == nil {
		t.Fatal("client for unapproved provider accepted")
	}
}

func TestCostUnknownModel(t *testing.T) {
	if c, ok := Cost(ProviderBedrock, "anthropic.claude-opus-5-5", Usage{InputTokens: 10}); ok || c != 0 {
		t.Fatalf("Cost(unknown) = %v, %v", c, ok)
	}
}

func TestIsRetryable(t *testing.T) {
	if IsRetryable(errors.New("x")) || !IsRetryable(&ProviderError{Retryable: true}) {
		t.Fatal("IsRetryable wrong")
	}
}
