package worker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestPlaygroundWorker(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	ctx := t.Context()
	pw, err := NewPlaygroundWorker(e.w.cfg, func(y []byte) (*config.Snapshot, string, error) {
		return playground.RuleFileSnapshot(os.DirFS("../../examples/exemplar-config"), y, "cfg-test")
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := os.ReadFile("../../examples/exemplar-config/rules/reliability/propagate-context.yaml")
	if err != nil {
		t.Fatal(err)
	}
	run := func(yaml, ruleID string) queries.PlaygroundRun {
		t.Helper()
		id, err := st.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: "alice", Repo: repo, PrNumber: prNum, RuleID: ruleID, RuleYaml: yaml})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pw.Run(ctx, id); err != nil {
			t.Fatal(err)
		}
		r, err := st.GetPlaygroundRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	result := func(r queries.PlaygroundRun) evaluate.TryResult {
		t.Helper()
		var res evaluate.TryResult
		if err := json.Unmarshal(r.Result, &res); err != nil {
			t.Fatalf("result %s: %v", r.Result, err)
		}
		return res
	}

	// No installation known for the repo: the PR is not found.
	r := run(string(draft), "propagate-context")
	if res := result(r); r.Status != "done" || res.Ran || !strings.Contains(res.Note, "not found") {
		t.Fatalf("no installation = %+v %+v", r, res)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, inst, repo); err != nil {
		t.Fatal(err)
	}
	r = run(string(draft), "propagate-context")
	res := result(r)
	if r.Status != "done" || !res.Ran || len(res.Findings) != 1 || res.Findings[0].Path != "main.go" || res.Findings[0].Start != 9 || !res.Findings[0].Kept {
		t.Fatalf("run = %+v %+v", r, res)
	}
	// Nothing is posted on the PR.
	if len(e.fake.reviews)+len(e.fake.checks)+len(e.fake.replies) != 0 {
		t.Errorf("posted: %v %v %v", e.fake.reviews, e.fake.checks, e.fake.replies)
	}
	// A rule that does not load, or names another id, is refused.
	if r := run("id: x\nstatus: nope\n", "x"); r.Status != "refused" || !strings.Contains(*r.Reason, "does not load") {
		t.Errorf("bad rule = %+v", r)
	}
	if r := run(string(draft), "other-id"); r.Status != "refused" {
		t.Errorf("mismatched id = %+v", r)
	}
	// A finished run is not run again.
	if status, err := pw.Run(ctx, r.ID); err != nil || status != "done" {
		t.Errorf("again = %s, %v", status, err)
	}
}
