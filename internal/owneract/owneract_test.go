package owneract

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fakeRepo struct {
	files    map[string]string // path -> content on main
	branches map[string]map[string]string
	prs      []string
	mergeErr error
	merged   int
	drafts   int
}

func (f *fakeRepo) DefaultBranch(context.Context) (string, string, error) { return "main", "sha1", nil }
func (f *fakeRepo) FindPR(_ context.Context, branch string) (string, bool, error) {
	for _, p := range f.prs {
		if strings.HasSuffix(p, branch) {
			return p, true, nil
		}
	}
	return "", false, nil
}
func (f *fakeRepo) GetFile(_ context.Context, _, path string) ([]byte, error) {
	c, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(c), nil
}
func (f *fakeRepo) EnsureBranch(_ context.Context, branch, _ string) error {
	f.branches[branch] = map[string]string{}
	return nil
}
func (f *fakeRepo) PutFile(_ context.Context, branch, path string, content []byte, _ string) error {
	f.branches[branch][path] = string(content)
	return nil
}
func (f *fakeRepo) OpenPR(_ context.Context, branch, _, _, body string) (string, int, error) {
	if !strings.Contains(body, "Requested by @") {
		panic("PR body does not record the actor")
	}
	f.prs = append(f.prs, "https://github.com/acme/exemplar-config/pull/"+branch)
	return f.prs[len(f.prs)-1], len(f.prs), nil
}
func (f *fakeRepo) OpenDraftPR(_ context.Context, branch, _, _, body string) (string, error) {
	if !strings.Contains(body, "Requested by @") {
		panic("PR body does not record the actor")
	}
	f.drafts++
	f.prs = append(f.prs, "https://github.com/acme/exemplar-config/pull/"+branch)
	return f.prs[len(f.prs)-1], nil
}

func (f *fakeRepo) MergePR(context.Context, int, string) error {
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged++
	return nil
}

