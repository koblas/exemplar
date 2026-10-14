package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

// fakeBatchesAPI imitates the Anthropic Message Batches endpoints.
type fakeBatchesAPI struct {
	mu       sync.Mutex
	created  map[string]any // last create body
	canceled bool
	results  []string // JSONL lines
}

func (f *fakeBatchesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/messages/batches":
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &f.created)
		fmt.Fprint(w, batchJSON("in_progress", ""))
	case r.Method == "GET" && r.URL.Path == "/v1/messages/batches/msgbatch_1":
		fmt.Fprint(w, batchJSON("ended", "http://"+r.Host+"/v1/messages/batches/msgbatch_1/results"))
	case r.Method == "POST" && r.URL.Path == "/v1/messages/batches/msgbatch_1/cancel":
		f.canceled = true
		fmt.Fprint(w, batchJSON("canceling", ""))
	case r.Method == "GET" && r.URL.Path == "/v1/messages/batches/msgbatch_1/results":
		w.Header().Set("Content-Type", "application/binary")
		fmt.Fprint(w, strings.Join(f.results, "\n")+"\n")
	default:
		w.WriteHeader(404)
		fmt.Fprint(w, `{"type":"error","error":{"type":"not_found_error","message":"no"}}`)
	}
}

func batchJSON(status, resultsURL string) string {
	results := "null"
	if resultsURL != "" {
		results = `"` + resultsURL + `"`
	}
	return `{"id":"msgbatch_1","type":"message_batch","processing_status":"` + status + `","request_counts":{"processing":0,"succeeded":2,"errored":1,"canceled":0,"expired":0},"created_at":"2026-10-12T00:00:00Z","expires_at":"2026-10-13T00:00:00Z","ended_at":null,"cancel_initiated_at":null,"archived_at":null,"results_url":` + results + `}`
}

func succeededLine(id string, in, out int) string {
	return `{"custom_id":"` + id + `","result":{"type":"succeeded","message":{"id":"msg_` + id + `","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":` + fmt.Sprint(in) + `,"output_tokens":` + fmt.Sprint(out) + `,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}}`
}

