package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v88/github"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

// flagHTTPGet is a scripted model: the context rule and the baseline flag
// the http.Get line; every other agent reads its files and reports nothing.
func flagHTTPGet(task string, files []string) ([]string, string) {
	for _, f := range files {
		if f == "handler.go" && (strings.Contains(task, "deadlines reach them") || strings.Contains(task, "Review every changed file")) {
			return files, `{"violations": [{"path": "handler.go", "side": "RIGHT", "start_line": 11, "end_line": 11, "explanation": "http.Get ignores ctx."}]}`
		}
	}
	return files, `{"violations": []}`
}

func newRunner(t *testing.T) *Runner {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "cfg-replay")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Snapshot: snap, Scanner: sc, WorkDir: t.TempDir(), Baseline: true}
	tr := &engine.ScriptedTransport{Script: engine.Script{
		Generator: flagHTTPGet,
		Quote:     func(string, int) string { return `resp, err := http.Get(` },
	}}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "t", HTTPClient: &http.Client{Transport: tr}})
	r.Engine = engine.New(h, sc, compiled.Runner{ASTGrep: "ast-grep-not-installed"})
	return r
}

func TestLoadFixtures(t *testing.T) {
	fx, err := LoadFixtures("../../examples/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	if len(fx) < 2 {
		t.Fatalf("got %d fixtures", len(fx))
	}
}

func TestReplayWithoutStore(t *testing.T) {
	fixtures, err := LoadFixtures("../../examples/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t)
	outs, errs, err := r.RunAll(t.Context(), fixtures)
	if err != nil || len(errs) != 0 {
		t.Fatalf("RunAll: %v %v", err, errs)
	}
	byID := map[string]*Outcome{}
	for _, o := range outs {
		byID[o.Fixture] = o
	}
	got := byID["ctx-http-get"]
	if got == nil || got.Comparison.Matched != 1 || got.Comparison.HumansCovered != 1 {
		t.Fatalf("ctx-http-get outcome = %+v", got)
	}
	if got.Verdict != engine.VerdictNoOp {
		t.Fatalf("verdict = %s", got.Verdict)
	}
	if got.Baseline == nil || len(got.Baseline.Findings) != 1 || got.Baseline.Findings[0].State != engine.StateShadow {
		t.Fatalf("baseline = %+v", got.Baseline)
	}
	clean := byID["clean-docs"]
	for _, f := range clean.Findings {
		if f.State == engine.StatePosted {
			t.Fatalf("clean fixture posted %+v", f)
		}
	}
}

func TestReplayPersistsThroughRiverIdempotently(t *testing.T) {
	s := storetest.New(t)
	fixtures, err := LoadFixtures("../../examples/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t)
	r.Store = s

	outs, errs, err := r.RunAll(t.Context(), fixtures)
	if err != nil || len(errs) != 0 || len(outs) != len(fixtures) {
		t.Fatalf("RunAll = %d outcomes, %v, %v", len(outs), errs, err)
	}
	ctx := t.Context()
	var reviews, findings, baselinePosted, decisions int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM reviews").Scan(&reviews)
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM findings").Scan(&findings)
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM findings f JOIN rule_runs r ON r.id = f.rule_run_id JOIN reviews v ON v.id = r.review_id `+
		`WHERE v.caller = 'baseline' AND f.state = 'posted'`).Scan(&baselinePosted)
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM approval_decisions WHERE decision = 'ineligible'").Scan(&decisions)
	if reviews != 2*len(fixtures) {
		t.Fatalf("reviews = %d, want eval + baseline per fixture", reviews)
	}
	if findings == 0 || baselinePosted != 0 || decisions != reviews {
		t.Fatalf("findings=%d baselinePosted=%d decisions=%d", findings, baselinePosted, decisions)
	}

	// Replaying the same PRs again reuses the review rows and findings.
	if _, errs, err := r.RunAll(ctx, fixtures); err != nil || len(errs) != 0 {
		t.Fatalf("second RunAll: %v %v", err, errs)
	}
	var reviews2, findings2 int
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM reviews").Scan(&reviews2)
	_ = s.Pool.QueryRow(ctx, "SELECT count(*) FROM findings").Scan(&findings2)
	if reviews2 != reviews || findings2 != findings {
		t.Fatalf("replay not idempotent: reviews %d->%d findings %d->%d", reviews, reviews2, findings, findings2)
	}
}

type fakeGH struct {
	mu      sync.Mutex
	reviews []map[string]any
	checks  int
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reviews"):
		if err := json.NewEncoder(w).Encode(f.reviews); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reviews"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = len(f.reviews) + 1
		f.reviews = append(f.reviews, body)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case strings.Contains(r.URL.Path, "/check-runs"):
		f.checks++
		fmt.Fprint(w, `{"id": 9}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestReplayPostsToSandboxPR(t *testing.T) {
	fake := &fakeGH{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_test"))
	if err != nil {
		t.Fatal(err)
	}
	fixtures, _ := LoadFixtures("../../examples/fixtures")
	r := newRunner(t)
	r.PosterFor = func(context.Context, string) (*gh.Poster, error) { return gh.NewPoster(client), nil }
	r.Baseline = false
	fx := fixtures[0]
	for _, f := range fixtures {
		if f.ID == "ctx-http-get" {
			fx = f
		}
	}
	// Posting needs a real PR, so replay the synthetic repo as a recorded
	// PR at known commits.
	repo, err := synth.Build(t.Context(), filepath.Join(t.TempDir(), "remote"), fx.Synthetic.Base, fx.Synthetic.Head, nil)
	if err != nil {
		t.Fatal(err)
	}
	fx.Synthetic, fx.CloneURL, fx.BaseSHA, fx.HeadSHA = nil, repo.Dir, repo.BaseSHA, repo.HeadSHA
	out, err := r.Run(t.Context(), fx)
	if err != nil {
		t.Fatal(err)
	}
	if out.Posted == nil || !out.Posted.NewPost || out.Posted.Comments < 1 {
		t.Fatalf("posted = %+v", out.Posted)
	}
	review := fake.reviews[0]
	reviewBody, _ := review["body"].(string)
	if review["event"] != "COMMENT" || !strings.Contains(reviewBody, "exemplar:review") {
		t.Fatalf("review = %v", review)
	}
	comments, ok := review["comments"].([]any)
	if !ok || len(comments) == 0 {
		t.Fatalf("review comments = %v", review["comments"])
	}
	comment, ok := comments[0].(map[string]any)
	if !ok {
		t.Fatalf("comment = %v", comments[0])
	}
	body, ok := comment["body"].(string)
	if !ok {
		t.Fatalf("comment body = %v", comment["body"])
	}
	if !strings.Contains(body, "Rule `propagate-context`") || !strings.Contains(body, "issue (reliability)") {
		t.Fatalf("comment body = %q", body)
	}
	if fake.checks != 2 {
		t.Fatalf("check run calls = %d, want start + complete", fake.checks)
	}
}
