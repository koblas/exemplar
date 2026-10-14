package pipeline

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

const diff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,1 +1,2 @@
 package a
+var x = 1
`

func TestPlanClosures(t *testing.T) {
	d, err := scope.Parse(strings.NewReader(diff))
	if err != nil {
		t.Fatal(err)
	}
	rule := func(id, status string) config.Rule {
		r := config.Rule{}
		r.ID, r.Status = id, status
		return r
	}
	p := &engine.Prepared{
		PR: engine.PRInfo{HeadSHA: "abcdef1234"}, Diff: d, Files: []string{"a.go"},
		Effective: &config.Effective{Rules: []config.Rule{rule("on", schema.StatusActive), rule("broken", schema.StatusActive), rule("off", "killed"), rule("quiet", "shadow")}},
	}
	res := &engine.Result{
		Runs: []engine.RunResult{
			{RuleID: "on", Status: engine.RunOK, Coverage: 1, ScopedFiles: []string{"a.go"}},
			{RuleID: "broken", Status: engine.RunError, ScopedFiles: []string{"a.go"}},
		},
		Findings: []engine.Finding{
			{Fingerprint: "still"},
			{Fingerprint: "refuted", Suppressed: engine.SuppressRefuted},
		},
	}
	open := func(id int64, rule, path, fp, head string) OpenFinding {
		return OpenFinding{ID: id, RuleID: rule, Path: path, Fingerprint: fp, HeadSha: head}
	}
	got := PlanClosures([]OpenFinding{
		open(1, "on", "a.go", "gone", "old"),        // addressed
		open(2, "on", "a.go", "still", "old"),       // holds
		open(3, "on", "a.go", "refuted", "old"),     // now refuted: addressed
		open(4, "off", "a.go", "x", "old"),          // killed rule: retired
		open(5, "quiet", "a.go", "x", "old"),        // shadow rule: retired
		open(6, "removed", "a.go", "x", "old"),      // rule no longer configured: retired
		open(7, "on", "b.go", "x", "old"),           // file left the PR: outdated
		open(8, "broken", "a.go", "x", "old"),       // rule failed this time: leave alone
		open(9, "on", "a.go", "gone", "abcdef1234"), // this head (a retry): leave alone
		open(10, engine.SecretRuleID, "a.go", "s", "old"),
	}, p, res)
	want := map[int64]string{1: ClosedAddressed, 3: ClosedAddressed, 4: ClosedRetired, 5: ClosedRetired, 6: ClosedRetired, 7: ClosedOutdated}
	if len(got) != len(want) {
		t.Fatalf("closures = %+v", got)
	}
	for _, c := range got {
		if want[c.Finding.ID] != c.Reason {
			t.Errorf("finding %d: %s, want %q", c.Finding.ID, c.Reason, want[c.Finding.ID])
		}
		if c.Reason == ClosedAddressed && c.Reply != "Addressed in abcdef1." {
			t.Errorf("reply = %q", c.Reply)
		}
	}
	// An incomplete review closes nothing.
	res.Incomplete = "providers down"
	if got := PlanClosures([]OpenFinding{open(1, "on", "a.go", "gone", "old")}, p, res); len(got) != 0 {
		t.Errorf("incomplete closed %+v", got)
	}
}
