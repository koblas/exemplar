package seeds_test

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/evalgen"
	"github.com/koblas/exemplar/internal/learn/seeds"
	"github.com/koblas/exemplar/internal/owneract"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fakePR struct {
	number int
	url    string
	author string
	bot    bool
	fork   bool
	state  string
	merged bool
	added  []string
}

// fakeRepo is exemplar-config: files per branch ("main" is the default),
// and PRs by head branch. A branch's head SHA is its name.
type fakeRepo struct {
	files    map[string]map[string]string
	prs      map[string]*fakePR
	comments map[int][]string
	puts     []string
}

func newRepo() *fakeRepo {
	return &fakeRepo{files: map[string]map[string]string{"main": {}}, prs: map[string]*fakePR{}, comments: map[int][]string{}}
}

func (f *fakeRepo) DefaultBranch(context.Context) (string, string, error) { return "main", "main", nil }

func (f *fakeRepo) FindPR(_ context.Context, branch string) (string, bool, error) {
	if p, ok := f.prs[branch]; ok {
		return p.url, true, nil
	}
	return "", false, nil
}

func (f *fakeRepo) GetFile(_ context.Context, ref, path string) ([]byte, error) {
	c, ok := f.files[ref][path]
	if !ok {
		return nil, fmt.Errorf("%w: %s@%s", gh.ErrNotFound, path, ref)
	}
	return []byte(c), nil
}

func (f *fakeRepo) EnsureBranch(_ context.Context, branch, _ string) error {
	f.files[branch] = maps.Clone(f.files["main"])
	return nil
}

func (f *fakeRepo) PutFile(_ context.Context, branch, path string, content []byte, _ string) error {
	f.files[branch][path] = string(content)
	f.puts = append(f.puts, branch+":"+path)
	return nil
}

func (f *fakeRepo) open(branch, author string, bot bool, added ...string) *fakePR {
	n := len(f.prs) + 1
	p := &fakePR{number: n, url: fmt.Sprintf("https://github.com/acme/exemplar-config/pull/%d", n), author: author, bot: bot, state: "open", added: added}
	f.prs[branch] = p
	return p
}

func (f *fakeRepo) OpenPR(context.Context, string, string, string, string) (string, int, error) {
	panic("not used")
}

func (f *fakeRepo) OpenDraftPR(_ context.Context, branch, _, _, _ string) (string, error) {
	var added []string
	for p := range f.files[branch] {
		if _, ok := f.files["main"][p]; !ok {
			added = append(added, p)
		}
	}
	return f.open(branch, "exemplar[bot]", true, added...).url, nil
}

func (f *fakeRepo) MergePR(context.Context, int, string) error { panic("not used") }

func (f *fakeRepo) OpenPRs(context.Context) ([]gh.ConfigPR, error) {
	var out []gh.ConfigPR
	for branch, p := range f.prs {
		if p.state == "open" {
			out = append(out, gh.ConfigPR{Number: p.number, URL: p.url, Author: p.author, Bot: p.bot, HeadRef: branch, HeadSHA: branch, Fork: p.fork, Added: p.added})
		}
	}
	return out, nil
}

func (f *fakeRepo) Comment(_ context.Context, number int, body string) error {
	f.comments[number] = append(f.comments[number], body)
	return nil
}

func (f *fakeRepo) PRByBranch(_ context.Context, branch string) (gh.ConfigItem, bool, error) {
	p, ok := f.prs[branch]
	if !ok {
		return gh.ConfigItem{}, false, nil
	}
	return gh.ConfigItem{Number: p.number, URL: p.url, State: p.state, Merged: p.merged}, true, nil
}

func (f *fakeRepo) PullRequest(_ context.Context, number int) (gh.PullRequest, error) {
	for _, p := range f.prs {
		if p.number == number {
			typ := "User"
			if p.bot {
				typ = "Bot"
			}
			return gh.PullRequest{Number: number, Author: p.author, AuthorType: typ}, nil
		}
	}
	return gh.PullRequest{}, gh.ErrNotFound
}

