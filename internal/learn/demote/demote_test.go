package demote

import (
	"context"
	"errors"
	"math"
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

func TestWilson(t *testing.T) {
	for _, c := range []struct {
		k, n int
		want float64
	}{{30, 30, 0.8865}, {27, 30, 0.7438}, {24, 30, 0.6269}, {15, 30, 0.3315}, {0, 0, 0}} {
		if got := Wilson(c.k, c.n); math.Abs(got-c.want) > 0.0001 {
			t.Errorf("Wilson(%d, %d) = %.4f, want %.4f", c.k, c.n, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	// 30 judged, 6 negative: precision 0.8, Wilson 0.627 (under 0.80).
	bad := queries.DemotionStatsRow{RuleID: "r", Judged: 30, Negatives: 6, NegW: 12, PosW: 24, NonAuthorActors: 2}
	tests := []struct {
		name string
		mod  func(*queries.DemotionStatsRow)
		cfg  *schema.FeedbackConfig
		want bool
		why  string
	}{
		{name: "both bars and two non-author people", want: true},
		{name: "an owner adjudication instead", mod: func(s *queries.DemotionStatsRow) { s.NonAuthorActors, s.Adjudicated = 0, true }, want: true},
		{name: "author signals alone", mod: func(s *queries.DemotionStatsRow) { s.NonAuthorActors = 1 }, why: "flagged, not demoted"},
		{name: "too few judged", mod: func(s *queries.DemotionStatsRow) { s.Judged, s.Negatives = 29, 6 }, why: "only 29"},
		{name: "eFP within the bar", mod: func(s *queries.DemotionStatsRow) { s.NegW, s.PosW = 2, 30 }, why: "within the bar"},
		// 30 of 30 positive by count, but heavy negative weight: the Wilson
		// bound (0.887) clears the floor.
		{name: "precision bound meets the floor", mod: func(s *queries.DemotionStatsRow) { s.Negatives = 0 }, why: "meets the floor"},
		{
			name: "a stricter configured floor", mod: func(s *queries.DemotionStatsRow) { s.Negatives = 0 },
			cfg: &schema.FeedbackConfig{EFPBar: 0.1, DemotionWilsonFlr: 0.9}, want: true,
		},
		{name: "a looser configured bar", cfg: &schema.FeedbackConfig{EFPBar: 0.5}, why: "within the bar"},
	}
	for _, tt := range tests {
		s := bad
		if tt.mod != nil {
			tt.mod(&s)
		}
		d := Decide(s, tt.cfg)
		if d.Demote != tt.want || !strings.Contains(d.Why, tt.why) {
			t.Errorf("%s = %v (%s)", tt.name, d.Demote, d.Why)
		}
	}
}

func TestShadowed(t *testing.T) {
	tests := []struct{ in, want string }{
		{"id: r\nstatus: active\nowner: \"@a/b\"\n", "id: r\nstatus: shadow\nowner: \"@a/b\"\n"},
		{"id: r\nstatus: \"active\"  # promoted 2026-09\n", "id: r\nstatus: shadow\n"},
	}
	for _, tt := range tests {
		got, err := shadowed([]byte(tt.in))
		if err != nil || string(got) != tt.want {
			t.Errorf("shadowed(%q) = %q, %v", tt.in, got, err)
		}
	}
	for _, in := range []string{"id: r\nstatus: shadow\n", "status: active\nstatus: active\n", "id: r\n  status: active\n"} {
		if _, err := shadowed([]byte(in)); err == nil {
			t.Errorf("shadowed(%q) accepted", in)
		}
	}
}

type fakeRepo struct {
	issues   []gh.ConfigItem
	titles   []string
	closed   []int
	activity bool
	files    map[string]string
	prs      map[string]string // branch -> url
	written  map[string]string
	bodies   []string
	merged   []int
	mergeErr error
}

func (f *fakeRepo) DefaultBranch(context.Context) (string, string, error) { return "main", "abc", nil }

func (f *fakeRepo) FindPR(_ context.Context, branch string) (string, bool, error) {
	u, ok := f.prs[branch]
	return u, ok, nil
}
func (f *fakeRepo) EnsureBranch(context.Context, string, string) error { return nil }
func (f *fakeRepo) GetFile(_ context.Context, _, path string) ([]byte, error) {
	s, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(s), nil
}

func (f *fakeRepo) PutFile(_ context.Context, branch, path string, content []byte, _ string) error {
	f.written[branch+":"+path] = string(content)
	return nil
}

func (f *fakeRepo) OpenPR(_ context.Context, branch, _, _, body string) (string, int, error) {
	f.bodies = append(f.bodies, body)
	f.prs[branch] = "https://github.com/acme/exemplar-config/pull/9"
	return f.prs[branch], 9, nil
}

func (f *fakeRepo) MergePR(_ context.Context, n int, _ string) error {
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged = append(f.merged, n)
	return nil
}

func (f *fakeRepo) FindIssue(_ context.Context, _, title string) (gh.ConfigItem, bool, error) {
	for i, t := range f.titles {
		if t == title {
			return f.issues[i], true, nil
		}
	}
	return gh.ConfigItem{}, false, nil
}

func (f *fakeRepo) CreateIssue(_ context.Context, title, body string, _ []string) (gh.ConfigItem, error) {
	it := gh.ConfigItem{Number: 100 + len(f.issues), URL: "https://github.com/acme/exemplar-config/issues/x", State: "open", CreatedAt: time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)}
	f.issues, f.titles, f.bodies = append(f.issues, it), append(f.titles, title), append(f.bodies, body)
	return it, nil
}

func (f *fakeRepo) CloseIssue(_ context.Context, n int, _ string) error {
	f.closed = append(f.closed, n)
	return nil
}

func (f *fakeRepo) HumanActivity(context.Context, int, time.Time) (bool, error) {
	return f.activity, nil
}

type fakeStats []queries.DemotionStatsRow

func (f fakeStats) DemotionStats(context.Context, int32) ([]queries.DemotionStatsRow, error) {
	return f, nil
}

// TestRunner runs in a synctest bubble slept to 2026-10-14.
func TestRunner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)))
		testRunner(t)
	})
}

