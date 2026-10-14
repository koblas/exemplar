package evalrun

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

const repo = "exemplar-sandbox/playground"

const (
	shaA = "aaaaaaa1111111"
	shaB = "bbbbbbb2222222"
)

// files: bad.go calls http.Get on line 6; ok.go does not.
var files = fakeFiles{
	repo + "@" + shaA + ":app/bad.go": "package app\n\nimport \"net/http\"\n\nfunc Load(url string) error {\n\t_, err := http.Get(url)\n\treturn err\n}\n",
	repo + "@" + shaB + ":app/ok.go":  "package app\n\nfunc Load(c Client) error {\n\treturn c.Do()\n}\n",
	repo + "@" + shaB + ":app/inj.go": "package app\n\nfunc Load(c Client) error {\n\treturn c.Do()\n}\n",
}

type fakeFiles map[string]string

func (f fakeFiles) FileAt(_ context.Context, r, sha, p string) ([]byte, error) {
	s, ok := f[r+"@"+sha+":"+p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(s), nil
}

// grepMatcher "matches" lines containing needle.
type grepMatcher struct{ needle string }

func (g grepMatcher) Run(_ context.Context, dir, _, _ string, paths []string) ([]compiled.Match, error) {
	var out []compiled.Match
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, g.needle) {
				out = append(out, compiled.Match{Path: p, StartLine: i + 1, EndLine: i + 1})
			}
		}
	}
	return out, nil
}

func snapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "eval")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func rule(t *testing.T, snap *config.Snapshot, id string) config.Rule {
	t.Helper()
	for _, r := range snap.Rules {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no rule %s", id)
	return config.Rule{}
}

func cases() []Case {
	// The synthetic case turns inj.go's c.Do() into http.Get on line 4.
	patch := "--- a/app/inj.go\n+++ b/app/inj.go\n@@ -3,3 +3,3 @@\n func Load(c Client) error {\n-\treturn c.Do()\n+\treturn http.Get(url)\n }\n"
	return []Case{
		{Name: "positive", Repo: repo, SHA: shaA, Paths: []string{"app/bad.go"}, Expect: []schema.Expectation{{Path: "app/bad.go", Lines: []int{6, 6}}}},
		{Name: "negative", Repo: repo, SHA: shaB, Paths: []string{"app/ok.go"}, Expect: []schema.Expectation{}},
		{Name: "injected", Repo: repo, SHA: shaB, Synthetic: true, Patch: patch, Paths: []string{"app/inj.go"}, Expect: []schema.Expectation{{Path: "app/inj.go", Lines: []int{4, 4}}}},
		{Name: "missing file", Repo: repo, SHA: shaA, Paths: []string{"app/gone.go"}, Expect: []schema.Expectation{{Path: "app/gone.go", Lines: []int{1, 1}}}},
		{Name: "denylisted", Repo: "acme/denied", SHA: shaA, Paths: []string{"app/bad.go"}, Expect: []schema.Expectation{{Path: "app/bad.go", Lines: []int{6, 6}}}},
	}
}

func eligible(r string) bool { return r != "acme/denied" }

func results(rep Report) string {
	var out []string
	for _, o := range rep.Outcomes {
		out = append(out, o.Case+"="+o.Result)
	}
	return strings.Join(out, ",")
}

func TestCompiledRule(t *testing.T) {
	snap := snapshot(t)
	r := rule(t, snap, "no-raw-sql-concat") // a compiled rule; the fake matcher stands in for ast-grep
	e := &Evaluator{Snapshot: snap, Eligible: eligible, Matcher: grepMatcher{"http.Get"}, Files: files, WorkDir: t.TempDir()}
	rep, err := e.Rule(t.Context(), r, cases())
	if err != nil {
		t.Fatal(err)
	}
	if got := results(rep); got != "positive=tp,negative=tn,injected=tp,missing file=unavailable,denylisted=unavailable" {
		t.Fatalf("outcomes = %s", got)
	}
	if rep.Historical.Cases != 2 || rep.Synthetic.Cases != 1 || rep.All.Precision != 1 || rep.All.Recall != 1 || rep.All.F1 != 1 {
		t.Errorf("metrics = %+v / %+v / %+v", rep.Historical, rep.Synthetic, rep.All)
	}
	Gate(&rep, schema.EvalThresholds{MinCases: 3, Precision: 0.8, Recall: 0.5})
	if !rep.Pass {
		t.Errorf("gate = %v", rep.Reasons)
	}
	Gate(&rep, schema.EvalThresholds{MinCases: 10, Precision: 0.8, Recall: 0.5})
	if rep.Pass || !strings.Contains(strings.Join(rep.Reasons, ";"), "below the minimum 10") {
		t.Errorf("min-cases gate = %v", rep.Reasons)
	}

	// A noisy matcher fails on precision.
	e.Matcher = grepMatcher{"package"}
	rep, _ = e.Rule(t.Context(), r, cases())
	Gate(&rep, schema.EvalThresholds{MinCases: 3, Precision: 0.8, Recall: 0.5})
	if rep.Pass || rep.All.FP != 1 || !strings.Contains(strings.Join(rep.Reasons, ";"), "precision") {
		t.Errorf("noisy rule = %+v %v", rep.All, rep.Reasons)
	}
}

func TestExemplarRule(t *testing.T) {
	snap := snapshot(t)
	r := rule(t, snap, "propagate-context")
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	// The scripted model flags http.Get wherever it sees it in the files.
	tr := &engine.ScriptedTransport{Script: engine.Script{
		Generator: func(task string, fs []string) ([]string, string) {
			for _, f := range fs {
				switch {
				case strings.Contains(task, "deadlines reach them") && f == "app/bad.go":
					return fs, `{"violations": [{"path": "app/bad.go", "side": "RIGHT", "start_line": 6, "end_line": 6, "explanation": "http.Get ignores ctx."}]}`
				case strings.Contains(task, "deadlines reach them") && f == "app/inj.go":
					return fs, `{"violations": [{"path": "app/inj.go", "side": "RIGHT", "start_line": 4, "end_line": 4, "explanation": "http.Get ignores ctx."}]}`
				}
			}
			return fs, `{"violations": []}`
		},
		Quote: func(p string, _ int) string {
			if p == "app/bad.go" {
				return "\t_, err := http.Get(url)"
			}
			return "\treturn http.Get(url)"
		},
	}}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "t", HTTPClient: &http.Client{Transport: tr}})
	e := &Evaluator{Snapshot: snap, Eligible: eligible, Engine: engine.New(h, sc, compiled.Runner{ASTGrep: "ast-grep-not-installed"}),
		Files: files, Scanner: sc, WorkDir: t.TempDir()}
	rep, err := e.Rule(t.Context(), r, cases()[:3])
	if err != nil {
		t.Fatal(err)
	}
	if got := results(rep); got != "positive=tp,negative=tn,injected=tp" {
		t.Fatalf("outcomes = %s", got)
	}
	if len(tr.Calls) == 0 {
		t.Fatal("the exemplar lane never ran")
	}
	for _, c := range tr.Calls {
		if c.Stage == "generator" && !strings.Contains(string(c.Body), "deadlines reach them") {
			t.Error("a rule other than the evaluated one ran")
		}
	}
}