// newBatchGateway runs a gateway whose light stage routes to a fake
// Bedrock-like provider (no batches) and then the Claude provider pointed at
// fakeBatchesAPI, so batches must skip the first route.
func newBatchGateway(t *testing.T, rec Recorder) (*httptest.Server, *fakeBatchesAPI) {
	t.Helper()
	api := &fakeBatchesAPI{results: []string{
		succeededLine("c1", 1000, 100),
		succeededLine("c2", 2000, 200),
		`{"custom_id":"c3","result":{"type":"errored","error":{"type":"error","error":{"type":"invalid_request_error","message":"x"}}}}`,
	}}
	apiSrv := httptest.NewServer(api)
	t.Cleanup(apiSrv.Close)

	claude, err := NewProvider(t.Context(), ProviderClaude, envOf(map[string]string{EnvClaudeAPIKey: "sk-test", EnvClaudeBaseURL: apiSrv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	gw, err := NewGateway(Config{
		Providers: schema.Providers{
			Generator: []schema.ProviderModel{{Provider: ProviderClaude, Standard: "claude-sonnet-5-5", High: "claude-opus-5-5"}},
			Verifier:  []schema.ProviderModel{{Provider: ProviderClaude, Model: "claude-opus-5-5"}},
			Light: []schema.ProviderModel{
				{Provider: ProviderBedrock, Model: "anthropic.claude-haiku-5-5"},
				{Provider: ProviderClaude, Model: "claude-haiku-5-5"},
			},
		},
		Clients:  map[string]Provider{ProviderClaude: claude, ProviderBedrock: &fakeProvider{name: ProviderBedrock}},
		Scanner:  sc,
		Token:    "gw-token",
		Recorder: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return srv, api
}

func sdkClient(srv *httptest.Server) anthropic.Client {
	return anthropic.NewClient(
		option.WithBaseURL(srv.URL), option.WithAPIKey("gw-token"), option.WithMaxRetries(0),
		option.WithHeader(HeaderStage, StageLight), option.WithHeader(HeaderCaller, "learning"),
		option.WithHeader(HeaderDataClasses, "review_comments,diff"),
	)
}

func batchParams(texts ...string) anthropic.MessageBatchNewParams {
	var reqs []anthropic.MessageBatchNewParamsRequest
	for i, txt := range texts {
		reqs = append(reqs, anthropic.MessageBatchNewParamsRequest{
			CustomID: fmt.Sprintf("c%d", i+1),
			Params: anthropic.MessageBatchNewParamsRequestParams{
				Model: "routed-by-gateway", MaxTokens: 512,
				Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(txt))},
			},
		})
	}
	return anthropic.MessageBatchNewParams{Requests: reqs}
}

// TestBatchRoundTripWithSDK drives the gateway with the real SDK, as the
// learning plane will: create, poll, cancel, stream results.
func TestBatchRoundTripWithSDK(t *testing.T) {
	rec := &memRecorder{}
	srv, api := newBatchGateway(t, rec)
	client := sdkClient(srv)
	ctx := t.Context()

	b, err := client.Messages.Batches.New(ctx, batchParams("extract a rule from comment 1", "extract a rule from comment 2", "comment 3"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.ID != "msgbatch_1" {
		t.Fatalf("batch = %+v", b)
	}
	// The gateway skipped Bedrock (no batch support) and routed every
	// request to the Claude light model.
	reqs := api.created["requests"].([]any)
	for _, r := range reqs {
		if m := r.(map[string]any)["params"].(map[string]any)["model"]; m != "claude-haiku-5-5" {
			t.Fatalf("routed model = %v", m)
		}
	}

	got, err := client.Messages.Batches.Get(ctx, b.ID, anthropic.MessageBatchGetParams{})
	if err != nil || got.ProcessingStatus != "ended" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := client.Messages.Batches.Cancel(ctx, b.ID, anthropic.MessageBatchCancelParams{}); err != nil || !api.canceled {
		t.Fatalf("cancel: %v", err)
	}

	for range 2 { // reading results twice books usage once
		stream := client.Messages.Batches.ResultsStreaming(ctx, b.ID, anthropic.MessageBatchResultsParams{})
		var ids []string
		for stream.Next() {
			ids = append(ids, stream.Current().CustomID)
		}
		if err := stream.Err(); err != nil {
			t.Fatalf("results: %v", err)
		}
		if strings.Join(ids, ",") != "c1,c2,c3" {
			t.Fatalf("results = %v", ids)
		}
	}
	if len(rec.calls) != 1 {
		t.Fatalf("recorded %d usage rows, want exactly 1 per batch", len(rec.calls))
	}
	c := rec.calls[0]
	wantCost := (3000*0.10 + 300*0.50) / 1e6 * BatchDiscount
	if c.BatchID == nil || *c.BatchID != "msgbatch_1" || c.Usage.InputTokens != 3000 || c.Usage.OutputTokens != 300 || c.CostUSD != wantCost || c.Caller != "learning" || c.Stage != StageLight {
		t.Fatalf("usage row = %+v (want cost %v)", c, wantCost)
	}
	if strings.Join(c.DataClasses, ",") != "review_comments,diff" {
		t.Fatalf("data classes = %v", c.DataClasses)
	}
}

func TestBatchRefusals(t *testing.T) {
	srv, _ := newBatchGateway(t, nil)
	post := func(headers map[string]string, body string) int {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/v1/messages/batches", strings.NewReader(body))
		req.Header.Set("x-api-key", "gw-token")
		req.Header.Set(HeaderStage, StageLight)
		req.Header.Set(HeaderCaller, "learning")
		req.Header.Set(HeaderDataClasses, "review_comments")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	one := func(id, text string) string {
		return `{"custom_id":"` + id + `","params":{"model":"x","max_tokens":10,"messages":[{"role":"user","content":` + strconvQuote(text) + `}]}}`
	}
	sum := sha256.Sum256([]byte("batch test token"))
	secret := "token = \"ghp_" + hex.EncodeToString(sum[:])[:36] + "\""

	tests := []struct {
		name    string
		headers map[string]string
		body    string
		want    int
	}{
		{"wrong token", map[string]string{"x-api-key": "nope"}, `{"requests":[` + one("a", "hi") + `]}`, 401},
		{"no data classes", map[string]string{HeaderDataClasses: ""}, `{"requests":[` + one("a", "hi") + `]}`, 400},
		{"empty batch", nil, `{"requests":[]}`, 400},
		{"duplicate ids", nil, `{"requests":[` + one("a", "x") + `,` + one("a", "y") + `]}`, 400},
		{"unknown field", nil, `{"requests":[` + one("a", "x") + `],"evil":1}`, 400},
		{"secret in one request", nil, `{"requests":[` + one("a", "fine") + `,` + one("b", secret) + `]}`, 422},
		{"another batch-capable stage", map[string]string{HeaderStage: StageVerifier}, `{"requests":[` + one("a", "hi") + `]}`, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := post(tt.headers, tt.body); got != tt.want {
				t.Fatalf("status %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBatchNoBatchCapableProvider(t *testing.T) {
	sc, _ := redact.New()
	gw, err := NewGateway(Config{
		Providers: schema.Providers{
			Generator: []schema.ProviderModel{{Provider: ProviderBedrock, Standard: "anthropic.claude-sonnet-5-5", High: "anthropic.claude-opus-5-5"}},
			Verifier:  []schema.ProviderModel{{Provider: ProviderBedrock, Model: "anthropic.claude-opus-5-5"}},
			Light:     []schema.ProviderModel{{Provider: ProviderBedrock, Model: "anthropic.claude-haiku-5-5"}},
		},
		Clients: map[string]Provider{ProviderBedrock: &fakeProvider{name: ProviderBedrock}},
		Scanner: sc, Token: "gw-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/messages/batches", strings.NewReader(`{"requests":[{"custom_id":"a","params":{"model":"x","max_tokens":1,"messages":[]}}]}`))
	req.Header.Set("x-api-key", "gw-token")
	req.Header.Set(HeaderStage, StageLight)
	req.Header.Set(HeaderCaller, "learning")
	req.Header.Set(HeaderDataClasses, "review_comments")
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Message Batches") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestBatchUnknownID(t *testing.T) {
	srv, _ := newBatchGateway(t, nil)
	req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/v1/messages/batches/msgbatch_unknown/results", nil)
	req.Header.Set("x-api-key", "gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestMemoryBatchStoreClaimsOnce(t *testing.T) {
	m := NewMemoryBatchStore()
	ctx := context.Background()
	_ = m.SaveBatch(ctx, BatchRecord{ID: "b"})
	first, _ := m.ClaimAccounting(ctx, "b")
	second, _ := m.ClaimAccounting(ctx, "b")
	if !first || second {
		t.Fatalf("claims = %v, %v", first, second)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
