package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/llm"
)

func lightCalls(tr *ScriptedTransport) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, c := range tr.Calls {
		if c.Stage == llm.StageLight {
			n++
		}
	}
	return n
}

func TestReviewLinterDedupe(t *testing.T) {
	t.Parallel()
	noctx := LinterAnnotation{Check: "golangci-lint", Path: "main.go", Start: httpGetLine, End: httpGetLine, Title: "noctx", Message: "should rewrite http.NewRequestWithContext"}
	secret := LinterAnnotation{Check: "golangci-lint", Path: "config.go", Start: 3, End: 3, Title: "gosec", Message: "G101: potential hardcoded credentials"}
	tests := []struct {
		name      string
		linters   []string
		mapIDs    map[string][]string
		anns      []LinterAnnotation
		loadErr   error
		judge     bool
		wantDup   bool // the propagate-context finding is a linter duplicate
		wantLight int
		wantTrace bool
	}{
		{name: "mapped title", linters: []string{"golangci-lint"}, mapIDs: map[string][]string{"GolangCI-Lint": {"noctx"}}, anns: []LinterAnnotation{noctx}, wantDup: true, wantTrace: true},
		{
			name: "mapped message suffix", linters: []string{"golangci-lint"}, mapIDs: map[string][]string{"golangci-lint": {"noctx"}},
			anns: []LinterAnnotation{{Check: "golangci-lint", Path: "main.go", Start: 8, End: 10, Message: "net/http.Get must not be called (noctx)"}}, wantDup: true, wantTrace: true,
		},
		{name: "map for another linter", linters: []string{"golangci-lint"}, mapIDs: map[string][]string{"staticcheck": {"noctx"}}, anns: []LinterAnnotation{noctx}, wantLight: 1, wantTrace: true},
		{name: "judged same", linters: []string{"golangci-lint"}, anns: []LinterAnnotation{noctx}, judge: true, wantDup: true, wantLight: 1, wantTrace: true},
		{name: "judged different", linters: []string{"golangci-lint"}, anns: []LinterAnnotation{noctx}, wantLight: 1, wantTrace: true},
		{name: "other lines", linters: []string{"golangci-lint"}, anns: []LinterAnnotation{{Check: "golangci-lint", Path: "main.go", Start: 2, Title: "noctx"}}, judge: true, wantTrace: true},
		{name: "other file", linters: []string{"golangci-lint"}, anns: []LinterAnnotation{{Check: "golangci-lint", Path: "other.go", Start: httpGetLine, End: httpGetLine}}, judge: true, wantTrace: true},
		{name: "load error", linters: []string{"golangci-lint"}, loadErr: errors.New("check runs unavailable"), judge: true, wantTrace: true},
		{name: "no linters configured", anns: []LinterAnnotation{noctx}, judge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := setup(t, "Fetch inventory", "Adds a fetch.")
			e.prep.Effective.RepoConfig.Linters = tt.linters
			for i := range e.prep.Effective.Rules {
				if e.prep.Effective.Rules[i].ID == "propagate-context" {
					e.prep.Effective.Rules[i].LinterMap = tt.mapIDs
				}
			}
			eng, tr := newEngine(e, Script{
				Generator: contextViolation(""), Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) },
				LinterJudge: func(pair string) bool {
					if !strings.Contains(pair, "<untrusted_linter_context>") || !strings.Contains(pair, "http.Get ignores ctx") {
						t.Errorf("judge context = %q", pair)
					}
					return tt.judge
				},
			})
			loads := 0
			// The secret-in-diff finding always overlaps an annotation, and
			// the judge would call it the same: it posts regardless.
			anns := append([]LinterAnnotation{secret}, tt.anns...)
			res, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval", Linters: func(context.Context) ([]LinterAnnotation, error) {
				loads++
				return anns, tt.loadErr
			}})
			if err != nil {
				t.Fatal(err)
			}
			fs := findByRule(res.Findings, "propagate-context")
			if len(fs) != 1 {
				t.Fatalf("findings = %+v", res.Findings)
			}
			if dup := fs[0].State == StateDropped && fs[0].Suppressed == SuppressLinterDuplicate; dup != tt.wantDup {
				t.Errorf("finding = %s/%s, want duplicate %v", fs[0].State, fs[0].Suppressed, tt.wantDup)
			} else if !dup && fs[0].State != StatePosted {
				t.Errorf("finding = %s/%s, want posted", fs[0].State, fs[0].Suppressed)
			}
			if s := findByRule(res.Findings, SecretRuleID); len(s) != 1 || s[0].State != StatePosted {
				t.Errorf("secret finding = %+v", s)
			}
			if n := lightCalls(tr); n != tt.wantLight {
				t.Errorf("judge calls = %d, want %d", n, tt.wantLight)
			}
			if (res.Linters != nil) != tt.wantTrace || loads > 1 || (tt.wantTrace && loads != 1) {
				t.Errorf("trace = %+v, loads = %d", res.Linters, loads)
			}
			if tt.loadErr != nil && (res.Linters == nil || res.Linters.Error == "") {
				t.Errorf("trace = %+v, want the load error", res.Linters)
			}
			// Duplicates still count as candidates for auto-approve.
			if res.Stage1.Input.Candidates != 2 {
				t.Errorf("candidates = %d", res.Stage1.Input.Candidates)
			}
		})
	}
}

