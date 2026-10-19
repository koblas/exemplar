package worker

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// Mode eval evaluates a config PR's changed rules at its head.
func TestEvaluateEvalMode(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	ctx := t.Context()
	const cfgRepo = "acme/exemplar-config"
	cfgDir := filepath.Join(t.TempDir(), "cfg")
	if err := os.CopyFS(cfgDir, os.DirFS("../../examples/exemplar-config")); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cfgDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	p := filepath.Join(cfgDir, "rules/reliability/propagate-context.yaml")
	data, _ := os.ReadFile(p)
	_ = os.WriteFile(p, append(data, []byte("model_tier: high\n")...), 0o644)
	git("commit", "-q", "-am", "tune rule")
	head := git("rev-parse", "HEAD")

	clone := e.w.cfg.CloneURL
	e.w.cfg.CloneURL = func(r string) string {
		if r == cfgRepo {
			return cfgDir
		}
		return clone(r)
	}
	e.w.cfg.ConfigRepo = cfgRepo
	ew, err := NewEvaluateWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, inst, cfgRepo); err != nil {
		t.Fatal(err)
	}
	scopes, requester := []string{"evaluate:acme/*"}, "svc:ci@clients"
	create := func(r, b, h string, rules ...string) queries.GetEvaluationRow {
		t.Helper()
		id, err := st.CreateEvaluation(ctx, queries.CreateEvaluationParams{
			Repo: r, BaseSha: b, RequestedBy: requester, Caller: "agent",
			Mode: evaluate.ModeEval, HeadSha: &h, Rules: append([]string{}, rules...), RequesterScopes: scopes,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ew.Evaluate(ctx, id); err != nil {
			t.Fatal(err)
		}
		row, err := st.GetEvaluation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	result := func(row queries.GetEvaluationRow) (evaluate.EvalResult, []evalrun.Report) {
		t.Helper()
		var res evaluate.Result
		if err := json.Unmarshal(row.Result, &res); err != nil || res.Eval == nil {
			reason := ""
			if row.Reason != nil {
				reason = *row.Reason
			}
			t.Fatalf("result = %s %s %q, %v", row.Status, row.Result, reason, err)
		}
		var reps []evalrun.Report
		if err := json.Unmarshal(res.Eval.Reports, &reps); err != nil {
			t.Fatal(err)
		}
		return *res.Eval, reps
	}

	// The changed rule has no cases, so the gate fails; the extra rule
	// asked for is evaluated too.
	row := create(cfgRepo, base, head, "wrap-errors-with-cause")
	ev, reps := result(row)
	if row.Status != evaluate.StatusDone || ev.Pass || len(reps) != 2 || reps[0].Rule != "propagate-context" ||
		!strings.Contains(ev.Markdown, "no evals/propagate-context/cases.yaml") || strings.Join(ev.Rules, ",") != "propagate-context,wrap-errors-with-cause" {
		t.Fatalf("eval = %+v %+v", row, ev)
	}
	// No change: nothing to evaluate, and that passes.
	if ev, reps := result(create(cfgRepo, head, head)); !ev.Pass || len(reps) != 0 || !strings.Contains(ev.Markdown, "No rules to evaluate") {
		t.Errorf("no change = %+v", ev)
	}
	// A head's case on a repo the requester cannot read is never run, even
	// an eligible one: the service's permissions cover only exemplar-config.
	cases := filepath.Join(cfgDir, "evals/propagate-context/cases.yaml")
	_ = os.MkdirAll(filepath.Dir(cases), 0o755)
	_ = os.WriteFile(cases, []byte("rule: propagate-context\ncases:\n  - name: probe\n    ref: "+repo+"@"+strings.Repeat("d", 40)+
		"\n    paths: [main.go]\n    expect: [{path: main.go, lines: [1, 1]}]\n"), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "add a case")
	withCase := git("rev-parse", "HEAD")
	outcome := func() evalrun.Outcome {
		t.Helper()
		_, reps := result(create(cfgRepo, base, withCase))
		if len(reps) != 1 || len(reps[0].Outcomes) != 1 {
			t.Fatalf("reports = %+v", reps)
		}
		return reps[0].Outcomes[0]
	}
	scopes = []string{"evaluate:acme/exemplar-config"}
	if o := outcome(); o.Result != "unavailable" || o.Detail != "repo is not eligible" {
		t.Errorf("unreadable case repo = %+v", o)
	}
	scopes = []string{"evaluate:*"}
	if o := outcome(); o.Detail == "repo is not eligible" {
		t.Errorf("readable case repo refused = %+v", o)
	}
	// A GitHub login: the repo's collaborator permission decides.
	if _, err := st.Pool.Exec(ctx, `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, inst, repo); err != nil {
		t.Fatal(err)
	}
	scopes, requester = nil, "mallory"
	e.fake.noAccess = map[string]bool{"mallory": true}
	if o := outcome(); o.Detail != "repo is not eligible" {
		t.Errorf("case repo the login cannot read = %+v", o)
	}
	requester = "alice"
	if o := outcome(); o.Detail == "repo is not eligible" {
		t.Errorf("case repo the login can read refused = %+v", o)
	}
	scopes, requester = []string{"evaluate:acme/*"}, "svc:ci@clients"

	// Only exemplar-config, and only pushed commits.
	if row := create(repo, base, head); row.Status != evaluate.StatusRefused || !strings.Contains(*row.Reason, "exemplar-config only") {
		t.Errorf("other repo = %+v", row)
	}
	if row := create(cfgRepo, base, strings.Repeat("c", 40)); row.Status != evaluate.StatusRefused || !strings.Contains(*row.Reason, "not on GitHub") {
		t.Errorf("unpushed head = %+v", row)
	}
}
