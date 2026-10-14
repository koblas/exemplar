package compile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

var goLang, _ = LanguageFor(".go")

const goodASTGrep = `id: whatever
language: Go
rule:
  pattern: http.Get($URL)
message: model text
severity: error
`

func TestNormalize(t *testing.T) {
	body, err := Normalize(EngineASTGrep, goodASTGrep, "set-timeouts-c7", "Set timeouts on outbound calls", goLang)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["id"] != "set-timeouts-c7" || doc["language"] != "go" || doc["message"] != "Set timeouts on outbound calls" || doc["severity"] != "warning" {
		t.Errorf("normalized = %v", doc)
	}

	semgrep := "rules:\n  - id: x\n    languages: [go]\n    message: m\n    severity: ERROR\n    mode: taint\n    pattern-sources: [{pattern: os.Getenv(...)}]\n    pattern-sinks: [{pattern: exec.Command(...)}]\n"
	if body, err := Normalize(EngineSemgrep, semgrep, "r-c1", "No env in exec", goLang); err != nil || !strings.Contains(body, "r-c1") || !strings.Contains(body, "WARNING") {
		t.Errorf("semgrep = %q, %v", body, err)
	}

	for name, tc := range map[string]struct{ engine, body string }{
		"wrong language":     {EngineASTGrep, strings.Replace(goodASTGrep, "Go", "python", 1)},
		"no rule":            {EngineASTGrep, "id: x\nlanguage: go\n"},
		"fix":                {EngineASTGrep, goodASTGrep + "fix: http.Get($URL, timeout)\n"},
		"nested fix":         {EngineASTGrep, "id: x\nlanguage: go\nrule:\n  any:\n    - pattern: a\n      fix: b\n"},
		"unknown key":        {EngineASTGrep, goodASTGrep + "files: ['**/*.go']\n"},
		"two documents":      {EngineASTGrep, goodASTGrep + "---\n" + goodASTGrep},
		"not yaml":           {EngineASTGrep, "id: [unclosed"},
		"too big":            {EngineASTGrep, goodASTGrep + "note: " + strings.Repeat("x", maxRuleBytes) + "\n"},
		"semgrep two rules":  {EngineSemgrep, "rules:\n  - {id: a, languages: [go], message: m, severity: INFO, pattern: x}\n  - {id: b, languages: [go], message: m, severity: INFO, pattern: y}\n"},
		"semgrep paths":      {EngineSemgrep, "rules:\n  - {id: a, languages: [go], message: m, severity: INFO, pattern: x, paths: {include: ['*']}}\n"},
		"semgrep python":     {EngineSemgrep, "rules:\n  - {id: a, languages: [go], message: m, severity: INFO, pattern: x, pattern-where-python: 'True'}\n"},
		"semgrep no pattern": {EngineSemgrep, "rules:\n  - {id: a, languages: [go], message: m, severity: INFO}\n"},
		"semgrep two langs":  {EngineSemgrep, "rules:\n  - {id: a, languages: [go, python], message: m, severity: INFO, pattern: x}\n"},
		"unknown engine":     {"comby", goodASTGrep},
	} {
		if _, err := Normalize(tc.engine, tc.body, "r", "m", goLang); !errors.Is(err, ErrInvalidRule) {
			t.Errorf("%s: err = %v, want ErrInvalidRule", name, err)
		}
	}
	if _, ok := LanguageFor("*"); ok {
		t.Error("language-agnostic candidates must not compile")
	}
}

// fakeEngine "matches" every line containing needle in the given files.
type fakeEngine struct {
	needle      string
	unavailable bool
	invalid     bool
	runs        int
}

func (f *fakeEngine) Validate(context.Context, string, string) error {
	switch {
	case f.unavailable:
		return compiled.ErrEngineUnavailable
	case f.invalid:
		return errors.New("bad rule")
	}
	return nil
}

func (f *fakeEngine) Run(_ context.Context, dir, _, _ string, files []string) ([]compiled.Match, error) {
	f.runs++
	var out []compiled.Match
	for _, p := range files {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, f.needle) {
				out = append(out, compiled.Match{Path: p, StartLine: i + 1, EndLine: i + 1})
			}
		}
	}
	return out, nil
}

type fakeFiles map[string]string // "repo@sha:path" -> content