func TestCases(t *testing.T) {
	cfg := fstest.MapFS{"evals/r/patches/i-1.diff": {Data: []byte("--- a/x\n+++ b/x\n")}}
	got, err := Cases(cfg, schema.EvalCases{Rule: "r", Cases: []schema.EvalCase{
		{Name: "p", Ref: "acme/app@" + shaA, Expect: []schema.Expectation{{Path: "a.go", Lines: []int{1, 2}}, {Path: "a.go", Lines: []int{5, 5}}}},
		{Name: "n", Ref: "acme/app@" + shaB, Paths: []string{"b.go"}, Expect: []schema.Expectation{}},
		{Name: "i", Ref: "acme/app@" + shaB, Synthetic: true, Patch: "patches/i-1.diff", Expect: []schema.Expectation{{Path: "x", Lines: []int{1, 1}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got[0].Paths, ",") != "a.go" || strings.Join(got[1].Paths, ",") != "b.go" || got[2].Patch == "" || got[0].Repo != "acme/app" {
		t.Errorf("cases = %+v", got)
	}
	if _, err := Cases(cfg, schema.EvalCases{Rule: "r", Cases: []schema.EvalCase{{Name: "x", Ref: "acme/app@" + shaA, Patch: "../../platform/eligibility.yaml"}}}); err == nil {
		t.Error("a patch path escaping the rule's evals dir was accepted")
	}
}

// A bug class is evaluated like a rule: only its lane runs, on every file.
func TestBugClass(t *testing.T) {
	snap := snapshot(t)
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := &engine.ScriptedTransport{Script: engine.Script{
		Generator: func(task string, fs []string) ([]string, string) {
			if strings.Contains(task, "<bug_class>") && slices.Contains(fs, "app/bad.go") {
				return fs, `{"violations": [{"path": "app/bad.go", "side": "RIGHT", "start_line": 6, "end_line": 6,
					"explanation": "err is dropped.", "trigger": "the server is down", "impact": "the caller sees success"}]}`
			}
			return fs, `{"violations": []}`
		},
		Quote: func(string, int) string { return "\t_, err := http.Get(url)" },
	}}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "t", HTTPClient: &http.Client{Transport: tr}})
	e := &Evaluator{Snapshot: snap, Eligible: eligible, Engine: engine.New(h, sc, compiled.Runner{ASTGrep: "ast-grep-not-installed"}),
		Files: files, Scanner: sc, WorkDir: t.TempDir()}
	class := config.Rule{Rule: schema.Rule{ID: "bug.error-handling", Kind: schema.KindBugClass, Status: schema.StatusActive, FilePatterns: []string{"**"}}}
	rep, err := e.Rule(t.Context(), class, cases()[:2])
	if err != nil {
		t.Fatal(err)
	}
	if got := results(rep); got != "positive=tp,negative=tn" {
		t.Fatalf("outcomes = %s", got)
	}
	for _, c := range tr.Calls {
		// The body is JSON, so the tag arrives escaped.
		if c.Stage == "generator" && !strings.Contains(string(c.Body), `\u003cbug_class\u003e`) {
			t.Errorf("a rule ran besides the bug class: %.300s", c.Body)
		}
	}
}
