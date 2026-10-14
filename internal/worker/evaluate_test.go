package worker

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestEvaluate(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	ew, err := NewEvaluateWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	// The working-tree diff: the scripted head's change, against the base.
	out, err := exec.CommandContext(t.Context(), "git", "-C", *e.dir, "diff", e.fake.base, e.head).Output()
	if err != nil {
		t.Fatal(err)
	}
	diff := string(out)
	if _, err := st.Pool.Exec(ctx, `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, inst, repo); err != nil {
		t.Fatal(err)
	}
	create := func(repoName, base, d string) uuid.UUID {
		t.Helper()
		id, err := st.CreateEvaluation(ctx, queries.CreateEvaluationParams{Repo: repoName, BaseSha: base, RequestedBy: "alice", Caller: "cli", Diff: &d, Mode: "review", Rules: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	get := func(id uuid.UUID) queries.GetEvaluationRow {
		t.Helper()
		r, err := st.GetEvaluation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	id := create(repo, e.fake.base, diff)
	if status, err := ew.Evaluate(ctx, id); err != nil || status != evaluate.StatusDone {
		t.Fatalf("evaluate = %s, %v", status, err)
	}
	r := get(id)
	var res evaluate.Result
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if r.Status != evaluate.StatusDone || len(res.Findings) != 1 || res.Findings[0].RuleID != "propagate-context" ||
		res.Findings[0].Path != "main.go" || res.Findings[0].StartLine != 9 || res.Rules == 0 {
		t.Fatalf("evaluation = %+v, result %+v", r, res)
	}
	var cleared bool
	if err := st.Pool.QueryRow(ctx, `SELECT diff IS NULL FROM evaluations WHERE id = $1`, id).Scan(&cleared); err != nil || !cleared {
		t.Errorf("diff kept: %v, %v", cleared, err)
	}
	// Nothing is posted and no review is stored as a PR's.
	var reviews int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM reviews`).Scan(&reviews); err != nil || reviews != 0 || len(e.fake.reviews)+len(e.fake.checks) != 0 {
		t.Errorf("stored %d reviews; posted %v %v", reviews, e.fake.reviews, e.fake.checks)
	}
	// Done once: a redelivered job does nothing.
	if _, err := ew.Evaluate(ctx, id); err == nil {
		t.Error("a finished evaluation ran again")
	}

	for _, c := range []struct {
		name, repo, base, diff, reason string
	}{
		{"ineligible repo", "acme/not-opted-in", e.fake.base, diff, "not eligible"},
		{"patch does not apply", repo, e.fake.base, "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,1 @@\n-package nope\n+package main\n", "does not apply"},
		{"base not on GitHub", repo, strings.Repeat("b", 40), diff, "base commit is not on GitHub"},
	} {
		id := create(c.repo, c.base, c.diff)
		status, err := ew.Evaluate(ctx, id)
		if r := get(id); err != nil || status != evaluate.StatusRefused || r.Reason == nil || !strings.Contains(*r.Reason, c.reason) {
			t.Errorf("%s: %s, %v, %+v", c.name, status, err, r)
		}
	}
	// No installation seen for the repo.
	if _, err := st.Pool.Exec(ctx, `DELETE FROM gh_events`); err != nil {
		t.Fatal(err)
	}
	id = create(repo, e.fake.base, diff)
	if status, _ := ew.Evaluate(ctx, id); status != evaluate.StatusRefused || !strings.Contains(*get(id).Reason, "not installed") {
		t.Errorf("no installation = %s, %+v", status, get(id))
	}
}