func TestLinterJudgeCached(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	e.prep.Effective.RepoConfig.Linters = []string{"golangci-lint"}
	eng, tr := newEngine(e, Script{
		Generator: contextViolation(""), Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) },
		LinterJudge: func(string) bool { return true },
	})
	opt := Options{Caller: "eval", Linters: func(context.Context) ([]LinterAnnotation, error) {
		return []LinterAnnotation{{Check: "golangci-lint", Path: "main.go", Start: httpGetLine, End: httpGetLine, Message: "use a context"}}, nil
	}}
	for range 2 {
		res, err := eng.Review(t.Context(), e.prep, opt)
		if err != nil {
			t.Fatal(err)
		}
		if fs := findByRule(res.Findings, "propagate-context"); len(fs) != 1 || fs[0].Suppressed != SuppressLinterDuplicate {
			t.Fatalf("findings = %+v", fs)
		}
	}
	if n := lightCalls(tr); n != 1 {
		t.Errorf("judge calls = %d, want 1 (the second review reads the cache)", n)
	}
}

func TestLinterDedupeNotLoadedWithoutFindings(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	e.prep.Effective.RepoConfig.Linters = []string{"golangci-lint"}
	e.prep.Secrets = nil
	eng, _ := newEngine(e, Script{Generator: func(_ string, files []string) ([]string, string) { return files, `{"violations": []}` }})
	res, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval", Linters: func(context.Context) ([]LinterAnnotation, error) {
		t.Error("waited for linters with no finding to post")
		return nil, nil
	}})
	if err != nil || res.Linters != nil {
		t.Fatalf("res = %+v, %v", res.Linters, err)
	}
}

func TestLinterDuplicatesLate(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	rules := e.prep.Effective.Rules
	for i := range rules {
		if rules[i].ID == "propagate-context" {
			rules[i].LinterMap = map[string][]string{"golangci-lint": {"noctx"}}
		}
	}
	eng, tr := newEngine(e, Script{LinterJudge: func(string) bool { return true }})
	// Stored findings carry no category: it comes from the rule.
	fs := []Finding{
		{RuleID: "propagate-context", Lane: LaneExemplar, Severity: "important", Anchor: anchorAt("main.go", 9), Message: "http.Get ignores ctx."},
		{RuleID: "no-raw-sql-concat", Lane: LaneCompiled, Severity: "important", Anchor: anchorAt("db.go", 4), Message: "Use placeholders."},
		{RuleID: "propagate-context", Lane: LaneExemplar, Severity: "important", Anchor: anchorAt("other.go", 2), Message: "http.Get ignores ctx."},
		{RuleID: "propagate-context", Lane: LaneExemplar, Severity: "important", Anchor: anchorAt("main.go", 30), Message: "Pass ctx down."},
	}
	anns := []LinterAnnotation{
		{Check: "golangci-lint", Path: "main.go", Start: 9, End: 9, Title: "noctx", Message: "use a context"},
		{Check: "golangci-lint", Path: "db.go", Start: 4, End: 4, Title: "gosec", Message: "SQL string concatenation"},
		{Check: "golangci-lint", Path: "main.go", Start: 30, End: 31, Title: "contextcheck", Message: "function should pass the context"},
	}
	dups, trace := eng.LinterDuplicates(t.Context(), CallMeta{Caller: "production", Repo: sandboxRepo}, rules, fs, anns)
	// 0 is mapped; 1 is a Security rule, never judged; 2 overlaps nothing;
	// 3 is judged the same.
	if len(dups) != 2 || dups[0] != "golangci-lint" || dups[3] != "golangci-lint" {
		t.Errorf("duplicates = %v", dups)
	}
	if n := lightCalls(tr); n != 1 || trace.Judged != 1 || trace.Duplicates != 2 {
		t.Errorf("judge calls = %d, trace = %+v", n, trace)
	}
}