// merge lands a PR's branch on main.
func (f *fakeRepo) merge(branch string) {
	maps.Copy(f.files["main"], f.files[branch])
	f.prs[branch].state, f.prs[branch].merged = "closed", true
}

const playgroundRule = `id: pay-check-errors
kind: exemplar
category: Reliability
severity: important
owner: "@acme/pay"
status: active
file_patterns: ["pay/**/*.go"]
description: |
  Errors from outbound calls in pay must be handled.
citation:
  doc: https://docs.internal/pay/errors
`

func seedRows(t *testing.T, st *store.Store) map[string]string {
	t.Helper()
	rows, err := st.Pool.Query(t.Context(), `SELECT rule_id, status || ' ' || coalesce(seed_author, '-') ||
		' evals=' || (seed_evals_at IS NOT NULL)::text || ' delivered=' || (seed_cases_delivered_at IS NOT NULL)::text
		FROM candidates WHERE source_kind = 'owner_seed'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = v
	}
	return out
}

func actions(outs []seeds.Outcome) map[string]string {
	m := map[string]string{}
	for _, o := range outs {
		m[o.Step+" "+o.RuleID] = o.Action
	}
	return m
}

// PRD §14.1 D1.1: an owner writes a rule in the playground or the config
// repo and it reaches active through a merged config PR, as a candidate
// with generated eval cases on the way.
func TestOwnerSeedsEndToEnd(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	repo := newRepo()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	job := &seeds.Job{Store: st, Repo: repo, Scanner: sc}
	gen, err := evalgen.New(evalgen.Config{Eligible: func(string) bool { return true }}, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	// History the seeds' negatives come from.
	if _, err := st.Pool.Exec(ctx, `INSERT INTO mined_prs VALUES ('acme/pay', 20, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'b20', now(), true, '{pay/api/fetch.go}'),
		('acme/web', 21, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'b21', now(), true, '{web/app.go}')`); err != nil {
		t.Fatal(err)
	}

	// 1. In the playground: alice saves a try as a candidate PR.
	run, err := st.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: "alice", Repo: "acme/pay", PrNumber: 4, RuleID: "pay-check-errors", RuleYaml: playgroundRule})
	if err != nil {
		t.Fatal(err)
	}
	act, err := st.CreateOwnerAction(ctx, queries.CreateOwnerActionParams{Action: owneract.ProposeRule, Target: strconv.FormatInt(run, 10), Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	r := owneract.Runner{Store: st, Repo: func(context.Context) (owneract.Repo, error) { return repo, nil }}
	if o, err := r.Run(ctx, &config.Snapshot{}, act); err != nil || o.Result != "draft PR opened" {
		t.Fatalf("propose_rule = %+v, %v", o, err)
	}
	var pgBranch string
	for b := range repo.prs {
		pgBranch = b
	}

	// 2. In the config repo: dana adds a rule by hand on her own branch;
	// mallory's comes from a fork; an edit of an existing rule is no seed.
	repo.files["main"]["rules/old.yaml"] = strings.Replace(playgroundRule, "pay-check-errors", "old", 1)
	repo.files["dana/seed"] = map[string]string{
		"rules/reliability/pay-timeouts.yaml": strings.NewReplacer("pay-check-errors", "pay-timeouts", "status: active", "status: candidate").Replace(playgroundRule),
		"rules/old.yaml":                      strings.Replace(playgroundRule, "status: active", "status: candidate", 1),
	}
	dana := repo.open("dana/seed", "dana", false, "rules/reliability/pay-timeouts.yaml")
	repo.files["fork"] = map[string]string{"rules/forked.yaml": strings.NewReplacer("pay-check-errors", "forked", "status: active", "status: candidate").Replace(playgroundRule)}
	repo.open("fork", "mallory", false, "rules/forked.yaml").fork = true

	outs, err := job.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a := actions(outs); a["sync pay-check-errors"] != "recorded" || a["sync pay-timeouts"] != "recorded" || a["sync forked"] != "skipped" || len(a) != 3 {
		t.Errorf("sync = %v", a)
	}
	want := map[string]string{"pay-check-errors": "pr_open alice evals=false delivered=false", "pay-timeouts": "pr_open dana evals=false delivered=false"}
	if got := seedRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("seeds = %v, want %v", got, want)
	}

	// 3. The evals job generates their cases; the next propose run
	// commits them on the app's branch and comments them on dana's.
	if reps, err := gen.RunSeeds(ctx); err != nil || len(reps) != 2 || reps[0].Negatives != 1 {
		t.Fatalf("RunSeeds = %+v, %v", reps, err)
	}
	if _, err := job.Run(ctx); err != nil {
		t.Fatal(err)
	}
	cases := repo.files[pgBranch]["evals/pay-check-errors/cases.yaml"]
	var ec schema.EvalCases
	if err := schema.Decode(schema.DocEvalCases, []byte(cases), &ec); err != nil || len(ec.Cases) != 1 || !strings.Contains(ec.Cases[0].Ref, "acme/pay@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("committed cases = %q, %v", cases, err)
	}
	if c := repo.comments[dana.number]; len(c) != 1 || !strings.Contains(c[0], "evals/pay-timeouts/cases.yaml") {
		t.Errorf("comments on dana's PR = %q", c)
	}
	if _, ok := repo.files["dana/seed"]["evals/pay-timeouts/cases.yaml"]; ok {
		t.Error("committed to a person's branch")
	}
	// Delivered once: another run writes and comments nothing more.
	puts := len(repo.puts)
	if _, err := job.Run(ctx); err != nil || len(repo.puts) != puts || len(repo.comments[dana.number]) != 1 {
		t.Errorf("delivered again: %v puts %d→%d", err, puts, len(repo.puts))
	}

	// 4. The owner sets the playground rule active and merges; dana closes
	// hers. The sweep promotes one and retires the other.
	repo.files[pgBranch]["rules/reliability/pay-check-errors.yaml"] = playgroundRule
	repo.merge(pgBranch)
	dana.state = "closed"
	outs, err = job.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a := actions(outs); a["sweep pay-check-errors"] != "promoted" || a["sweep pay-timeouts"] != "retired" {
		t.Errorf("sweep = %v", a)
	}
	want = map[string]string{"pay-check-errors": "promoted alice evals=true delivered=true", "pay-timeouts": "retired dana evals=true delivered=true"}
	if got := seedRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("seeds = %v, want %v", got, want)
	}
}

// A seed merged still as a candidate is not promoted: nothing runs yet.
// An owner's own cases are kept, and the seed killed in the UI stays
// killed while its PR is open.
func TestSeedEdges(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	repo := newRepo()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	job := &seeds.Job{Store: st, Repo: repo, Scanner: sc}
	cand := strings.Replace(playgroundRule, "status: active", "status: candidate", 1)
	repo.files["exemplar/x"] = map[string]string{
		"rules/reliability/pay-check-errors.yaml": cand,
		"evals/pay-check-errors/cases.yaml":       "rule: pay-check-errors\ncases: []\n",
	}
	repo.open("exemplar/x", "exemplar[bot]", true, "rules/reliability/pay-check-errors.yaml", "evals/pay-check-errors/cases.yaml")
	if _, err := job.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx, `SELECT id FROM candidates WHERE rule_id = 'pay-check-errors'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO candidate_eval_cases (candidate_id, case_id, kind, name, ref, expect, spot_check, paths)
		VALUES ($1, 'n-acme-pay-1', 'negative', 'n', 'acme/pay@abcdef1', '[]', false, '{a.go}')`, id); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSeedEvals(ctx, id); err != nil {
		t.Fatal(err)
	}
	outs, err := job.Run(ctx)
	if err != nil || actions(outs)["deliver pay-check-errors"] != "kept" || len(repo.puts) != 0 {
		t.Errorf("deliver = %+v, %v, puts %v", outs, err, repo.puts)
	}

	// Killed in the UI: the open PR does not revive it.
	if _, err := st.KillCandidate(ctx, queries.KillCandidateParams{ID: id, KillReason: new("dup")}); err != nil {
		t.Fatal(err)
	}
	if outs, err := job.Run(ctx); err != nil || actions(outs)["sync pay-check-errors"] != "skipped" {
		t.Errorf("sync after kill = %+v, %v", outs, err)
	}

	// A new PR for the same rule revives it; merged as a candidate it
	// stays pr_open. (A second open PR adding it meanwhile does not take
	// it over: see TestSeedStaysOnItsPR.)
	repo.prs["exemplar/x"].state = "closed"
	repo.files["exemplar/y"] = map[string]string{"rules/reliability/pay-check-errors.yaml": cand}
	repo.open("exemplar/y", "exemplar[bot]", true, "rules/reliability/pay-check-errors.yaml")
	if _, err := job.Run(ctx); err != nil {
		t.Fatal(err)
	}
	repo.merge("exemplar/y")
	if _, err := job.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var evals bool
	if err := st.Pool.QueryRow(ctx, `SELECT status, seed_evals_at IS NOT NULL FROM candidates WHERE id = $1`, id).Scan(&status, &evals); err != nil || status != "pr_open" || evals {
		t.Errorf("seed = %s (evals %v), %v", status, evals, err)
	}
}

// Two open PRs adding the same rule id: the seed stays on the first and
// its generated cases are not reset by the second. A person's PR from a
// branch named exemplar/... gets a comment, not a commit.
func TestSeedStaysOnItsPR(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	repo := newRepo()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	job := &seeds.Job{Store: st, Repo: repo, Scanner: sc}
	cand := strings.Replace(playgroundRule, "status: active", "status: candidate", 1)
	path := "rules/reliability/pay-check-errors.yaml"
	repo.files["exemplar/mine"] = map[string]string{path: cand}
	first := repo.open("exemplar/mine", "dana", false, path)
	if _, err := job.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx, `SELECT id FROM candidates WHERE rule_id = 'pay-check-errors'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO candidate_eval_cases (candidate_id, case_id, kind, name, ref, expect, spot_check, paths)
		VALUES ($1, 'n-acme-pay-1', 'negative', 'n', 'acme/pay@abcdef1', '[]', false, '{pay/a.go}')`, id); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSeedEvals(ctx, id); err != nil {
		t.Fatal(err)
	}
	repo.files["exemplar/again"] = map[string]string{path: cand}
	repo.open("exemplar/again", "exemplar[bot]", true, path)
	for range 2 {
		outs, err := job.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range outs {
			if o.Step == "sync" && o.URL != first.url && o.Action != "skipped" {
				t.Errorf("second PR took the seed: %+v", o)
			}
		}
	}
	var url string
	var evals bool
	if err := st.Pool.QueryRow(ctx, `SELECT config_pr_url, seed_evals_at IS NOT NULL FROM candidates WHERE id = $1`, id).Scan(&url, &evals); err != nil ||
		url != first.url || !evals {
		t.Errorf("seed on %s (evals %v), %v", url, evals, err)
	}
	if len(repo.puts) != 0 || len(repo.comments[first.number]) != 1 {
		t.Errorf("person's exemplar/ branch: puts %v, comments %d", repo.puts, len(repo.comments[first.number]))
	}
}

func TestTitle(t *testing.T) {
	for _, c := range []struct {
		rule schema.Rule
		want string
	}{
		{schema.Rule{ID: "x", Description: "  First line.\nSecond."}, "First line."},
		{schema.Rule{ID: "x"}, "x"},
		{schema.Rule{ID: "x", Description: strings.Repeat("a", 130)}, strings.Repeat("a", 117) + "..."},
	} {
		if got := seeds.Title(c.rule); got != c.want {
			t.Errorf("Title(%q) = %q, want %q", c.rule.Description, got, c.want)
		}
	}
}
