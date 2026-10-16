package exports

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// fakeWriter is one repo: its default branch's files, the export branch,
// and at most one open PR.
type fakeWriter struct {
	main, branch map[string]string
	resets       int
	prOpen       bool
	prs, autos   int
	bodies       []string
}

func (f *fakeWriter) DefaultBranch(context.Context) (string, string, error) {
	return "main", "sha1", nil
}

func (f *fakeWriter) GetFile(_ context.Context, ref, path string) ([]byte, bool, error) {
	if ref != "main" {
		panic("read off the default branch: " + ref)
	}
	s, ok := f.main[path]
	return []byte(s), ok, nil
}

func (f *fakeWriter) ResetBranch(_ context.Context, branch, sha string) error {
	if branch != Branch || sha != "sha1" {
		panic("reset " + branch + " " + sha)
	}
	f.resets++
	f.branch = map[string]string{}
	return nil
}

func (f *fakeWriter) PutFile(_ context.Context, branch, path string, content []byte, _ string) error {
	if branch != Branch {
		panic("write to " + branch)
	}
	f.branch[path] = string(content)
	return nil
}

func (f *fakeWriter) OpenPRFor(context.Context, string) (gh.ConfigItem, string, bool, error) {
	return gh.ConfigItem{URL: "https://github.com/acme/app/pull/7"}, "node", f.prOpen, nil
}

func (f *fakeWriter) OpenPR(_ context.Context, branch, base, _, body string) (gh.ConfigItem, string, error) {
	if branch != Branch || base != "main" {
		panic("PR " + branch + " into " + base)
	}
	f.prs++
	f.prOpen = true
	f.bodies = append(f.bodies, body)
	return gh.ConfigItem{URL: "https://github.com/acme/app/pull/7"}, "node", nil
}
func (f *fakeWriter) EnableAutoMerge(context.Context, string) error { f.autos++; return nil }

// merge lands the export PR.
func (f *fakeWriter) merge() {
	maps.Copy(f.main, f.branch)
	f.prOpen = false
}

