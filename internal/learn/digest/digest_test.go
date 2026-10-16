package digest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fakeRepo struct {
	titles, bodies []string
}

func (f *fakeRepo) FindIssue(_ context.Context, _, title string) (gh.ConfigItem, bool, error) {
	if slices.Contains(f.titles, title) {
		return gh.ConfigItem{URL: "u"}, true, nil
	}
	return gh.ConfigItem{}, false, nil
}

func (f *fakeRepo) CreateIssue(_ context.Context, title, body string, _ []string) (gh.ConfigItem, error) {
	f.titles, f.bodies = append(f.titles, title), append(f.bodies, body)
	return gh.ConfigItem{URL: "https://github.com/acme/exemplar-config/issues/9"}, nil
}

func TestDigest(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := st.Pool.QueryRow(ctx, sql+" RETURNING id", args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	rid := exec(`INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at, finished_at)
		VALUES ('acme/app', 4, 'abcdef0123456789', 'b', 'v', 'production', 'done', now(), now())`)
	run := exec(`INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status) VALUES ($1, 'fetch-in-loop', 'v', 'exemplar', '{a.go}', 'ok')`, rid)
	n := 0
	finding := func(rule, path string, events ...string) {
		n++
		fid := exec(`INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side, start_line, end_line,
			fingerprint, message, state, gh_comment_id) VALUES ($1, 'acme/app', 4, $2, 'exemplar', 'important', 'issue', $3, 'RIGHT', 1, 1, $4, 'm', 'posted', $5)`,
			run, rule, path, rule+string(rune('a'+n)), 9000+n)
		for _, e := range events {
			parts := strings.Split(e, ":")
			kind, actor, role := parts[0], parts[1], parts[2]
			if _, err := st.Pool.Exec(ctx, `INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via) VALUES ($1, $2, $3, $4, $5, 'slash')`,
				fid, kind, actor, role, map[string]float32{"author": 1, "reviewer": 2}[role]); err != nil {
				t.Fatal(err)
			}
		}
	}
	// fetch-in-loop: 4 posted, 3 fixed, 1 author "not important" under tools/.
	finding("fetch-in-loop", "src/net/a.go", "fb_fixed:alice:author")
	finding("fetch-in-loop", "src/net/b.go", "fb_fixed:alice:author")
	finding("fetch-in-loop", "src/net/c.go", "fb_fixed:alice:author")
	finding("fetch-in-loop", "tools/gen/x.go", "fb_not_important:alice:author")
	// arena: an author's "wrong" waiting for adjudication.
	finding("prefer-arena", "src/sim/a.go", "fb_wrong:bob:author")
	if _, err := st.Pool.Exec(ctx, `INSERT INTO approval_decisions (review_id, stage1, stage2, decision) VALUES ($1, '{}', $2, 'approved')`, rid,
		`{"codeowners": {"pass": true, "owners": {"docs/runtime/a.md": ["@platform/backend"], "docs/other.md": ["@docs/team"]}}}`); err != nil {
		t.Fatal(err)
	}

	rule := func(id, owner, reviewBy string) config.Rule {
		return config.Rule{ID: id, Status: schema.StatusActive, Owner: owner, ReviewBy: reviewBy, Scope: "global", VersionSHA: "v"}
	}
	snap := &config.Snapshot{Rules: []config.Rule{
		rule("fetch-in-loop", "@platform/backend", "2027-01-01"), rule("prefer-arena", "@platform/backend", "2026-09-01"),
		rule("legacy", "@platform/backend", ""), rule("docs-style", "@docs/team", ""),
	}}
	repo := &fakeRepo{}
	now := func() time.Time { return time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) } // a Thursday
	ds, err := Runner{Store: st, Repo: repo, Now: now}.Run(ctx, snap)
	if err != nil || len(ds) != 2 || ds[0].Action != "opened" {
		t.Fatalf("digests = %+v, %v", ds, err)
	}
	if repo.titles[1] != "Rule health for @platform/backend, week of October 12, 2026" {
		t.Errorf("titles = %q", repo.titles)
	}
	body := repo.bodies[1]
	for _, want := range []string{
		"| `fetch-in-loop` | active | 4 | 75% | 25% | Over the bar (not enough judged findings to demote yet) |",
		"| `prefer-arena` | active | 1 | 0% | 100% | Review date passed (2026-09-01). Confirm or retire. |",
		"| `legacy` | active | 0 | — | — | No feedback yet |",
		"`fetch-in-loop`: *Not important here* (1), mostly under `tools/gen/**`",
		"Waiting for your adjudication** (1)",
		"[acme/app#4](https://github.com/acme/app/pull/4#discussion_r9005): the author says *Wrong*",
		"[acme/app#4](https://github.com/acme/app/pull/4) at `abcdef012345`: docs/runtime/a.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(repo.bodies[0], "fetch-in-loop") || !strings.Contains(repo.bodies[0], "docs/other.md") {
		t.Errorf("docs digest = %s", repo.bodies[0])
	}
	// The same week again: no new issues.
	if ds, err := (Runner{Store: st, Repo: repo, Now: now}).Run(ctx, snap); err != nil || ds[0].Action != "exists" || len(repo.titles) != 2 {
		t.Errorf("again = %+v, %v", ds, err)
	}
}

