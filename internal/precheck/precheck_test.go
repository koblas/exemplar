package precheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

const repo = "exemplar-sandbox/playground"

func snapshot(t *testing.T, linters ...string) *config.Snapshot {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "x")
	if err != nil {
		t.Fatal(err)
	}
	rc := snap.Repos[repo]
	rc.Linters = linters
	snap.Repos[repo] = rc
	return snap
}

func all(string) bool { return true }

type fakeAnnotations map[string][]gh.Annotation // sha -> annotations

func (f fakeAnnotations) Annotations(_ context.Context, _, sha string, checks []string) ([]gh.Annotation, error) {
	anns, ok := f[sha]
	if !ok {
		return nil, gh.ErrNotFound
	}
	var out []gh.Annotation
	for _, a := range anns {
		for _, c := range checks {
			if strings.EqualFold(a.Check, c) {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

func pos(name, sha string, start, end int) evalrun.Case {
	return evalrun.Case{Name: name, Repo: repo, SHA: sha, Paths: []string{"svc/a.go"}, Expect: []schema.Expectation{{Path: "svc/a.go", Lines: []int{start, end}}}}
}

func TestLinterOverlap(t *testing.T) {
	anns := fakeAnnotations{
		"aaa": {{Check: "golangci-lint", Path: "svc/a.go", Start: 6, End: 6, Title: "noctx"}},
		"bbb": {{Check: "golangci-lint", Path: "svc/a.go", Start: 30, End: 30, Title: "errcheck"}, {Check: "unit-tests", Path: "svc/a.go", Start: 10, End: 10}},
	}
	cases := []evalrun.Case{
		pos("flagged", "aaa", 5, 7),
		pos("elsewhere", "bbb", 10, 12), // linter flags line 30; the non-linter check on line 10 does not count
		{Name: "negative", Repo: repo, SHA: "aaa", Expect: []schema.Expectation{}},
		{Name: "synthetic", Repo: repo, SHA: "aaa", Synthetic: true, Expect: []schema.Expectation{{Path: "svc/a.go", Lines: []int{6, 6}}}},
		pos("gone", "ccc", 1, 1),
	}
	rep, err := LinterOverlap(t.Context(), anns, snapshot(t, "golangci-lint"), all, "r", cases)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pass || rep.Checked != 2 || len(rep.Overlaps) != 1 || rep.Overlaps[0].Rule != "noctx" || rep.Overlaps[0].Case != "flagged" || len(rep.Skipped) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	// No linters configured: nothing to compare, and the check passes.
	rep, _ = LinterOverlap(t.Context(), anns, snapshot(t), all, "r", cases)
	if !rep.Pass || rep.Checked != 0 || !strings.Contains(strings.Join(rep.Skipped, ";"), "configures no linters") {
		t.Errorf("no linters = %+v", rep)
	}
	// Ineligible repos are not read.
	rep, _ = LinterOverlap(t.Context(), anns, snapshot(t, "golangci-lint"), func(string) bool { return false }, "r", cases)
	if rep.Checked != 0 {
		t.Errorf("read an ineligible repo: %+v", rep)
	}
}

type fakePRs struct {
	prs   []gh.PullRequest
	files [][]string
}

func (f fakePRs) MergedPRs(_ context.Context, r string, _ time.Time, limit int) ([]gh.PullRequest, [][]string, error) {
	if r != repo {
		return nil, nil, nil
	}
	n := min(limit, len(f.prs))
	return f.prs[:n], f.files[:n], nil
}

// fakeTrial reports n findings on PR n (two in the same file for n >= 4).
type fakeTrial struct{ runs int }

func (f *fakeTrial) RunPR(_ context.Context, _ *config.Snapshot, ids []string, pr gh.PullRequest) (playground.Trial, error) {
	f.runs++
	res := &engine.Result{Runs: []engine.RunResult{{RuleID: ids[0], CostUSD: 0.01}}}
	for i := range pr.Number {
		res.Findings = append(res.Findings, engine.Finding{
			RuleID: ids[0], State: engine.StateShadow,
			Anchor: scope.Anchor{Path: fmt.Sprintf("f%d.go", i%2), StartLine: i + 1, EndLine: i + 1},
		})
	}
	res.Findings = append(res.Findings, engine.Finding{RuleID: ids[0], State: engine.StateDropped, Anchor: scope.Anchor{Path: "x.go"}})
	return playground.Trial{Result: res}, nil
}

func TestVolume(t *testing.T) {
	snap := snapshot(t)
	var rule config.Rule
	for _, r := range snap.Rules {
		if r.ID == "propagate-context" { // **/*.go, not tests
			rule = r
		}
	}
	src := fakePRs{
		prs:   []gh.PullRequest{{Repo: repo, Number: 0}, {Repo: repo, Number: 1}, {Repo: repo, Number: 8}, {Repo: repo, Number: 2}},
		files: [][]string{{"svc/a.go"}, {"svc/b.go"}, {"svc/c.go"}, {"README.md"}},
	}
	tr := &fakeTrial{}
	rep, err := Volume(t.Context(), src, tr, snap, all, rule, VolumeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// PR 0: in scope, no findings. PR 1: 1 comment. PR 8: 8 findings, budget
	// per_file 3 over two files and per_pr 7 -> 6. PR 2: README only, not run.
	if rep.Sampled != 4 || rep.InScope != 3 || rep.Ran != 3 || tr.runs != 3 || rep.Affected != 2 || rep.Comments != 7 || rep.MaxPerPR != 6 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.ShareAffected != 0.5 || rep.CommentsPerPR != 1.75 || rep.CostUSD < 0.029 {
		t.Errorf("rates = %+v", rep)
	}

	// The run cap bounds model cost and is reported.
	rep, _ = Volume(t.Context(), src, &fakeTrial{}, snap, all, rule, VolumeConfig{MaxRuns: 1})
	if rep.Ran != 1 || len(rep.Notes) != 1 {
		t.Errorf("capped = %+v", rep)
	}
}

func TestExportChanges(t *testing.T) {
	base := snapshot(t)
	head := snapshot(t)
	for i, r := range head.Rules {
		switch r.ID {
		case "propagate-context":
			e := *r.Export
			e.Summary = "Pass ctx to every I/O call."
			head.Rules[i].Export = &e
		case "no-raw-sql-concat":
			head.Rules[i].Export = nil // removed
		}
	}
	got := ExportChanges(base, head)
	if len(got) != 2 || got[0].Rule != "no-raw-sql-concat" || got[0].After != nil || got[1].Rule != "propagate-context" || got[1].After.Summary != "Pass ctx to every I/O call." {
		t.Fatalf("changes = %+v", got)
	}
	if len(ExportChanges(base, snapshot(t))) != 0 {
		t.Error("identical configs report export changes")
	}
}

func TestCheckExportApproval(t *testing.T) {
	devex := map[string]bool{"dana": true, "ari": true}
	member := func(_ context.Context, _ string, login string) (bool, error) { return devex[login], nil }
	changes := []ExportChange{{Rule: "r"}}
	tests := []struct {
		name    string
		reviews []gh.Review
		pass    bool
		reason  string
	}{
		{"no reviews", nil, false, "need an approval"},
		{"owner only", []gh.Review{{User: "owen", State: "APPROVED", CommitID: "h2"}}, false, "need an approval"},
		{"devex on head", []gh.Review{{User: "owen", State: "APPROVED", CommitID: "h2"}, {User: "dana", State: "APPROVED", CommitID: "h2"}}, true, "approved by dana"},
		{"devex on an old commit", []gh.Review{{User: "dana", State: "APPROVED", CommitID: "h1"}}, false, "earlier commit"},
		{"approval then changes requested", []gh.Review{{User: "dana", State: "APPROVED", CommitID: "h2"}, {User: "dana", State: "CHANGES_REQUESTED", CommitID: "h2"}}, false, "need an approval"},
		{"approval then a comment", []gh.Review{{User: "dana", State: "APPROVED", CommitID: "h2"}, {User: "dana", State: "COMMENTED", CommitID: "h2"}}, true, "dana"},
		{"dismissed", []gh.Review{{User: "dana", State: "APPROVED", CommitID: "h2"}, {User: "dana", State: "DISMISSED", CommitID: "h2"}}, false, "need an approval"},
		{"author is devex", []gh.Review{{User: "ari", State: "APPROVED", CommitID: "h2"}}, false, "need an approval"},
		{"bot", []gh.Review{{User: "dana", UserType: "Bot", State: "APPROVED", CommitID: "h2"}}, false, "need an approval"},
	}
	for _, tt := range tests {
		got, err := CheckExportApproval(t.Context(), changes, ExportTeam, "ari", "h2", tt.reviews, member)
		if err != nil || got.Pass != tt.pass || !strings.Contains(got.Reason, tt.reason) {
			t.Errorf("%s: %+v, %v", tt.name, got, err)
		}
	}
	if got, _ := CheckExportApproval(t.Context(), nil, ExportTeam, "ari", "h2", nil, member); !got.Pass {
		t.Error("no export changes must pass")
	}
	failing := func(context.Context, string, string) (bool, error) { return false, errors.New("github down") }
	if _, err := CheckExportApproval(t.Context(), changes, ExportTeam, "ari", "h2", []gh.Review{{User: "dana", State: "APPROVED", CommitID: "h2"}}, failing); err == nil {
		t.Error("a failed membership check must be an error, not a pass")
	}
}