func TestRun(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	repo := &fakeRepo{branches: map[string]map[string]string{}, files: map[string]string{
		"rules/security/no-raw-sql.yaml": "id: no-raw-sql\nstatus: active # live\nowner: \"@acme/sec\"\n",
		"rules/old.yaml":                 "id: old\nstatus: shadow\nowner: \"@acme/sec\"\n",
		"repos/acme/app.yaml":            "enabled: true\nauto_approve:\n  enabled: true # pilot\n  approvable_owners: [\"@acme/app\"]\nshadow: false\n",
		"repos/acme/web.yaml":            "enabled: true\n",
	}}
	members := map[string][]string{"@acme/sec": {"dana"}, "@acme/app": {"erin"}, "@platform/devex": {"pat"}}
	memberErr := false
	r := Runner{Store: st, Repo: func(context.Context) (Repo, error) { return repo, nil }, PlatformTeam: "@platform/devex",
		Member: func(_ context.Context, team, login string) (bool, error) {
			if memberErr {
				return false, errors.New("GitHub is down")
			}
			for _, m := range members[team] {
				if m == login {
					return true, nil
				}
			}
			return false, nil
		}}
	snap := &config.Snapshot{
		Rules: []config.Rule{
			{Rule: schema.Rule{ID: "no-raw-sql", Status: schema.StatusActive, Owner: "@acme/sec"}, Scope: "global", Path: "rules/security/no-raw-sql.yaml"},
			{Rule: schema.Rule{ID: "old", Status: schema.StatusShadow, Owner: "@acme/sec"}, Scope: "global", Path: "rules/old.yaml"},
			{Rule: schema.Rule{ID: "local", Status: schema.StatusActive, Owner: "@acme/app"}, Scope: "acme/app", Path: ".exemplar/rules/local.yaml"},
		},
		Repos: map[string]schema.RepoConfig{
			"acme/app": {Enabled: true, AutoApprove: &schema.RepoAutoApprove{Enabled: true, ApprovableOwners: []string{"@acme/app"}}},
			"acme/web": {Enabled: true},
		},
	}
	var run int64
	if err := st.Pool.QueryRow(ctx, `INSERT INTO cluster_runs (params, status) VALUES ('{}', 'done') RETURNING id`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	var cand int64
	if err := st.Pool.QueryRow(ctx, `INSERT INTO candidates (ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
		VALUES ('go', 't', 'd', 5, 3, 0.9, '{}', '@acme/sec', 'h', $1) RETURNING id`, run).Scan(&cand); err != nil {
		t.Fatal(err)
	}
	act := func(action, target, actor, reason string) int64 {
		t.Helper()
		id, err := st.CreateOwnerAction(ctx, queries.CreateOwnerActionParams{Action: action, Target: target, Actor: actor, Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	check := func(name string, id int64, status, result string) queries.OwnerAction {
		t.Helper()
		o, err := r.Run(ctx, snap, id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		row, _ := st.GetOwnerAction(ctx, id)
		if o.Status != status || row.Status != status || !strings.Contains(*row.Result, result) {
			t.Errorf("%s: %+v, row %s %q", name, o, row.Status, *row.Result)
		}
		return row
	}

	row := check("owner shadows", act(ShadowRule, "no-raw-sql", "dana", "too noisy"), StatusDone, "merged")
	if row.PrUrl == nil || repo.merged != 1 {
		t.Errorf("no merged PR: %+v", row)
	}
	var got string
	for _, files := range repo.branches {
		got = files["rules/security/no-raw-sql.yaml"]
	}
	if got != "id: no-raw-sql\nstatus: shadow # live\nowner: \"@acme/sec\"\n" {
		t.Errorf("edited rule = %q", got)
	}
	check("not an owner", act(ShadowRule, "no-raw-sql", "mallory", ""), StatusRefused, "only @acme/sec")
	check("already shadow", act(ShadowRule, "old", "dana", ""), StatusRefused, "already shadow")
	check("platform kills", act(KillRule, "old", "pat", "obsolete"), StatusDone, "merged")
	repo.files["rules/cand.yaml"] = "id: cand\nstatus: candidate\n"
	snap.Rules = append(snap.Rules, config.Rule{Rule: schema.Rule{ID: "cand", Status: "candidate", Owner: "@acme/sec"}, Scope: "global", Path: "rules/cand.yaml"})
	check("shadow needs active", act(ShadowRule, "cand", "dana", ""), StatusRefused, "not active")
	check("repo rule", act(KillRule, "local", "pat", ""), StatusRefused, "repo's own rules")
	check("unknown rule", act(KillRule, "nope", "pat", ""), StatusRefused, "no global rule")

	check("candidate without reason", act(KillCandidate, itoa(cand), "dana", " "), StatusRefused, "needs a reason")
	check("candidate by a stranger", act(KillCandidate, itoa(cand), "erin", "dup"), StatusRefused, "only @acme/sec")
	check("candidate killed", act(KillCandidate, itoa(cand), "dana", "duplicate of no-raw-sql"), StatusDone, "killed")
	var status, reason string
	_ = st.Pool.QueryRow(ctx, `SELECT status, kill_reason FROM candidates WHERE id = $1`, cand).Scan(&status, &reason)
	if status != "killed" || !strings.Contains(reason, "@dana") {
		t.Errorf("candidate = %s %q", status, reason)
	}
	check("candidate again", act(KillCandidate, itoa(cand), "dana", "x"), StatusRefused, "not open")

	repo.mergeErr = errors.New("review required")
	row = check("approvable owner kills auto-approve", act(KillSwitch, "acme/app", "erin", "incident"), StatusDone, "waits for a review")
	for b, files := range repo.branches {
		if strings.Contains(b, "kill-switch") {
			got = files["repos/acme/app.yaml"]
		}
	}
	if got != "enabled: true\nauto_approve:\n  enabled: false # pilot\n  approvable_owners: [\"@acme/app\"]\nshadow: false\n" {
		t.Errorf("edited repo = %q", got)
	}
	check("no auto-approve block", act(KillSwitch, "acme/web", "pat", ""), StatusRefused, "already off")
	check("not opted in", act(KillSwitch, "acme/none", "pat", ""), StatusRefused, "not opted in")
	check("kill switch by a stranger", act(KillSwitch, "acme/app", "mallory", ""), StatusRefused, "only the platform team")

	// A membership failure is retried, and recorded as failed at the end.
	memberErr = true
	id := act(ShadowRule, "no-raw-sql", "dana", "")
	if _, err := r.Run(ctx, snap, id); err == nil {
		t.Fatal("a GitHub failure was not returned for retry")
	}
	if err := r.Fail(ctx, id, errors.New("GitHub is down")); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.GetOwnerAction(ctx, id); row.Status != StatusFailed {
		t.Errorf("failed action = %+v", row)
	}
	// A finished action is not run again.
	if o, err := r.Run(ctx, snap, id); err != nil || o.Result != "already finished" {
		t.Errorf("again = %+v, %v", o, err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestPropose(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	example, err := os.ReadFile("../../examples/exemplar-config/rules/security/no-raw-sql-concat.yaml")
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{branches: map[string]map[string]string{}, files: map[string]string{"rules/security/no-raw-sql-concat.yaml": string(example)}}
	r := Runner{Store: st, Repo: func(context.Context) (Repo, error) { return repo, nil }, PlatformTeam: "@platform/devex",
		Member: func(_ context.Context, team, login string) (bool, error) {
			return team == "@security/appsec" && login == "sam", nil
		}}
	snap := &config.Snapshot{Rules: []config.Rule{{Rule: schema.Rule{ID: "no-raw-sql-concat", Owner: "@security/appsec"}, Scope: "global",
		Path: "rules/security/no-raw-sql-concat.yaml"}}}
	try := func(actor, yaml string) int64 {
		t.Helper()
		id, err := st.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: actor, Repo: "acme/app", PrNumber: 4, RuleID: "x", RuleYaml: yaml})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	propose := func(name, actor string, run int64, status, result string) {
		t.Helper()
		id, err := st.CreateOwnerAction(ctx, queries.CreateOwnerActionParams{Action: ProposeRule, Target: itoa(run), Actor: actor})
		if err != nil {
			t.Fatal(err)
		}
		o, err := r.Run(ctx, snap, id)
		if err != nil || o.Status != status || !strings.Contains(o.Result, result) {
			t.Errorf("%s: %+v, %v", name, o, err)
		}
	}
	fresh := strings.Replace(string(example), "id: no-raw-sql-concat\n", "id: no-raw-sql-format\n", 1)
	run := try("alice", fresh)
	propose("someone else's run", "bob", run, StatusRefused, "only the person who ran")
	propose("new rule", "alice", run, StatusDone, "draft PR opened")
	var got string
	for _, files := range repo.branches {
		got = files["rules/security/no-raw-sql-format.yaml"]
	}
	if !strings.Contains(got, "status: candidate") || strings.Contains(got, "status: active") || repo.drafts != 1 || repo.merged != 0 {
		t.Errorf("new rule file = %q (drafts %d, merged %d)", got, repo.drafts, repo.merged)
	}
	edited := strings.Replace(string(example), "severity: blocking", "severity: important", 1)
	propose("edit by a stranger", "alice", try("alice", edited), StatusRefused, "only @security/appsec")
	propose("edit by the owner", "sam", try("sam", edited), StatusDone, "draft PR opened")
	propose("unchanged", "sam", try("sam", string(example)), StatusRefused, "the same as")
	propose("invalid", "alice", try("alice", "id: x\n"), StatusRefused, "not valid")
}
