package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// fakeExtractor imitates the gateway's batch API. It answers each request
// from the comments in it: a body containing RULE is a reusable
// constraint; ERRORONCE makes the request error the first time; OMIT is
// left out of the answer.
type fakeExtractor struct {
	mu          sync.Mutex
	creates     int
	bodies      []string // request texts sent, for leak checks
	batches     map[string][]string
	erroredOnce map[string]bool
}

var commentRE = regexp.MustCompile(`(?s)<comment id="(\d+)" path="[^"]*">.*?<body>\n(.*?)\n</body>|<revert id="(-?\d+)" path="[^"]*">.*?<reason>\n(.*?)\n</reason>`)

func (f *fakeExtractor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/batches":
		f.creates++
		var in struct {
			Requests []struct {
				CustomID string `json:"custom_id"`
				Params   struct {
					Messages []struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"messages"`
				} `json:"params"`
			} `json:"requests"`
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &in)
		id := fmt.Sprintf("msgbatch_%d", f.creates)
		lines := make([]string, 0, len(in.Requests))
		for _, req := range in.Requests {
			text := req.Params.Messages[0].Content[0].Text
			f.bodies = append(f.bodies, text)
			lines = append(lines, f.answer(req.CustomID, text))
		}
		f.batches[id] = lines
		w.Header().Set(llm.HeaderModel, "claude-sonnet-5-5")
		fmt.Fprintf(w, `{"id":%q,"type":"message_batch","processing_status":"in_progress","request_counts":{"processing":1,"succeeded":0,"errored":0,"canceled":0,"expired":0},`+
			`"created_at":"2026-10-12T00:00:00Z","expires_at":"2026-10-13T00:00:00Z","ended_at":null,"cancel_initiated_at":null,"archived_at":null,"results_url":null}`, id)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/results"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/messages/batches/"), "/results")
		fmt.Fprint(w, strings.Join(f.batches[id], "\n")+"\n")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/messages/batches/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/messages/batches/")
		fmt.Fprintf(w, `{"id":%q,"type":"message_batch","processing_status":"ended","request_counts":{"processing":0,"succeeded":1,"errored":0,"canceled":0,"expired":0},`+
			`"created_at":"2026-10-12T00:00:00Z","expires_at":"2026-10-13T00:00:00Z","ended_at":"2026-10-12T01:00:00Z","cancel_initiated_at":null,"archived_at":null,"results_url":"x"}`, id)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeExtractor) answer(customID, text string) string {
	if strings.Contains(text, "ERRORONCE") && !f.erroredOnce[customID] {
		f.erroredOnce[customID] = true
		return `{"custom_id":"` + customID + `","result":{"type":"errored","error":{"type":"error","error":{"type":"overloaded_error","message":"busy"}}}}`
	}
	var results []Result
	for _, m := range commentRE.FindAllStringSubmatch(text, -1) {
		var id int64
		if m[1] == "" { // a revert item
			m[1], m[2] = m[3], m[4]
		}
		if _, err := fmt.Sscan(m[1], &id); err != nil {
			continue
		}
		body := m[2]
		switch {
		case strings.Contains(body, "OMIT"):
			continue
		case strings.Contains(body, "RULE"):
			results = append(results, Result{
				CommentID: id, IsReusable: true, Title: "Set timeouts on outbound calls",
				Description:       "Every outbound HTTP call needs a timeout so a slow dependency cannot hang the service.",
				PreferredApproach: "Use an http.Client with Timeout set.", Category: "Reliability", LanguageAgnostic: true, Confidence: 0.9,
			})
		default:
			results = append(results, Result{CommentID: id, Confidence: 0.95})
		}
	}
	answer, _ := json.Marshal(output{Results: results}) //nolint:errchkjson // test fixture: plain structs of strings and numbers always encode
	//nolint:errchkjson // test fixture: maps of strings and numbers always encode
	msg, _ := json.Marshal(map[string]any{
		"id": "msg", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"content": []any{map[string]any{"type": "text", "text": string(answer)}}, "stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 300, "output_tokens": 80, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
	})
	return `{"custom_id":"` + customID + `","result":{"type":"succeeded","message":` + string(msg) + `}}`
}

func fakeToken() string {
	sum := sha256.Sum256([]byte("extract test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

func seed(t *testing.T, s *store.Store, id int64, body string) {
	t.Helper()
	seedIn(t, s, id, "acme/app", body)
}

func seedIn(t *testing.T, s *store.Store, id int64, repo, body string) {
	t.Helper()
	b, h := body, "@@ -1 +1 @@\n-x\n+y"
	if err := s.InsertMinedComment(t.Context(), queries.InsertMinedCommentParams{
		ID: id, Repo: repo, PrNumber: 1, Path: "a.go", Ext: ".go", ReviewerPid: "r", AuthorPid: "a",
		CreatedAt: tsNow(), CommentUrl: "https://github.com/acme/app/pull/1#discussion_r" + strconv.FormatInt(id, 10),
		Body: &b, DiffHunk: &h, DocUrls: []string{}, IsThreadRoot: true, Source: "human",
	}); err != nil {
		t.Fatal(err)
	}
}

func statusOf(t *testing.T, s *store.Store, id int64) (string, int) {
	t.Helper()
	var status string
	var attempts int
	if err := s.Pool.QueryRow(t.Context(), "SELECT extract_status, extract_attempts FROM mined_comments WHERE id = $1", id).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	return status, attempts
}

func newDriver(t *testing.T, s *store.Store) (*Driver, *fakeExtractor) {
	t.Helper()
	return newDriverFor(t, s, func(string) bool { return true })
}

func newDriverFor(t *testing.T, s *store.Store, eligible func(string) bool) (*Driver, *fakeExtractor) {
	t.Helper()
	fake := &fakeExtractor{batches: map[string][]string{}, erroredOnce: map[string]bool{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{GatewayURL: srv.URL, GatewayToken: "gw", Eligible: eligible}, s, sc)
	if err != nil {
		t.Fatal(err)
	}
	return d, fake
}

// A repo can leave eligibility after its comments were mined: nothing
// from it may reach the model, and its comments stop being pending so
// they do not hold the front of every batch.
func TestSubmitSkipsIneligibleRepos(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	d, fake := newDriverFor(t, s, func(repo string) bool { return repo != "acme/gone" })
	seed(t, s, 1, "RULE: always set a timeout on outbound HTTP calls")
	seedIn(t, s, 2, "acme/gone", "RULE: never log request bodies; they carry tokens")

	rep, err := d.Submit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Submitted != 1 || rep.Ineligible != 1 {
		t.Fatalf("submit = %+v", rep)
	}
	for _, b := range fake.bodies {
		if strings.Contains(b, "never log request bodies") {
			t.Fatal("an ineligible repo's comment reached the extractor")
		}
	}
	if st, _ := statusOf(t, s, 2); st != "error" {
		t.Fatalf("ineligible comment = %s, want error", st)
	}
	if rep, err := d.Submit(ctx); err != nil || rep.Submitted != 0 || rep.Ineligible != 0 {
		t.Fatalf("second submit = %+v, %v", rep, err)
	}
}

func TestNewRequiresEligibility(t *testing.T) {
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{GatewayURL: "http://gw.invalid", GatewayToken: "gw"}, nil, sc); err == nil {
		t.Fatal("New accepted a config without an eligibility check")
	}
}

func TestExtractionLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	d, fake := newDriver(t, s)

	seed(t, s, 1, "RULE: always set a timeout on outbound HTTP calls")
	seed(t, s, 2, "nit: rename this variable")
	seed(t, s, 3, "RULE: never log request bodies; they carry tokens")
	seed(t, s, 4, "lgtm")
	seed(t, s, 5, "the key "+fakeToken()+" leaked here")
	seed(t, s, 6, "ERRORONCE: odd request")
	seed(t, s, 7, "OMIT this one")

	rep, err := d.Submit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Submitted != 7 || rep.BatchID == "" {
		t.Fatalf("submit = %+v", rep)
	}
	for _, b := range fake.bodies {
		if strings.Contains(b, fakeToken()) {
			t.Fatal("secret reached the extractor")
		}
		if !strings.Contains(b, "<comments>") {
			t.Fatalf("unexpected request text %q", b)
		}
	}
	if st, _ := statusOf(t, s, 1); st != "in_batch" {
		t.Fatalf("comment 1 = %s after submit", st)
	}

	col, err := d.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.Batches) != 1 {
		t.Fatalf("collect = %+v", col)
	}
	// Requests are [1 2 3] [4 5 6] [7]: the second errored, the third
	// omitted its comment.
	out := col.Batches[0]
	if out.Accepted != 2 || out.Rejected != 1 || out.Returned != 4 {
		t.Fatalf("batch outcome = %+v", out)
	}
	for id, want := range map[int64]string{1: "accepted", 2: "rejected", 3: "accepted", 4: "pending", 5: "pending", 6: "pending", 7: "pending"} {
		if st, _ := statusOf(t, s, id); st != want {
			t.Errorf("comment %d = %s, want %s", id, st, want)
		}
	}
	var title string
	var agnostic bool
	if err := s.Pool.QueryRow(ctx, "SELECT title, language_agnostic FROM extractions WHERE comment_id = 1").Scan(&title, &agnostic); err != nil || title == "" || !agnostic {
		t.Fatalf("extraction 1 = %q %v %v", title, agnostic, err)
	}

	// Second pass: the errored request now succeeds; the omitted comment
	// keeps coming back until it stops at error.
	for range 2 {
		if _, err := d.Submit(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Collect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[int64]string{4: "rejected", 5: "rejected", 6: "rejected", 7: "error"} {
		if st, _ := statusOf(t, s, id); st != want {
			t.Errorf("comment %d = %s, want %s", id, st, want)
		}
	}
	if _, attempts := statusOf(t, s, 7); attempts != 3 {
		t.Errorf("comment 7 attempts = %d, want 3", attempts)
	}
	var reason string
	_ = s.Pool.QueryRow(ctx, "SELECT extract_error FROM mined_comments WHERE id = 7").Scan(&reason)
	if !strings.Contains(reason, "omitted") {
		t.Errorf("comment 7 reason = %q", reason)
	}

	// A new comment with content identical to comment 1 is answered from
	// the cache: no new batch, no model call.
	creates := fake.creates
	seed(t, s, 8, "RULE: always set a timeout on outbound HTTP calls")
	rep, err = d.Submit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CacheHits != 1 || rep.Submitted != 0 || fake.creates != creates {
		t.Fatalf("cache: report %+v, creates %d -> %d", rep, creates, fake.creates)
	}
	if st, _ := statusOf(t, s, 8); st != "accepted" {
		t.Fatalf("comment 8 = %s, want accepted from cache", st)
	}
	col, err = d.Collect(ctx)
	if err != nil || len(col.Batches) != 0 || col.Waiting != 0 {
		t.Fatalf("nothing left to collect: %+v %v", col, err)
	}
}

func TestCustomIDRoundTrip(t *testing.T) {
	cs := []Comment{{ID: 1234567890123}, {ID: 2}, {ID: 99999999999}}
	id := customID(cs)
	if len(id) > 64 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(id) {
		t.Fatalf("custom id %q breaks the API's format", id)
	}
	got, err := parseCustomID(id)
	if err != nil || len(got) != 3 || got[0] != 1234567890123 || got[2] != 99999999999 {
		t.Fatalf("parse = %v, %v", got, err)
	}
	for _, bad := range []string{"", "c", "x-1", "c-1-2-3-4", "c-0", "c--1", "c-a"} {
		if _, err := parseCustomID(bad); err == nil {
			t.Errorf("parseCustomID(%q) accepted", bad)
		}
	}
}

func TestInputHash(t *testing.T) {
	a := InputHash(Version, "m", "ab", "c")
	if a == InputHash(Version, "m", "a", "bc") {
		t.Fatal("hash ignores field boundaries")
	}
	if a != InputHash(Version, "m", "ab", "c") || a == InputHash("extract-v2", "m", "ab", "c") || a == InputHash(Version, "other", "ab", "c") {
		t.Fatal("hash must be stable and depend on version and model")
	}
}

func TestResultValidation(t *testing.T) {
	ok := Result{IsReusable: true, Title: "t", Description: "d", Category: "Security", Confidence: 0.5}
	if ok.validate() != nil {
		t.Fatal("valid result rejected")
	}
	bad := []Result{
		{IsReusable: true, Description: "d", Category: "c", Confidence: 0.5},
		{IsReusable: true, Title: strings.Repeat("x", 200), Description: "d", Category: "c", Confidence: 0.5},
		{IsReusable: true, Title: "t", Description: "d", Category: "", Confidence: 0.5},
		{Confidence: 1.5},
	}
	for i, r := range bad {
		if r.validate() == nil {
			t.Errorf("bad result %d accepted", i)
		}
	}
}

func tsNow() pgtype.Timestamptz { return pgtype.Timestamptz{Time: time.Now(), Valid: true} }

// TestThroughRealGateway runs the driver against the real gateway handler
// (headers, data classes, secret scan, stage routing, batch accounting),
// with the Claude provider pointed at the fake batches API.
func TestThroughRealGateway(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	fake := &fakeExtractor{batches: map[string][]string{}, erroredOnce: map[string]bool{}}
	api := httptest.NewServer(fake)
	t.Cleanup(api.Close)

	claude, err := llm.NewProvider(ctx, llm.ProviderClaude, llm.Settings{Getenv: func(k string) string {
		return map[string]string{llm.EnvClaudeAPIKey: "sk-test", llm.EnvClaudeBaseURL: api.URL}[k]
	}})
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	gw, err := llm.NewGateway(llm.Config{
		Providers: schema.Providers{
			Generator: []schema.ProviderModel{{Provider: llm.ProviderClaude, Standard: "claude-sonnet-5-5", High: "claude-opus-5-5"}},
			Verifier:  []schema.ProviderModel{{Provider: llm.ProviderClaude, Model: "claude-opus-5-5"}},
			Light:     []schema.ProviderModel{{Provider: llm.ProviderClaude, Model: "claude-haiku-5-5"}},
		},
		Clients:  map[string]llm.Provider{llm.ProviderClaude: claude},
		Scanner:  sc,
		Token:    "gw",
		Recorder: store.CallRecorder{Store: s},
		Batches:  store.BatchStore{Store: s},
	})
	if err != nil {
		t.Fatal(err)
	}
	gwSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(gwSrv.Close)

	d, err := New(Config{GatewayURL: gwSrv.URL, GatewayToken: "gw", Eligible: func(string) bool { return true }}, s, sc)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, s, 1, "RULE: always set a timeout on outbound HTTP calls")
	seed(t, s, 2, "the key "+fakeToken()+" leaked")
	if rep, err := d.Submit(ctx); err != nil || rep.Submitted != 2 {
		t.Fatalf("submit through gateway = %+v, %v", rep, err)
	}
	if _, err := d.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := statusOf(t, s, 1); st != "accepted" {
		t.Fatalf("comment 1 = %s", st)
	}
	var calls int
	var stage, caller, classes string
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) OVER (), stage, caller, array_to_string(data_classes, ',') `+
		`FROM model_calls WHERE batch_id IS NOT NULL`).Scan(&calls, &stage, &caller, &classes); err != nil {
		t.Fatalf("batch usage not booked: %v", err)
	}
	if calls != 1 || stage != llm.StageGenerator || caller != "learning" || classes != "review_comments,diff" {
		t.Fatalf("model_calls = %d %s %s %s", calls, stage, caller, classes)
	}
}

func TestRevertSeedsGetTheirOwnBatch(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	d, fake := newDriver(t, s)
	seed(t, s, 1, "RULE: always set a timeout on outbound HTTP calls")
	head := "ddddddd4444444"
	start, end := int32(20), int32(22)
	if err := s.UpsertRevertSeed(ctx, queries.UpsertRevertSeedParams{
		ID: -4242, Repo: "acme/app", PrNumber: 7, Path: "svc/cache.go", Ext: ".go", ReviewerPid: "r", AuthorPid: "a",
		CreatedAt: tsNow(), CommentUrl: "https://github.com/acme/app/pull/9",
		Body: "Revert: Revert \"Add price cache\"\nReason: RULE stale prices after deploys", DiffHunk: "@@ -18,2 +20,3 @@\n+cache.Set(k, v)",
		DocUrls: []string{}, CommitSha: &head, StartLine: &start, EndLine: &end,
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := d.Submit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Submitted != 2 || rep.BatchID == "" || rep.SeedBatchID == "" || rep.BatchID == rep.SeedBatchID || fake.creates != 2 {
		t.Fatalf("submit = %+v, creates = %d", rep, fake.creates)
	}
	var sawRevert bool
	for _, b := range fake.bodies {
		if strings.Contains(b, "<reverts>") {
			sawRevert = true
			if strings.Contains(b, "<comments>") {
				t.Error("a request mixes comments and revert seeds")
			}
		}
	}
	if !sawRevert {
		t.Fatal("no revert request sent")
	}
	if _, err := d.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{1: Version, -4242: RevertVersion} {
		var got string
		if err := s.Pool.QueryRow(ctx, `SELECT extractor_version FROM extractions WHERE comment_id = $1`, id).Scan(&got); err != nil || got != want {
			t.Errorf("extraction %d version = %q, %v; want %q", id, got, err, want)
		}
	}
	var versions []string
	rows, _ := s.Pool.Query(ctx, `SELECT extractor_version FROM extraction_batches ORDER BY extractor_version`)
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		versions = append(versions, v)
	}
	if strings.Join(versions, ",") != RevertVersion+","+Version {
		t.Errorf("batch versions = %v", versions)
	}
}

func TestCustomIDSeedIDs(t *testing.T) {
	cs := []Comment{{ID: 9223372036854775807}, {ID: -9223372036854775807}, {ID: 12}}
	id := customID(cs)
	if len(id) > 64 {
		t.Fatalf("custom_id %q is longer than 64", id)
	}
	got, err := parseCustomID(id)
	if err != nil || len(got) != 3 || got[0] != cs[0].ID || got[1] != cs[1].ID || got[2] != 12 {
		t.Fatalf("parseCustomID(%q) = %v, %v", id, got, err)
	}
	for _, bad := range []string{"c-0", "c-n0", "c--5", "x-1", "c-1-2-3-4"} {
		if _, err := parseCustomID(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}