func testRunner(t *testing.T) {
	t.Helper()
	bad := func(id string) queries.DemotionStatsRow {
		return queries.DemotionStatsRow{RuleID: id, Judged: 30, Negatives: 6, NegW: 12, PosW: 24, NonAuthorActors: 2}
	}
	rule := func(id, status, scope string) config.Rule {
		return config.Rule{ID: id, Status: status, Owner: "@platform/backend", Path: "rules/" + id + ".yaml", Scope: scope, VersionSHA: "0123456789abcdef"}
	}
	snap := &config.Snapshot{Rules: []config.Rule{
		rule("noisy", schema.StatusActive, "global"), rule("quiet", schema.StatusActive, "global"),
		rule("already", schema.StatusShadow, "global"), rule("local", schema.StatusActive, "acme/app"),
	}}
	stats := fakeStats{bad("noisy"), {RuleID: "quiet", Judged: 30, PosW: 30}, bad("already"), bad("local"), bad("bug.nil-deref")}
	newRepo := func() *fakeRepo {
		return &fakeRepo{files: map[string]string{"rules/noisy.yaml": "id: noisy\nstatus: active\n"}, prs: map[string]string{}, written: map[string]string{}}
	}

	repo := newRepo()
	decisions, outs, err := Runner{Stats: stats, Repo: repo}.Run(t.Context(), snap)
	if err != nil || len(decisions) != 5 {
		t.Fatalf("Run = %d decisions, %v", len(decisions), err)
	}
	actions := map[string]string{}
	for _, o := range outs {
		actions[o.RuleID] = o.Action
	}
	want := map[string]string{"noisy": "demoted", "already": "skipped", "local": "skipped", "bug.nil-deref": "skipped"}
	if len(actions) != len(want) {
		t.Errorf("actions = %v", actions)
	}
	for id, a := range want {
		if actions[id] != a {
			t.Errorf("%s = %q, want %q (all %v)", id, actions[id], a, actions)
		}
	}
	branch := "exemplar/demote/noisy/0123456789ab"
	if repo.written[branch+":rules/noisy.yaml"] != "id: noisy\nstatus: shadow\n" || len(repo.merged) != 1 {
		t.Errorf("written = %v, merged = %v", repo.written, repo.merged)
	}
	if len(repo.bodies) != 1 || !strings.HasPrefix(repo.bodies[0], "@platform/backend:") || !strings.Contains(repo.bodies[0], "0.627") {
		t.Errorf("body = %q", repo.bodies)
	}

	// The next night: the PR exists, so nothing is opened again.
	if _, outs, err := (Runner{Stats: stats, Repo: repo}).Run(t.Context(), snap); err != nil || outs[0].Action != "skipped" || len(repo.bodies) != 1 {
		t.Errorf("rerun = %+v, %v", outs, err)
	}

	// Protection refuses the merge: the PR stays open for a human.
	repo = newRepo()
	repo.mergeErr = errors.New("review required")
	if _, outs, err := (Runner{Stats: fakeStats{bad("noisy")}, Repo: repo}).Run(t.Context(), snap); err != nil || outs[0].Action != "opened" {
		t.Errorf("unmerged = %+v, %v", outs, err)
	}

	// A dry run writes nothing.
	if _, outs, err := (Runner{Stats: fakeStats{bad("noisy")}}).Run(t.Context(), snap); err != nil || outs[0].Action != "dry_run" {
		t.Errorf("dry run = %+v, %v", outs, err)
	}

	// The file no longer says active (someone already changed it): an error
	// for that rule, not a broken PR.
	repo = newRepo()
	repo.files["rules/noisy.yaml"] = "id: noisy\nstatus: shadow\n"
	if _, outs, err := (Runner{Stats: fakeStats{bad("noisy")}, Repo: repo}).Run(t.Context(), snap); err == nil || outs[0].Action != "skipped" || len(repo.bodies) != 0 {
		t.Errorf("stale = %+v, %v", outs, err)
	}
}

