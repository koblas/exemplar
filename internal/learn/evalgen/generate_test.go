package evalgen

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

func TestMain(m *testing.M) { storetest.Main(m) }

func run(t *testing.T, s *store.Store, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func allEligible(string) bool { return true }

func newGenerator(t *testing.T, cfg Config, s *store.Store, inj *Injector) *Generator {
	t.Helper()
	if cfg.Eligible == nil {
		cfg.Eligible = allEligible
	}
	g, err := New(cfg, s, inj)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestNewRequiresEligibility(t *testing.T) {
	if _, err := New(Config{}, nil, nil); err == nil {
		t.Fatal("New without an eligibility check succeeded")
	}
}

// A repo denylisted or dropped after it was mined is never read for cases
// nor sent to the model.
func TestGeneratorSkipsIneligibleRepos(t *testing.T) {
	s := storetest.New(t)
	run(t, s, `INSERT INTO cluster_runs (id, params, status) VALUES (1, '{}', 'done')`)
	run(t, s, `INSERT INTO candidates (id, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
	           VALUES (1, '.go', 't', 'd', 1, 1, 1, '{}', '@o/t', 'h', 1)`)
	run(t, s, `INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url, body, commit_sha, start_line, end_line)
	           VALUES (1, 'acme/secret', 1, 'a.go', '.go', 'r', 'a', now(), 'u', 'b', 'abc', 3, 3)`)
	run(t, s, `INSERT INTO candidate_sources VALUES (1, 1)`)
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/secret', 20, 'h', 'b', now(), true, '{a.go}')`)
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	inj := &Injector{Model: fakeModel{}, Files: &CheckoutFiles{Root: t.TempDir(), CloneURL: func(string) string {
		t.Error("an ineligible repo was checked out")
		return ""
	}}, Scanner: sc}
	g := newGenerator(t, Config{Eligible: func(repo string) bool { return repo != "acme/secret" }}, s, inj)
	reports, err := g.Run(t.Context())
	if err != nil || len(reports) != 1 || reports[0].Skipped == "" || reports[0].Positives != 0 || reports[0].Injected != 0 {
		t.Fatalf("reports = %+v, %v", reports, err)
	}
	if rows, _ := s.CandidateEvalCases(t.Context(), 1); len(rows) != 0 {
		t.Fatalf("cases written from an ineligible repo: %d", len(rows))
	}
}

func TestGeneratorEndToEnd(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()

	// A real repository for the negative PR's head, read through the same
	// partial checkout the review engine uses.
	repo, err := synth.Build(ctx, filepath.Join(t.TempDir(), "src"),
		map[string]string{"pay/fetch.go": goFile},
		map[string]string{"pay/fetch.go": goFile, "pay/new.go": "package pay\n"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	run(t, s, `INSERT INTO cluster_runs (id, params, status) VALUES (1, '{}', 'done')`)
	run(t, s, `INSERT INTO candidates (id, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
	           VALUES (1, '.go', 'Check every error', 'Errors must be handled.', 2, 2, 5, '{}', '@acme/pay', 'h1', 1)`)
	for i, line := range []int{6, 0} { // the second source has no recorded location
		id := i + 1
		run(t, s, `INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url, body, commit_sha, start_line, end_line)
		           VALUES ($1, 'acme/pay', 1, 'pay/fetch.go', '.go', 'r', 'a', now(), 'u', 'b', $2, nullif($3, 0), nullif($3, 0))`,
			id, repo.HeadSHA, line)
		run(t, s, `INSERT INTO candidate_sources VALUES (1, $1)`, id)
	}
	// Negative PRs: one in scope and approved, one with only docs, one not approved.
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/pay', 20, $1, $2, now(), true, '{pay/fetch.go}')`, repo.HeadSHA, repo.BaseSHA)
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/pay', 21, $1, $2, now(), true, '{README.md}')`, repo.HeadSHA, repo.BaseSHA)
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/pay', 22, $1, $2, now(), false, '{pay/fetch.go}')`, repo.HeadSHA, repo.BaseSHA)
	// The PR the source comment was on never counts as a negative.
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/pay', 1, $1, $2, now(), true, '{pay/fetch.go}')`, repo.HeadSHA, repo.BaseSHA)

	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	inj := &Injector{
		Model:   fakeModel{Edit{StartLine: 6, EndLine: 9, Replacement: "\tresp, _ := c.Get(url)"}},
		Files:   &CheckoutFiles{Root: t.TempDir(), CloneURL: func(string) string { return repo.Dir }},
		Scanner: sc,
	}
	reports, err := newGenerator(t, Config{}, s, inj).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Positives != 1 || reports[0].Negatives != 1 || reports[0].Injected != 1 {
		t.Fatalf("reports = %+v", reports)
	}

	rows, err := s.CandidateEvalCases(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, r := range rows {
		kinds[r.CaseID] = r.Kind
		var expect []Expect
		_ = json.Unmarshal(r.Expect, &expect)
		switch r.Kind {
		case KindPositive:
			if len(expect) != 1 || expect[0].Lines != [2]int{6, 6} {
				t.Errorf("positive expect = %+v", expect)
			}
		case KindNegative:
			if len(expect) != 0 {
				t.Errorf("negative expects a finding: %+v", expect)
			}
		case KindInjected:
			if r.Patch == nil || !r.SpotCheck {
				t.Errorf("injected case = %+v", r)
			}
		}
	}
	want := map[string]string{"p-1": KindPositive, "n-acme-pay-20": KindNegative, "i-acme-pay-20": KindInjected}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("case %s = %q, want %q (all: %v)", id, kinds[id], k, kinds)
		}
	}

	// Candidates that already have cases are not regenerated.
	again, err := newGenerator(t, Config{}, s, inj).Run(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
}

func TestGeneratorSkipsCandidatesWithoutLocations(t *testing.T) {
	s := storetest.New(t)
	run(t, s, `INSERT INTO cluster_runs (id, params, status) VALUES (1, '{}', 'done')`)
	run(t, s, `INSERT INTO candidates (id, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
	           VALUES (1, '.go', 't', 'd', 1, 1, 1, '{}', '@o/t', 'h', 1)`)
	run(t, s, `INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url, body)
	           VALUES (1, 'acme/pay', 1, 'a.go', '.go', 'r', 'a', now(), 'u', 'b')`)
	run(t, s, `INSERT INTO candidate_sources VALUES (1, 1)`)
	reports, err := newGenerator(t, Config{}, s, nil).Run(t.Context())
	if err != nil || len(reports) != 1 || reports[0].Skipped == "" {
		t.Fatalf("reports = %+v, %v", reports, err)
	}
	if rows, _ := s.CandidateEvalCases(t.Context(), 1); len(rows) != 0 {
		t.Fatalf("cases written for a candidate without positives: %d", len(rows))
	}
}
