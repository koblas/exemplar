package expiry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fakeRepo struct {
	files    map[string]string
	prs      map[string]gh.ConfigItem
	written  map[string]string
	bodies   []string
	merged   []int
	activity bool
}

func (f *fakeRepo) DefaultBranch(context.Context) (string, string, error) { return "main", "abc", nil }

func (f *fakeRepo) PRByBranch(_ context.Context, b string) (gh.ConfigItem, bool, error) {
	p, ok := f.prs[b]
	return p, ok, nil
}
func (f *fakeRepo) EnsureBranch(context.Context, string, string) error { return nil }
func (f *fakeRepo) GetFile(_ context.Context, _, path string) ([]byte, error) {
	s, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(s), nil
}

func (f *fakeRepo) PutFile(_ context.Context, branch, path string, c []byte, _ string) error {
	f.written[branch+":"+path] = string(c)
	return nil
}

func (f *fakeRepo) OpenPR(_ context.Context, branch, _, _, body string) (string, int, error) {
	f.bodies = append(f.bodies, body)
	f.prs[branch] = gh.ConfigItem{
		Number: 10 + len(f.prs), URL: "https://github.com/acme/exemplar-config/pull/1", State: "open",
		CreatedAt: time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC),
	}
	return f.prs[branch].URL, f.prs[branch].Number, nil
}

func (f *fakeRepo) MergePR(_ context.Context, n int, _ string) error {
	f.merged = append(f.merged, n)
	return nil
}

func (f *fakeRepo) HumanActivity(context.Context, int, time.Time) (bool, error) {
	return f.activity, nil
}

type fakeStats map[string]queries.RuleRecentOutcomesRow

func (f fakeStats) RuleRecentOutcomes(_ context.Context, a queries.RuleRecentOutcomesParams) (queries.RuleRecentOutcomesRow, error) {
	return f[a.RuleID], nil
}

// TestExpiry runs in a synctest bubble slept to 2026-10-14.
func TestExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)))
		testExpiry(t)
	})
}

func testExpiry(t *testing.T) {
	t.Helper()
	rule := func(id, reviewBy, status string) config.Rule {
		return config.Rule{
			ID: id, Status: status, Owner: "@platform/backend", ReviewBy: reviewBy,
			Path: "rules/" + id + ".yaml", Scope: "global", VersionSHA: "0123456789abcdef",
		}
	}
	snap := &config.Snapshot{Rules: []config.Rule{
		rule("healthy", "2026-10-01", schema.StatusActive), rule("noisy", "2026-10-01", schema.StatusActive),
		rule("quiet", "2026-10-01", schema.StatusActive), rule("current", "2027-10-01", schema.StatusActive),
		rule("asleep", "2026-01-01", schema.StatusShadow),
	}}
	file := func(id string) string {
		return "id: " + id + "\nstatus: active  # promoted\nreview_by: \"2026-10-01\"\n"
	}
	repo := &fakeRepo{
		files: map[string]string{"rules/healthy.yaml": file("healthy"), "rules/noisy.yaml": file("noisy"), "rules/quiet.yaml": file("quiet")},
		prs:   map[string]gh.ConfigItem{}, written: map[string]string{},
	}
	stats := fakeStats{
		"healthy": {Judged: 12, NegW: 1, PosW: 20},
		"noisy":   {Judged: 12, NegW: 8, PosW: 10},
	}
	run := func() map[string]Outcome {
		t.Helper()
		outs, err := Runner{Stats: stats, Repo: repo}.Run(t.Context(), snap)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Outcome{}
		for _, o := range outs {
			m[o.RuleID] = o
		}
		return m
	}
	outs := run()
	if len(outs) != 3 || outs["current"].RuleID != "" || outs["asleep"].RuleID != "" {
		t.Fatalf("outs = %+v", outs)
	}
	if o := outs["healthy"]; !o.Extend || o.Action != "proposed" ||
		repo.written["exemplar/expiry/healthy/extend/0123456789ab:rules/healthy.yaml"] != "id: healthy\nstatus: active  # promoted\nreview_by: 2027-10-14\n" {
		t.Errorf("healthy = %+v, written %v", o, repo.written)
	}
	for _, id := range []string{"noisy", "quiet"} {
		if o := outs[id]; o.Extend || o.Action != "proposed" ||
			repo.written["exemplar/expiry/"+id+"/shadow/0123456789ab:rules/"+id+".yaml"] != "id: "+id+"\nstatus: shadow\nreview_by: \"2026-10-01\"\n" {
			t.Errorf("%s = %+v", id, o)
		}
	}
	if !strings.Contains(strings.Join(repo.bodies, "\n"), "no findings with feedback in the last 90 days") ||
		!strings.Contains(strings.Join(repo.bodies, "\n"), "merges on its own after 2026-10-28") {
		t.Errorf("bodies = %q", repo.bodies)
	}
	time.Sleep(7 * 24 * time.Hour)
	if outs := run(); outs["noisy"].Action != "waiting" || outs["healthy"].Action != "waiting" || len(repo.merged) != 0 {
		t.Errorf("day 7 = %+v", outs)
	}
	time.Sleep(8 * 24 * time.Hour) // day 15
	repo.activity = true
	if outs := run(); outs["noisy"].Action != "responded" || len(repo.merged) != 0 {
		t.Errorf("responded = %+v", outs["noisy"])
	}
	repo.activity = false
	if outs := run(); outs["noisy"].Action != "demoted" || outs["quiet"].Action != "demoted" || outs["healthy"].Action != "waiting" || len(repo.merged) != 2 {
		t.Errorf("day 15 = %+v, merged %v", outs, repo.merged)
	}
	// Merged PRs are closed: nothing more.
	for b, p := range repo.prs {
		p.State = "closed"
		repo.prs[b] = p
	}
	if outs := run(); outs["noisy"].Action != "done" || len(repo.merged) != 2 {
		t.Errorf("after merge = %+v", outs)
	}
	// A dry run writes nothing.
	if outs, err := (Runner{Stats: stats}).Run(t.Context(), snap); err != nil || outs[0].Action != "dry_run" {
		t.Errorf("dry run = %+v, %v", outs, err)
	}
}

func TestRecentOutcomesQuery(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	var rid, run int64
	if err := st.Pool.QueryRow(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at)
		VALUES ('acme/app', 1, 'h', 'b', 'v', 'production', 'done', now()) RETURNING id`).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status)
		VALUES ($1, 'r', 'v', 'exemplar', '{a.go}', 'ok') RETURNING id`, rid).Scan(&run); err != nil {
		t.Fatal(err)
	}
	for i, age := range []int{10, 200} {
		var fid int64
		if err := st.Pool.QueryRow(ctx, `INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side,
			start_line, end_line, fingerprint, message, state, created_at)
			VALUES ($1, 'acme/app', 1, 'r', 'exemplar', 'important', 'issue', 'a.go', 'RIGHT', 1, 1, $2, 'm', 'posted', now() - make_interval(days => $3)) RETURNING id`,
			run, string(rune('a'+i)), age).Scan(&fid); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool.Exec(ctx, `INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via) VALUES ($1, 'fb_wrong', 'rev', 'reviewer', 2, 'slash')`, fid); err != nil {
			t.Fatal(err)
		}
	}
	s, err := st.RuleRecentOutcomes(ctx, queries.RuleRecentOutcomesParams{RuleID: "r", Days: Window})
	if err != nil || s.Judged != 1 || s.NegW != 2 || s.PosW != 0 {
		t.Errorf("recent = %+v, %v", s, err)
	}
}
