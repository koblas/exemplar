package teach

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

func TestExcluded(t *testing.T) {
	tests := []struct{ in, want string }{
		{"id: r\nfile_patterns: [\"**/*.go\", \"!**/*_test.go\"]\nowner: x\n", "id: r\nfile_patterns: [\"**/*.go\", \"!**/*_test.go\", \"!tools/gen/**\"]\nowner: x\n"},
		{"file_patterns: []\n", "file_patterns: [\"!tools/gen/**\"]\n"},
		{"id: r\nfile_patterns:\n  - \"**/*.go\"\n  - src/**\nowner: x\n", "id: r\nfile_patterns:\n  - \"**/*.go\"\n  - src/**\n  - \"!tools/gen/**\"\nowner: x\n"},
		{"id: r\nfile_patterns:\n  - \"**/*.go\"", "id: r\nfile_patterns:\n  - \"**/*.go\"\n  - \"!tools/gen/**\""},
	}
	for _, tt := range tests {
		got, err := excluded([]byte(tt.in), "!tools/gen/**")
		if err != nil || string(got) != tt.want {
			t.Errorf("excluded(%q) = %q, %v", tt.in, got, err)
		}
	}
	if _, err := excluded([]byte("id: r\n"), "!x/**"); err == nil {
		t.Error("no file_patterns accepted")
	}
}

type fakeRepo struct {
	written map[string]string
	drafts  []string
	issues  []string
}

func (f *fakeRepo) DefaultBranch(context.Context) (string, string, error) { return "main", "abc", nil }
func (f *fakeRepo) EnsureBranch(context.Context, string, string) error    { return nil }
func (f *fakeRepo) GetFile(_ context.Context, _, path string) ([]byte, error) {
	if path != "rules/noisy.yaml" {
		return nil, errors.New("not found")
	}
	return []byte("id: noisy\nfile_patterns: [\"**/*.go\"]\n"), nil
}
func (f *fakeRepo) PutFile(_ context.Context, branch, path string, c []byte, _ string) error {
	f.written[branch+":"+path] = string(c)
	return nil
}
func (f *fakeRepo) OpenDraftPR(_ context.Context, _, _, title, body string) (string, error) {
	f.drafts = append(f.drafts, title+"\n"+body)
	return "https://github.com/acme/exemplar-config/pull/3", nil
}
func (f *fakeRepo) CreateIssue(_ context.Context, title, body string, _ []string) (gh.ConfigItem, error) {
	f.issues = append(f.issues, title+"\n"+body)
	return gh.ConfigItem{URL: "https://github.com/acme/exemplar-config/issues/4"}, nil
}

func TestTeach(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	var rid, run int64
	if err := st.Pool.QueryRow(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at)
		VALUES ('acme/app', 4, 'h', 'b', 'v', 'production', 'done', now()) RETURNING id`).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status)
		VALUES ($1, 'noisy', 'v', 'exemplar', '{a.go}', 'ok') RETURNING id`, rid).Scan(&run); err != nil {
		t.Fatal(err)
	}
	n := 0
	ev := func(rule, path, kind, actor, role, class, reason string) {
		t.Helper()
		n++
		var fid int64
		if err := st.Pool.QueryRow(ctx, `INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side, start_line, end_line,
			fingerprint, message, state, gh_comment_id) VALUES ($1, 'acme/app', 4, $2, 'exemplar', 'important', 'issue', $3, 'RIGHT', 1, 1, $4, 'm', 'posted', $5) RETURNING id`,
			run, rule, path, rule+string(rune('a'+n)), 7000+n).Scan(&fid); err != nil {
			t.Fatal(err)
		}
		var tc, rt *string
		if class != "" {
			tc = &class
		}
		if reason != "" {
			rt = &reason
		}
		if _, err := st.Pool.Exec(ctx, `INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via, teach_class, reason_text) VALUES ($1, $2, $3, $4, 1, 'slash', $5, $6)`,
			fid, kind, actor, role, tc, rt); err != nil {
			t.Fatal(err)
		}
	}
	// noisy: three "not important" under tools/gen (one with a scope
	// reason), one elsewhere; two exception reasons (not enough).
	ev("noisy", "tools/gen/a.go", "fb_not_important", "alice", "author", "", "")
	ev("noisy", "tools/gen/b.go", "fb_not_important", "bob", "author", "scope", "this is generated code")
	ev("noisy", "tools/gen/c/d.go", "fb_wrong", "carol", "reviewer", "scope", "generated, should not be reviewed")
	ev("noisy", "src/x.go", "fb_not_important", "dan", "author", "", "")
	ev("noisy", "src/y.go", "fb_wrong", "erin", "author", "exception", "this is deliberate")
	ev("noisy", "src/z.go", "fb_wrong", "fay", "author", "exception", "on purpose")
	// strict: three disagreements, and one rule_wrong from its owner.
	ev("strict", "a/b.go", "fb_disagree", "gus", "author", "", "")
	ev("strict", "a/c.go", "fb_disagree", "hal", "reviewer", "noise", "meh")
	ev("strict", "a/d.go", "fb_disagree", "ida", "author", "", "")
	ev("strict", "a/e.go", "fb_wrong", "olga", "owner", "rule_wrong", "superseded by RFC 12")
	// An observer counts for nothing.
	ev("strict", "q/r.go", "fb_wrong", "zed", "observer", "exception", "x")

	rule := func(id string) config.Rule {
		return config.Rule{Rule: schema.Rule{ID: id, Status: schema.StatusActive, Owner: "@platform/backend", FilePatterns: []string{"**/*.go"}},
			Path: "rules/" + id + ".yaml", Scope: "global", VersionSHA: "0123456789abcdef"}
	}
	snap := &config.Snapshot{Rules: []config.Rule{rule("noisy"), rule("strict")}}
	repo := &fakeRepo{written: map[string]string{}}
	ps, err := Runner{Store: st, Repo: repo}.Run(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Proposal{}
	for _, p := range ps {
		got[p.RuleID+"/"+p.Class] = p
	}
	if len(ps) != 3 || got["noisy/scope"].Action != "pr" || got["noisy/scope"].Exclude != "!tools/gen/**" || got["noisy/scope"].Signals != 3 ||
		got["strict/disagree"].Action != "issue" || got["strict/rule_wrong"].Action != "issue" || !got["strict/rule_wrong"].Owner {
		t.Fatalf("proposals = %+v", ps)
	}
	if repo.written["exemplar/teach/noisy/scope/0123456789ab:rules/noisy.yaml"] != "id: noisy\nfile_patterns: [\"**/*.go\", \"!tools/gen/**\"]\n" {
		t.Errorf("written = %v", repo.written)
	}
	all := strings.Join(append(repo.drafts, repo.issues...), "\n")
	for _, secret := range []string{"generated code", "deliberate", "RFC 12", "meh"} {
		if strings.Contains(all, secret) {
			t.Errorf("a proposal quotes a person's reason %q", secret)
		}
	}
	if !strings.Contains(all, "https://github.com/acme/app/pull/4#discussion_r") {
		t.Errorf("no evidence links: %s", all)
	}
	// Proposed once per rule version and class.
	if ps, err := (Runner{Store: st, Repo: repo}).Run(ctx, snap); err != nil || len(ps) != 0 {
		t.Errorf("again = %+v, %v", ps, err)
	}
}
