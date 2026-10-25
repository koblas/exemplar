package evalgen

import (
	"path/filepath"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

const seedRule = `id: pay-check-errors
kind: exemplar
category: Reliability
severity: important
owner: "@acme/pay"
status: candidate
file_patterns: ["pay/**/*.go"]
description: |
  Errors from outbound calls in pay must be handled.
`

// An owner-written seed has no source comments: its negatives come from
// approved PRs touching its file_patterns in eligible repos, its positives
// are injected into them, and it is not regenerated until it changes.
func TestGeneratorSeeds(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	repo, err := synth.Build(ctx, filepath.Join(t.TempDir(), "src"),
		map[string]string{"pay/fetch.go": goFile},
		map[string]string{"pay/fetch.go": goFile, "pay/new.go": "package pay\n"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run(t, s, `INSERT INTO candidates (id, source_kind, ext, title, description, cluster_size, distinct_reviewers, score, score_detail,
	             owner, status, rule_id, rule_path, rule_yaml, config_pr_url, config_pr_branch)
	           VALUES (1, 'owner_seed', '*', 't', 'd', 0, 0, 0, '{}', '@acme/pay', 'pr_open', 'pay-check-errors',
	             'rules/reliability/pay-check-errors.yaml', $1, 'https://x/pull/7', 'seed')`, seedRule)
	run(t, s, `INSERT INTO candidates (id, source_kind, ext, title, description, cluster_size, distinct_reviewers, score, score_detail,
	             owner, status, rule_id, rule_path, rule_yaml)
	           VALUES (2, 'owner_seed', '*', 't', 'd', 0, 0, 0, '{}', '@acme/pay', 'pr_open', 'broken',
	             'rules/broken.yaml', 'id: broken')`)
	// In scope and approved; out of the rule's paths; in a repo no longer eligible.
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/pay', 20, $1, $2, now(), true, '{pay/fetch.go}')`, repo.HeadSHA, repo.BaseSHA)
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/pay', 21, $1, $2, now(), true, '{web/app.go}')`, repo.HeadSHA, repo.BaseSHA)
	run(t, s, `INSERT INTO mined_prs VALUES ('acme/secret', 22, $1, $2, now(), true, '{pay/fetch.go}')`, repo.HeadSHA, repo.BaseSHA)
	// A stale case from an earlier rule is replaced.
	run(t, s, `INSERT INTO candidate_eval_cases (candidate_id, case_id, kind, name, ref, expect, spot_check, paths)
	           VALUES (1, 'n-old', 'negative', 'old', 'x@y', '[]', false, '{}')`)

	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	inj := &Injector{
		Model:   fakeModel{Edit{StartLine: 6, EndLine: 9, Replacement: "\tresp, _ := c.Get(url)"}},
		Files:   &CheckoutFiles{Root: t.TempDir(), CloneURL: func(string) string { return repo.Dir }},
		Scanner: sc,
	}
	g := newGenerator(t, Config{Eligible: func(r string) bool { return r != "acme/secret" }}, s, inj)
	reports, err := g.RunSeeds(ctx)
	if err != nil || len(reports) != 2 {
		t.Fatalf("reports = %+v, %v", reports, err)
	}
	if r := reports[0]; r.Negatives != 1 || r.Injected != 1 || r.Skipped != "" {
		t.Errorf("seed report = %+v", r)
	}
	if r := reports[1]; r.Skipped == "" {
		t.Errorf("an invalid seed was not skipped: %+v", r)
	}
	rows, err := s.CandidateEvalCases(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.CaseID] = r.Kind
	}
	if len(got) != 2 || got["n-acme-pay-20"] != KindNegative || got["i-acme-pay-20"] != KindInjected {
		t.Errorf("seed cases = %v", got)
	}

	// Both were tried: neither runs again until its rule or PR changes.
	if again, err := g.RunSeeds(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
	// Mined-candidate generation never picks up a seed.
	if mined, err := g.Run(ctx); err != nil || len(mined) != 0 {
		t.Fatalf("Run = %+v, %v", mined, err)
	}
}
