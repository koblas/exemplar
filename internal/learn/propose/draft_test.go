package propose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

var now = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func sampleCandidate() Candidate {
	return Candidate{
		ID: 42, Ext: ".go", Title: "Set timeouts on outbound HTTP calls!",
		Description: "Every outbound HTTP call needs a timeout.\nA slow dependency must not hang the service.",
		ClusterSize: 5, DistinctReviewers: 3, Score: 3.1, Owner: "@acme/payments",
		ScoreDetail: map[string]any{"factors": map[string]any{"recency": 0.9, "doc_boost": 1.5}},
		Sources: []Source{
			{CommentURL: "https://github.com/acme/pay/pull/1#discussion_r1", Repo: "acme/pay", Path: "svc/client.go",
				DocURLs: []string{"https://docs.internal/http"}, Kind: "human", Category: "reliability", PreferredApproach: "Use http.Client{Timeout: ...}."},
			{CommentURL: "https://github.com/acme/pay/pull/2#discussion_r2", Repo: "acme/pay", Path: "svc/fetch.go",
				Kind: "accepted_bot_finding", Category: "Reliability practice"},
			{CommentURL: "https://github.com/acme/pay/pull/3#discussion_r3", Repo: "acme/pay", Path: "svc/x.go", Kind: "human", Category: "Performance"},
		},
		Incidents: []Incident{{Repo: "acme/pay", Path: "svc/client.go", Kind: "incident", Ref: "INC-7"}},
	}
}