func TestDemotionStatsQuery(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	posted := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var rid, run int64
	if err := st.Pool.QueryRow(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at)
		VALUES ('acme/app', 1, 'h', 'b', 'v', 'production', 'done', $1) RETURNING id`, posted).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status)
		VALUES ($1, 'r', 'v', 'exemplar', '{a.go}', 'ok') RETURNING id`, rid).Scan(&run); err != nil {
		t.Fatal(err)
	}
	n := 0
	finding := func(rule string, events ...[4]any) {
		n++
		var id int64
		if err := st.Pool.QueryRow(ctx, `INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side,
			start_line, end_line, fingerprint, message, state, created_at)
			VALUES ($1, 'acme/app', 1, $2, 'exemplar', 'important', 'issue', 'a.go', 'RIGHT', $3, $3, $4, 'm', 'posted', $5) RETURNING id`,
			run, rule, n, rule+string(rune('a'+n%26))+string(rune('a'+n/26)), posted.Add(time.Duration(n)*time.Hour)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if _, err := st.Pool.Exec(ctx, `INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via) VALUES ($1, $2, $3, $4, $5, 'slash')`,
				id, e[0], e[1], e[2], e[3]); err != nil {
				t.Fatal(err)
			}
		}
	}
	finding("r", [4]any{"fb_wrong", "dana", "reviewer", 2})
	finding("r", [4]any{"fb_wrong", "alice", "author", 1}, [4]any{"adjudicated_overturn", "olga", "owner", 4})
	finding("r", [4]any{"fb_fixed", "alice", "author", 1})
	finding("r", [4]any{"react_down", "zed", "reviewer", 0.5}) // not explicit: not judged
	finding("r")                                               // no feedback: not judged
	for range 32 {
		finding("s", [4]any{"fb_fixed", "alice", "author", 1})
	}
	rows, err := st.DemotionStats(ctx, Window)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]queries.DemotionStatsRow{}
	for _, r := range rows {
		got[r.RuleID] = r
	}
	if r := got["r"]; r.Judged != 3 || r.Negatives != 2 || r.NegW != 6 || r.PosW != 1 || !r.Adjudicated || r.NonAuthorActors != 1 {
		t.Errorf("r = %+v", r)
	}
	// Only the most recent 30 count.
	if s := got["s"]; s.Judged != Window || s.Negatives != 0 || s.PosW != 30 {
		t.Errorf("s = %+v", s)
	}
}

