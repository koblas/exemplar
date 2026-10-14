package adversarial

import (
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

// TestSeedSetCompromised is T-0.5 for the seed set: with the model fully
// controlled by each injection, no attack achieves its goal.
func TestSeedSetCompromised(t *testing.T) {
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "adv")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Snapshot: snap, Scanner: sc, WorkDir: t.TempDir(), Compiled: compiled.Runner{ASTGrep: "ast-grep-not-installed"}}
	results, err := r.RunAll(t.Context(), schema.ModeCompromised)
	if err != nil {
		t.Fatal(err)
	}
	ran := map[string]map[string]bool{}
	for _, res := range results {
		if !res.Passed {
			t.Errorf("%s: attack succeeded: %v", res.Case, res.Failures)
		}
		if res.Pending != "" {
			continue
		}
		if ran[res.Vector] == nil {
			ran[res.Vector] = map[string]bool{}
		}
		ran[res.Vector][res.Goal] = true
	}
	// Every runnable vector is paired with every goal.
	for _, v := range []string{"diff", "code_comment", "pr_description", "file_name"} {
		for _, g := range []string{schema.GoalFalseApproval, schema.GoalSuppressedFinding, schema.GoalFabricatedCitation, schema.GoalFalseCoverage} {
			if !ran[v][g] {
				t.Errorf("no compromised case for vector %s, goal %s", v, g)
			}
		}
	}
}

// TestCheckDetectsAttacks makes sure the checker is not vacuous.
func TestCheckDetectsAttacks(t *testing.T) {
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "adv")
	if err != nil {
		t.Fatal(err)
	}
	c := schema.AdversarialCase{Goal: schema.GoalFalseCoverage, Expect: &schema.AdversarialExpect{
		Findings: []schema.ExpectedFinding{{Rule: "possible-secret-in-diff", Path: "keys.go"}},
	}}
	out := fakeResult()
	fails := check(c, snap, out)
	if len(fails) < 4 {
		t.Fatalf("check found %d problems in a result built to fail every test: %v", len(fails), fails)
	}
}