func TestRun(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	snap := &config.Snapshot{
		Version: "cfg1", Global: schema.GlobalConfig{Enabled: true},
		Repos: map[string]schema.RepoConfig{
			"acme/app":   {Enabled: true, Exports: &schema.RepoExports{Targets: []string{AgentsMD}, AutoMergeGenerated: true}},
			"acme/quiet": {Enabled: true}, // no exports
			"acme/off":   {Enabled: false, Exports: &schema.RepoExports{Targets: []string{AgentsMD}}},
		},
		Rules: []config.Rule{rule("no-raw-sql", "blocking", "Use placeholders, never string-built SQL.", "**/*.go")},
	}
	w := &fakeWriter{main: map[string]string{"AGENTS.md": "# App\n\nRun make test.\n"}}
	r := Runner{Store: st, Writer: func(_ context.Context, repo string) (Writer, error) {
		if repo != "acme/app" {
			t.Fatalf("writer for %s", repo)
		}
		return w, nil
	}}
	run := func() Outcome {
		t.Helper()
		outs, err := r.Run(ctx, snap)
		if err != nil || len(outs) != 1 {
			t.Fatalf("Run = %+v, %v", outs, err)
		}
		return outs[0]
	}
	behind := func() int32 {
		t.Helper()
		d, err := st.ExportDrift(ctx, 0)
		if err != nil || len(d) != 1 || d[0].Repo != "acme/app" {
			t.Fatalf("ExportDrift = %+v, %v", d, err)
		}
		return d[0].VersionsBehind
	}
	row := func() queries.Export {
		t.Helper()
		rows, err := st.GetExport(ctx, queries.GetExportParams{Repo: "acme/app", Path: "AGENTS.md"})
		if err != nil || len(rows) != 1 {
			t.Fatalf("GetExport = %+v, %v", rows, err)
		}
		return rows[0]
	}

	// First run: the block is appended after the team's text, in a new PR
	// set to auto-merge.
	o := run()
	got := w.branch["AGENTS.md"]
	if o.Action != "pr" || w.prs != 1 || w.autos != 1 || !strings.HasPrefix(got, "# App\n\nRun make test.\n\n"+Begin) ||
		!strings.Contains(got, "(`no-raw-sql`)") || !strings.Contains(w.bodies[0], "- `AGENTS.md`") {
		t.Fatalf("first run = %+v, file %q", o, got)
	}
	if e := row(); e.State != "proposed" || e.ConfigSha != "cfg1" || e.BaseHash != "" || !e.AutoMerged || e.PrUrl == nil {
		t.Fatalf("row = %+v", e)
	}
	if b := behind(); b != 1 { // never merged: the version proposed
		t.Fatalf("behind before merge = %d", b)
	}

	// Before the merge, the same rendering is not pushed again.
	if o := run(); o.Action != "pending" || w.prs != 1 || w.resets != 1 {
		t.Fatalf("rerun = %+v", o)
	}

	// Merged: the file is current, and nothing is written.
	w.merge()
	resets := w.resets
	if o := run(); o.Action != "current" || w.resets != resets {
		t.Fatalf("after merge = %+v", o)
	}
	if e := row(); e.State != "current" || e.CurrentSha != "cfg1" {
		t.Fatalf("row after merge = %+v", e)
	}
	if b := behind(); b != 0 {
		t.Fatalf("behind after merge = %d", b)
	}

	// A new rule: the block is replaced in place; the team's text stays.
	snap.Version = "cfg2"
	snap.Rules = append(snap.Rules, rule("propagate-context", "important", "Pass ctx to outbound calls.", "**/*.go"))
	if o := run(); o.Action != "pr" || w.prs != 2 {
		t.Fatalf("new rule = %+v", o)
	}
	if got := w.branch["AGENTS.md"]; strings.Count(got, Begin) != 1 || !strings.HasPrefix(got, "# App\n\nRun make test.\n\n"+Begin) || !strings.Contains(got, "propagate-context") {
		t.Fatalf("replaced = %q", got)
	}
	w.merge()

	// A team edit to their own text, then a rule change while a PR is
	// open, updates that PR rather than opening another.
	snap.Version = "cfg2b"
	snap.Rules[0].Export.Summary = "Use placeholders; never build SQL from strings."
	w.prOpen = true
	if o := run(); o.Action != "updated" || w.prs != 2 || !strings.Contains(w.branch["AGENTS.md"], "never build SQL") {
		t.Fatalf("update open PR = %+v", o)
	}
	w.merge()

	// A hand edit inside the markers: left alone and reported.
	w.main["AGENTS.md"] = strings.Replace(w.main["AGENTS.md"], "Pass ctx", "Always pass ctx", 1)
	snap.Version = "cfg3"
	snap.Rules = snap.Rules[:1]
	resets = w.resets
	if o := run(); o.Action != "current" || len(o.Edited) != 1 || w.resets != resets {
		t.Fatalf("hand edit = %+v", o)
	}
	if e := row(); e.State != "edited" {
		t.Fatalf("row after edit = %+v", e)
	}
	// Last current at cfg1; cfg2, cfg2b and cfg3 have been exported since.
	if b := behind(); b != 3 {
		t.Fatalf("behind after edit = %d", b)
	}
	if d, err := st.ExportDrift(ctx, 3); err != nil || len(d) != 1 || d[0].Edited != 1 {
		t.Fatalf("ExportDrift(3) = %+v, %v", d, err)
	}
	if d, err := st.ExportDrift(ctx, 4); err != nil || len(d) != 0 {
		t.Fatalf("ExportDrift(4) = %+v, %v", d, err)
	}

	// Denylisted: skipped before any writer is made.
	snap.Eligibility.Denylist = []schema.DenylistEntry{{Repo: "acme/app", Reason: "legal hold"}}
	if o := run(); o.Action != "skipped" || !strings.Contains(o.Reason, "legal hold") {
		t.Fatalf("denylisted = %+v", o)
	}
}

func TestRunDryRun(t *testing.T) {
	snap := &config.Snapshot{
		Global: schema.GlobalConfig{Enabled: true},
		Repos:  map[string]schema.RepoConfig{"acme/app": {Enabled: true, Exports: &schema.RepoExports{Targets: []string{AgentsMD, Copilot}}}},
		Rules:  []config.Rule{rule("no-raw-sql", "blocking", "Use placeholders.", "**/*.go"), rule("x", "blocking", "", "**/*.go")},
	}
	outs, err := Runner{}.Run(t.Context(), snap)
	if err != nil || len(outs) != 1 || outs[0].Action != "dry_run" || outs[0].Files != 2 {
		t.Fatalf("dry run = %+v, %v", outs, err)
	}
}