func TestDriftDigest(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO export_runs (config_sha, first_seen) VALUES ('c1', now() - interval '10 days'), ('c2', now() - interval '8 days'),
			('c3', now() - interval '6 days'), ('c4', now() - interval '4 days')`,
		// Current: 0 behind.
		`INSERT INTO exports (repo, path, target, content_hash, base_hash, config_sha, state, current_sha, current_at)
			VALUES ('acme/fresh', 'AGENTS.md', 'agents_md', 'h', '', 'c4', 'current', 'c4', now())`,
		// Last current before c2: c2, c3 and c4 since.
		`INSERT INTO exports (repo, path, target, content_hash, base_hash, config_sha, pr_url, state, current_sha, current_at)
			VALUES ('acme/stale', 'AGENTS.md', 'agents_md', 'h', 'g', 'c4', 'https://github.com/acme/stale/pull/2', 'proposed', 'c1', now() - interval '9 days'),
			       ('acme/stale', 'CLAUDE.md', 'claude_md', 'h', 'g', 'c1', NULL, 'edited', 'c1', now() - interval '9 days')`,
		// Never merged, first proposed after c3: 2 behind, under the bar.
		`INSERT INTO exports (repo, path, target, content_hash, base_hash, config_sha, state, created_at)
			VALUES ('acme/new', 'AGENTS.md', 'agents_md', 'h', '', 'c4', 'proposed', now() - interval '5 days')`,
	} {
		if _, err := st.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	repo := &fakeRepo{}
	now := func() time.Time { return time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) }
	ds, err := Runner{Store: st, Repo: repo, Platform: "@platform/devex", Now: now}.Run(ctx, &config.Snapshot{})
	if err != nil || len(ds) != 1 || ds[0].Owner != "@platform/devex" || ds[0].Title != "Export drift, week of October 12, 2026" {
		t.Fatalf("digests = %+v, %v", ds, err)
	}
	body := repo.bodies[0]
	if !strings.Contains(body, "| acme/stale | 3 | 1 proposed, not merged; 1 edited by hand inside the markers | https://github.com/acme/stale/pull/2 |") ||
		strings.Contains(body, "acme/new") || strings.Contains(body, "acme/fresh") {
		t.Errorf("body:\n%s", body)
	}
	// No platform team: no drift digest.
	if ds, err := (Runner{Store: st, Now: now}).Run(ctx, &config.Snapshot{}); err != nil || len(ds) != 0 {
		t.Errorf("without platform = %+v, %v", ds, err)
	}
}

func TestUpstreamRewrite(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := st.Pool.QueryRow(ctx, sql+" RETURNING id", args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	for _, id := range []string{"no-raw-sql", "prefer-arena"} {
		if _, err := st.Pool.Exec(ctx, `INSERT INTO rules (rule_id, version_sha, scope, kind, category, severity, status, owner, exported, spec)
			VALUES ($1, 'v', 'global', 'exemplar', 'c', 'important', 'active', '@platform/backend', true, '{}')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO exports (repo, path, target, content_hash, base_hash, config_sha, state, landed_at)
		VALUES ('acme/app', '.cursor/rules/exemplar.mdc', 'cursor', 'h', '', 'c1', 'current', now() - interval '30 days')`); err != nil {
		t.Fatal(err)
	}
	// 25 Cursor PRs and 25 human PRs since the export landed. no-raw-sql:
	// one violation on every PR either way. prefer-arena: none on the
	// agent's PRs. A refuted finding is not a violation.
	pr := 0
	for i := range 50 {
		pr++
		agent := map[bool]string{true: "cursor", false: ""}[i%2 == 0]
		rid := exec(`INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at, finished_at, agent, agent_reads)
			VALUES ('acme/app', $1, 'h', 'b', 'v', 'production', 'done', now() - interval '3 days', now(), $2, $3)`, pr, agent, map[bool][]string{true: {"cursor", "agents_md"}, false: {}}[agent != ""])
		for _, rule := range []string{"no-raw-sql", "prefer-arena"} {
			run := exec(`INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status) VALUES ($1, $2, 'v', 'exemplar', '{a.go}', 'ok')`, rid, rule)
			if rule == "prefer-arena" && agent != "" {
				exec(`INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side, start_line, end_line,
					fingerprint, message, state, suppressed_reason) VALUES ($1, 'acme/app', $2, $3, 'exemplar', 'important', 'issue', 'a.go', 'RIGHT', 1, 1, $4, 'm', 'dropped', 'refuted')`,
					run, pr, rule, fmt.Sprint(rule, pr, "r"))
				continue
			}
			exec(`INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side, start_line, end_line,
				fingerprint, message, state) VALUES ($1, 'acme/app', $2, $3, 'exemplar', 'important', 'issue', 'a.go', 'RIGHT', 1, 1, $4, 'm', 'posted')`,
				run, pr, rule, fmt.Sprint(rule, pr))
		}
	}
	stats, err := st.UpstreamRuleStats(ctx, 90)
	if err != nil || len(stats) != 2 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if s := stats[0]; s.RuleID != "no-raw-sql" || s.ReceivedPrs != 25 || s.ReceivedViolations != 25 || s.OtherPrs != 25 || s.AfterPrs != 50 || s.BeforePrs != 0 {
		t.Errorf("no-raw-sql = %+v", s)
	}
	if s := stats[1]; s.RuleID != "prefer-arena" || s.ReceivedViolations != 0 || s.OtherViolations != 25 {
		t.Errorf("prefer-arena = %+v", s)
	}

	exported := func(id string) config.Rule {
		return config.Rule{ID: id, Status: schema.StatusActive, Owner: "@platform/backend", Export: &schema.Export{Agents: true, Summary: "s"}, Scope: "global", VersionSHA: "v"}
	}
	snap := &config.Snapshot{Rules: []config.Rule{exported("no-raw-sql"), exported("prefer-arena")}}
	repo := &fakeRepo{}
	now := func() time.Time { return time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) }
	if _, err := (Runner{Store: st, Repo: repo, Now: now}).Run(ctx, snap); err != nil || len(repo.bodies) != 1 {
		t.Fatalf("digest: %v, %d", err, len(repo.bodies))
	}
	body := repo.bodies[0]
	if !strings.Contains(body, "**Exported summaries to rewrite**") ||
		!strings.Contains(body, "- `no-raw-sql`: PRs whose agent read the export, against the rest: 1.00 violations per PR (25 PRs) against 1.00 (25 PRs)") ||
		strings.Contains(body, "- `prefer-arena`:") {
		t.Errorf("body:\n%s", body)
	}
}