// TestFlag runs in a synctest bubble slept to 2026-10-14.
func TestFlag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)))
		testFlag(t)
	})
}

func testFlag(t *testing.T) {
	t.Helper()
	// Both bars crossed, but only the author objected: a flag.
	authorOnly := queries.DemotionStatsRow{RuleID: "noisy", Judged: 30, Negatives: 6, NegW: 12, PosW: 24, NonAuthorActors: 1}
	snap := &config.Snapshot{Rules: []config.Rule{{
		ID: "noisy", Status: schema.StatusActive, Owner: "@platform/backend",
		Path: "rules/noisy.yaml", Scope: "global", VersionSHA: "0123456789abcdef",
	}}}
	repo := &fakeRepo{files: map[string]string{"rules/noisy.yaml": "id: noisy\nstatus: active\n"}, prs: map[string]string{}, written: map[string]string{}}
	run := func() Outcome {
		t.Helper()
		_, outs, err := Runner{Stats: fakeStats{authorOnly}, Repo: repo}.Run(t.Context(), snap)
		if err != nil || len(outs) != 1 {
			t.Fatalf("%s: %+v, %v", time.Now().Format(time.DateOnly), outs, err)
		}
		return outs[0]
	}
	if o := run(); o.Action != "flagged" || !o.Flag || len(repo.titles) != 1 || repo.titles[0] != "Exemplar flag: noisy (0123456789ab)" ||
		!strings.Contains(repo.bodies[0], "@platform/backend") || !strings.Contains(repo.bodies[0], "Respond by 2026-10-28") {
		t.Fatalf("first = %+v, %q", o, repo.bodies)
	}
	time.Sleep(7 * 24 * time.Hour)
	if o := run(); o.Action != "waiting" || len(repo.bodies) != 1 {
		t.Errorf("day 7 = %+v", o)
	}
	repo.activity = true
	time.Sleep(8 * 24 * time.Hour) // day 15
	if o := run(); o.Action != "responded" || len(repo.merged) != 0 {
		t.Errorf("owner responded = %+v", o)
	}
	repo.activity = false
	if o := run(); o.Action != "demoted" || len(repo.merged) != 1 || len(repo.closed) != 1 ||
		repo.written["exemplar/demote/noisy/0123456789ab:rules/noisy.yaml"] != "id: noisy\nstatus: shadow\n" || !strings.Contains(o.Why, "no response in 14 days") {
		t.Errorf("no response = %+v (merged %v, closed %v)", o, repo.merged, repo.closed)
	}
	// A closed flag is a response.
	repo2 := &fakeRepo{
		files: repo.files, prs: map[string]string{}, written: map[string]string{},
		issues: []gh.ConfigItem{{Number: 7, State: "closed"}}, titles: []string{"Exemplar flag: noisy (0123456789ab)"},
	}
	if _, outs, err := (Runner{Stats: fakeStats{authorOnly}, Repo: repo2}).Run(t.Context(), snap); err != nil || outs[0].Action != "responded" {
		t.Errorf("closed flag = %+v, %v", outs, err)
	}
}