func (f fakeFiles) FileAt(_ context.Context, repo, sha, p string) ([]byte, error) {
	s, ok := f[repo+"@"+sha+":"+p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(s), nil
}

type fakeModel struct {
	p     Proposal
	err   error
	calls int
	saw   []Request
}

func (m *fakeModel) Propose(_ context.Context, r Request) (Proposal, error) {
	m.calls++
	m.saw = append(m.saw, r)
	return m.p, m.err
}

// fakeToken is assembled at run time so no literal credential is in source.
func fakeToken() string {
	sum := sha256.Sum256([]byte("exemplar compile test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

const (
	shaP = "aaaaaaa1111111"
	shaN = "bbbbbbb2222222"
)

var files = fakeFiles{
	"acme/pay@" + shaP + ":svc/a.go":   "package svc\n\nfunc f() {\n\thttp.Get(url)\n}\n",
	"acme/pay@" + shaP + ":svc/b.go":   "package svc\n\nfunc g() {\n\tclient.Do(req)\n}\n",
	"acme/pay@" + shaN + ":svc/ok.go":  "package svc\n\nfunc h() {\n\tclient.Do(req)\n}\n",
	"acme/pay@" + shaN + ":svc/bad.go": "package svc\n\n// careful\nfunc k() {\n\tclient.Do(req)\n}\n",
}

func exec(t *testing.T, s *store.Store, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// seed creates candidate 1 (.go) with two source comments and eval cases:
// two positives, one negative, one injected; and candidate 2 ("*").
func seed(t *testing.T, s *store.Store) {
	t.Helper()
	exec(t, s, `INSERT INTO cluster_runs (id, params, status) VALUES (1, '{}', 'done')`)
	for i, hunk := range []string{"@@ -1,3 +1,4 @@\n+\thttp.Get(url)", "@@ -1 +1 @@\n+\ttoken := \"" + fakeToken() + "\""} {
		exec(t, s, `INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url, body, diff_hunk)
		            VALUES ($1, 'acme/pay', 1, 'svc/a.go', '.go', 'r', 'a', now(), $3, 'b', $2)`, i+1, hunk, fmt.Sprintf("https://x/%d", i+1))
		exec(t, s, `INSERT INTO extractions (comment_id, title, description, preferred_approach, category, confidence, extractor_model, extractor_version)
		            VALUES ($1, 't', 'd', 'Use a client with a timeout.', 'Reliability', 0.9, 'm', 'v')`, i+1)
	}
	for id, ext := range map[int]string{1: ".go", 2: "*"} {
		exec(t, s, `INSERT INTO candidates (id, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
		            VALUES ($1, $2, 'Set timeouts on outbound HTTP calls', 'Every outbound call needs a timeout.', 2, 2, 10, '{}', '@svc', $3, 1)`, id, ext, fmt.Sprintf("h%d", id))
		exec(t, s, `INSERT INTO candidate_sources VALUES ($1, 1), ($1, 2)`, id)
		exec(t, s, `INSERT INTO candidate_eval_cases (candidate_id, case_id, kind, name, ref, expect) VALUES ($1, 'p-1', 'positive', 'p', 'acme/pay@`+shaP+`', '[{"path":"svc/a.go","lines":[4,4]}]')`, id)
	}
	exec(t, s, `INSERT INTO candidate_eval_cases (candidate_id, case_id, kind, name, ref, expect) VALUES
		(1, 'p-2', 'positive', 'p2', 'acme/pay@`+shaP+`', '[{"path":"svc/b.go","lines":[4,4]}]'),
		(1, 'n-1', 'negative', 'n', 'acme/pay@`+shaN+`', '[]'),
		(1, 'i-1', 'injected', 'i', 'acme/pay@`+shaN+`', '[{"path":"svc/bad.go","lines":[5,5]}]')`)
	patch := "--- a/svc/bad.go\n+++ b/svc/bad.go\n@@ -2,5 +2,5 @@\n \n // careful\n func k() {\n-\tclient.Do(req)\n+\thttp.Get(url)\n }\n"
	exec(t, s, `UPDATE candidate_eval_cases SET patch = $1 WHERE case_id = 'i-1'`, patch)
	exec(t, s, `INSERT INTO mined_prs (repo, pr_number, head_sha, base_sha, merged_at, approved_by_codeowner, files)
	            VALUES ('acme/pay', 9, '`+shaN+`', 'c', now(), true, ARRAY['svc/ok.go', 'README.md'])`)
}

func newJob(t *testing.T, s *store.Store, m Model, e Engine) *Job {
	t.Helper()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	j, err := New(Config{Thresholds: Thresholds{MinCases: 3, Precision: 0.7, Recall: 0.6}, Scratch: t.TempDir(), Sandbox: "test-sandbox", Eligible: func(r string) bool { return r != "acme/denied" }}, s, m, e, files, sc)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

type attempt struct {
	status, reason, baseline, sandbox string
	cases, tp, fp, fn                 int
	precision, recall                 *float64
	rule                              *string
	metrics                           []byte
}

func attempts(t *testing.T, s *store.Store) []attempt {
	t.Helper()
	rows, err := s.Pool.Query(t.Context(), `SELECT status, reason, baseline, sandbox, cases, true_pos, false_pos, false_neg, precision, recall, rule_body, metrics
		FROM compile_attempts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []attempt
	for rows.Next() {
		var a attempt
		var p, r *float32
		if err := rows.Scan(&a.status, &a.reason, &a.baseline, &a.sandbox, &a.cases, &a.tp, &a.fp, &a.fn, &p, &r, &a.rule, &a.metrics); err != nil {
			t.Fatal(err)
		}
		if p != nil {
			v := float64(*p)
			a.precision = &v
		}
		if r != nil {
			v := float64(*r)
			a.recall = &v
		}
		out = append(out, a)
	}
	return out
}

func TestCompileAccepted(t *testing.T) {
	s := storetest.New(t)
	seed(t, s)
	m := &fakeModel{p: Proposal{Engine: EngineASTGrep, Rule: goodASTGrep, Explanation: "matches http.Get", Model: "claude-opus-5-5"}}
	e := &fakeEngine{needle: "http.Get"}
	rep, err := newJob(t, s, m, e).Run(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Attempted != 1 || rep.Skipped != 1 || rep.ByStatus[StatusAccepted] != 1 {
		t.Fatalf("report = %+v", rep)
	}
	// The model saw redacted hunks and the preferred approach.
	r := m.saw[0]
	if len(r.Examples) != 2 || strings.Contains(r.Examples[1].Hunk, fakeToken()) || r.PreferredApproach == "" || r.Language.ASTGrep != "go" {
		t.Errorf("request = %+v", r)
	}
	as := attempts(t, s)
	if len(as) != 1 {
		t.Fatalf("attempts = %+v", as)
	}
	a := as[0]
	// p-1 tp (http.Get on line 4), p-2 fn (client.Do), n-1 tn, i-1 tp (patched in).
	if a.status != StatusAccepted || a.cases != 4 || a.tp != 2 || a.fn != 1 || a.fp != 0 || *a.precision != 1 || a.baseline != "thresholds" || a.sandbox != "test-sandbox" {
		t.Errorf("attempt = %+v (recall %v)", a, *a.recall)
	}
	if a.rule == nil || !strings.Contains(*a.rule, "set-timeouts-on-outbound-http-calls-c1") {
		t.Errorf("rule body = %v", a.rule)
	}
	var metrics struct {
		Outcomes []Outcome
	}
	_ = json.Unmarshal(a.metrics, &metrics)
	if len(metrics.Outcomes) != 4 {
		t.Errorf("outcomes = %+v", metrics.Outcomes)
	}
	// LatestAcceptedCompile finds it for the drafter.
	if cc, err := s.LatestAcceptedCompile(t.Context(), 1); err != nil || *cc.Engine != EngineASTGrep {
		t.Errorf("latest accepted = %+v, %v", cc, err)
	}

	// A scored attempt in the last 90 days means no new attempt.
	if rep, err := newJob(t, s, m, e).Run(t.Context(), 10); err != nil || rep.Attempted != 0 || m.calls != 1 {
		t.Errorf("rerun = %+v, %v, model calls %d", rep, err, m.calls)
	}
}

func TestCompileRejectedAndInvalid(t *testing.T) {
	tests := []struct {
		name   string
		model  *fakeModel
		engine *fakeEngine
		want   string
		reason string
	}{
		{"noisy rule", &fakeModel{p: Proposal{Engine: EngineASTGrep, Rule: goodASTGrep}}, &fakeEngine{needle: "client.Do"}, StatusRejected, "precision"},
		{"model declines", &fakeModel{p: Proposal{Engine: "none", Explanation: "depends on intent"}}, &fakeEngine{}, StatusRejected, "no deterministic rule"},
		{"bad yaml", &fakeModel{p: Proposal{Engine: EngineASTGrep, Rule: goodASTGrep + "fix: x\n"}}, &fakeEngine{}, StatusInvalid, "fix"},
		{"engine rejects", &fakeModel{p: Proposal{Engine: EngineASTGrep, Rule: goodASTGrep}}, &fakeEngine{invalid: true}, StatusInvalid, "engine validation"},
		{"model error", &fakeModel{err: errors.New("gateway down")}, &fakeEngine{}, StatusError, "gateway down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := storetest.New(t)
			seed(t, s)
			if _, err := newJob(t, s, tt.model, tt.engine).Run(t.Context(), 10); err != nil {
				t.Fatal(err)
			}
			as := attempts(t, s)
			if len(as) != 1 || as[0].status != tt.want || !strings.Contains(as[0].reason, tt.reason) {
				t.Fatalf("attempts = %+v", as)
			}
		})
	}
}

func TestCompileRetryLimitAndMissingEngine(t *testing.T) {
	s := storetest.New(t)
	seed(t, s)
	m := &fakeModel{err: errors.New("flaky")}
	for range 5 {
		if _, err := newJob(t, s, m, &fakeEngine{}).Run(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(attempts(t, s)); n != 3 {
		t.Errorf("attempts = %d, want 3 before giving up for the window", n)
	}

	s2 := storetest.New(t)
	seed(t, s2)
	_, err := newJob(t, s2, &fakeModel{p: Proposal{Engine: EngineASTGrep, Rule: goodASTGrep}}, &fakeEngine{unavailable: true}).Run(t.Context(), 10)
	if !errors.Is(err, compiled.ErrEngineUnavailable) || len(attempts(t, s2)) != 0 {
		t.Errorf("missing engine: err = %v, attempts %d (want an error and nothing recorded)", err, len(attempts(t, s2)))
	}
}

func TestEvaluateUnavailableCase(t *testing.T) {
	cases := []Case{
		{ID: "p", Kind: "positive", Repo: "acme/pay", SHA: "deadbeef", Files: []string{"svc/gone.go"}, Expect: []Expect{{"svc/gone.go", [2]int{1, 1}}}},
		{ID: "x", Kind: "positive", Repo: "acme/pay", SHA: shaP, Files: []string{"../../etc/passwd"}},
	}
	s, err := Evaluate(t.Context(), &fakeEngine{needle: "x"}, files, t.TempDir(), EngineASTGrep, "", cases)
	if err != nil || s.Cases != 0 || len(s.Outcomes) != 2 || s.Outcomes[0].Result != "unavailable" || s.Outcomes[1].Result != "unavailable" {
		t.Fatalf("Evaluate = %+v, %v", s, err)
	}
	if fmt.Sprint(s.Precision(), s.Recall()) != "0 0" {
		t.Error("empty score must be 0/0")
	}
}

func TestCompileSkipsIneligibleRepos(t *testing.T) {
	s := storetest.New(t)
	seed(t, s)
	exec(t, s, `UPDATE candidate_eval_cases SET ref = replace(ref, 'acme/pay', 'acme/denied') WHERE candidate_id = 1`)
	if _, err := newJob(t, s, &fakeModel{p: Proposal{Engine: EngineASTGrep, Rule: goodASTGrep}}, &fakeEngine{needle: "http.Get"}).Run(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	as := attempts(t, s)
	if len(as) != 1 || as[0].cases != 0 || as[0].status != StatusRejected {
		t.Errorf("attempts = %+v; cases in a denylisted repo must not be read", as)
	}
	var metrics struct{ Outcomes []Outcome }
	_ = json.Unmarshal(as[0].metrics, &metrics)
	if len(metrics.Outcomes) != 0 {
		t.Errorf("outcomes = %+v; denylisted cases were attempted", metrics.Outcomes)
	}
}