func TestBuildDraft(t *testing.T) {
	d, err := BuildDraft(sampleCandidate(), now)
	if err != nil {
		t.Fatalf("BuildDraft: %v", err)
	}
	if d.RuleID != "set-timeouts-on-outbound-http-calls-c42" || d.Path != "rules/reliability/"+d.RuleID+".yaml" || d.Branch != "exemplar/candidate-42" {
		t.Fatalf("draft = %+v", d)
	}
	var r schema.Rule
	if err := schema.Decode(schema.DocRule, d.Rule, &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "shadow" || r.Severity != "suggestion" || r.Kind != "exemplar" || r.Owner != "@acme/payments" ||
		r.ReviewBy != "2027-10-12" || r.FilePatterns[0] != "**/*.go" || r.Category != "Reliability" {
		t.Fatalf("rule = %+v", r)
	}
	if r.Citation == nil || r.Citation.Doc != "https://docs.internal/http" || len(r.Citation.Comments) != 3 {
		t.Fatalf("citation = %+v", r.Citation)
	}
	if !strings.Contains(r.PreferredApproach, "http.Client") {
		t.Fatalf("preferred approach = %q", r.PreferredApproach)
	}
	for _, want := range []string{"shadow", "INC-7", "(accepted bot finding)", "https://docs.internal/http", "evals/" + d.RuleID, "killed"} {
		if !strings.Contains(d.Body, want) {
			t.Errorf("PR body lacks %q", want)
		}
	}
}

// TestDraftLoadsIntoConfig adds the drafted rule to the example config tree
// and loads it with the same validator CI runs (`exemplar validate`).
func TestDraftLoadsIntoConfig(t *testing.T) {
	d, err := BuildDraft(sampleCandidate(), now)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	root := "../../../examples/exemplar-config"
	err = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		fsys[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	fsys[d.Path] = &fstest.MapFile{Data: d.Rule}
	snap, err := config.LoadSnapshot(fsys, "test")
	if err != nil {
		t.Fatalf("config with drafted rule does not load: %v", err)
	}
	found := false
	for _, r := range snap.Rules {
		found = found || r.ID == d.RuleID
	}
	if !found {
		t.Fatal("drafted rule not loaded")
	}
}

func TestDraftDefaultsAndRefusals(t *testing.T) {
	c := sampleCandidate()
	c.Ext, c.Owner = "*", "dev@example.com" // agnostic, and an email CODEOWNERS entry
	c.Sources = []Source{{CommentURL: "https://github.com/a/b/pull/1#r1", Repo: "a/b", Path: "x.py"}}
	d, err := BuildDraft(c, now)
	if err != nil {
		t.Fatal(err)
	}
	var r schema.Rule
	if err := schema.Decode(schema.DocRule, d.Rule, &r); err != nil {
		t.Fatal(err)
	}
	if r.FilePatterns[0] != "**/*" || r.Owner != fallbackOwner || r.Category != fallbackCategory || r.Citation.Doc != "" {
		t.Fatalf("defaults = %+v", r)
	}
	c.Title = "  "
	if _, err := BuildDraft(c, now); err == nil {
		t.Fatal("candidate without a title drafted")
	}
}

func TestRuleID(t *testing.T) {
	tests := []struct {
		title string
		id    int64
		want  string
	}{
		{"Never log request bodies", 7, "never-log-request-bodies-c7"},
		{"  Use `context.Context`, always!  ", 8, "use-context-context-always-c8"},
		{"???", 9, "rule-c9"},
		{"Ünïcode títle", 10, "n-code-t-tle-c10"},
	}
	for _, tt := range tests {
		if got := RuleID(tt.title, tt.id); got != tt.want {
			t.Errorf("RuleID(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
	long := RuleID(strings.Repeat("timeout ", 30), 123456)
	if len(long) > 80 || !strings.HasSuffix(long, "-c123456") || strings.Contains(long, "--") {
		t.Fatalf("long id = %q (%d)", long, len(long))
	}
}

// TestDraftWithEvalCases ships generated cases with the rule and loads the
// result with the config validator, including the eval cross-checks.
func TestDraftWithEvalCases(t *testing.T) {
	c := sampleCandidate()
	sha := "0123456789abcdef0123456789abcdef01234567"
	c.EvalCases = []EvalCase{
		{ID: "p-1", Kind: "positive", Name: "review comment", Ref: "acme/pay@" + sha, Expect: []schema.Expectation{{Path: "svc/client.go", Lines: []int{6, 9}}}},
		{ID: "n-acme-pay-20", Kind: "negative", Name: "approved PR", Ref: "acme/pay@" + sha, Expect: []schema.Expectation{}},
		{ID: "i-acme-pay-20", Kind: "injected", Name: "injected", Ref: "acme/pay@" + sha, Patch: "diff --git a/x b/x\n", SpotCheck: true,
			Expect: []schema.Expectation{{Path: "svc/client.go", Lines: []int{6, 6}}}},
	}
	d, err := BuildDraft(c, now)
	if err != nil {
		t.Fatal(err)
	}
	dir := "evals/" + d.RuleID + "/"
	if _, ok := d.Files[dir+"cases.yaml"]; !ok {
		t.Fatalf("files = %v", keys(d.Files))
	}
	if string(d.Files[dir+"patches/i-acme-pay-20.diff"]) != "diff --git a/x b/x\n" {
		t.Fatal("patch file missing")
	}
	if !strings.Contains(string(d.Rule), "eval_cases: evals/"+d.RuleID+"/") {
		t.Fatalf("rule does not point at its cases:\n%s", d.Rule)
	}
	if !strings.Contains(d.Body, "1 from source comments") || !strings.Contains(d.Body, "`i-acme-pay-20`") {
		t.Fatalf("body does not describe the cases:\n%s", d.Body)
	}

	fsys := exampleTree(t)
	fsys[d.Path] = &fstest.MapFile{Data: d.Rule}
	for p, data := range d.Files {
		fsys[p] = &fstest.MapFile{Data: data}
	}
	snap, err := config.LoadSnapshot(fsys, "test")
	if err != nil {
		t.Fatalf("config with rule and eval cases does not load: %v", err)
	}
	if len(snap.Evals[d.RuleID].Cases) != 3 {
		t.Fatalf("loaded cases = %+v", snap.Evals[d.RuleID])
	}
}

func exampleTree(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	root := "../../../examples/exemplar-config"
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		fsys[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBuildDraftCompiled(t *testing.T) {
	c := sampleCandidate()
	c.Compiled = &Compiled{
		AttemptID: 7, Engine: "ast-grep", Rule: "id: set-timeouts-on-outbound-http-calls-c42\nlanguage: go\nrule:\n  pattern: http.Get($URL)\nmessage: Set timeouts\nseverity: warning\n",
		Precision: 0.92, Recall: 0.8, Cases: 14, Baseline: "thresholds", BaselinePrecision: 0.7, BaselineRecall: 0.6,
	}
	d, err := BuildDraft(c, now)
	if err != nil {
		t.Fatalf("BuildDraft: %v", err)
	}
	if !strings.Contains(string(d.Rule), "kind: compiled") || !strings.Contains(string(d.Rule), "engine: ast-grep") || !strings.Contains(string(d.Rule), "pattern: http.Get($URL)") {
		t.Fatalf("compiled rule:\n%s", d.Rule)
	}
	for _, want := range []string{"### Compiled to ast-grep", "| Precision | 0.92 | not measured yet | ≥ 0.70 |", "| Recall | 0.80 | not measured yet | ≥ 0.60 |", "Compile attempt 7"} {
		if !strings.Contains(d.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, d.Body)
		}
	}
	// The drafted file loads as a valid compiled rule in exemplar-config.
	fsys := exampleTree(t)
	fsys[d.Path] = &fstest.MapFile{Data: d.Rule}
	snap, err := config.LoadSnapshot(fsys, "test")
	if err != nil {
		t.Fatalf("drafted compiled rule does not load: %v", err)
	}
	for _, r := range snap.Rules {
		if r.ID == d.RuleID && (r.Kind != schema.KindCompiled || r.Matcher == nil || r.Matcher.Engine != "ast-grep") {
			t.Errorf("loaded rule = %+v", r.Rule)
		}
	}
}
