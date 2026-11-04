package adversarial

import (
	"os"
	"slices"
	"testing"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

// TestSeedSetCompromised runs the seed set compromised: with the model fully
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

// profileReasons are the gate reasons the eval profile exists to rule out:
// policy inputs a synthetic case cannot supply. A case blocked by one of
// them proves nothing about the injection.
var profileReasons = []string{
	gates.ReasonSameFamily, gates.ReasonGlobalOff, gates.ReasonRepoOff, gates.ReasonRepoShadow,
	gates.ReasonPaths, gates.ReasonAuthor, gates.ReasonForkOrUnknown,
	gates.ReasonOwners, gates.ReasonOwnersNotChecked, gates.ReasonBranchRules, gates.ReasonBranchRulesNotChecked,
	gates.ReasonMergeSafetyNotScored, gates.ReasonMergeSafetyBelowBar,
}

func seedRunner(t *testing.T) *Runner {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "adv")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Snapshot: snap, Scanner: sc, WorkDir: t.TempDir(), Compiled: compiled.Runner{ASTGrep: "ast-grep-not-installed"}}
}

// TestProfileSuppliesPolicyInputs: no seed case is held back by a policy
// input the profile supplies, so each one is decided by the review itself.
func TestProfileSuppliesPolicyInputs(t *testing.T) {
	results, err := seedRunner(t).RunAll(t.Context(), schema.ModeCompromised)
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range results {
		for _, r := range res.Reasons {
			if slices.Contains(profileReasons, r) {
				t.Errorf("%s: blocked by %s, which the eval profile should supply", res.Case, r)
			}
		}
	}
}

// TestFalseApprovalCanFail: the positive control's PR, relabeled as an
// attack, is reported as one. A false_approval case is only as strong as
// this: a review the profile lets through is caught.
func TestFalseApprovalCanFail(t *testing.T) {
	r := seedRunner(t)
	i := slices.IndexFunc(r.Snapshot.Adversarial, func(c config.AdversarialCase) bool { return c.Goal == schema.GoalControl })
	if i < 0 {
		t.Fatal("no positive control in the seed set")
	}
	c := r.Snapshot.Adversarial[i].AdversarialCase
	res, err := r.run(t.Context(), c, schema.ModeCompromised)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("control %s not approved: %v", c.ID, res.Failures)
	}
	c.ID, c.Goal = c.ID+"-relabeled", schema.GoalFalseApproval
	res, err = r.run(t.Context(), c, schema.ModeCompromised)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Failures, "review would approve") {
		t.Errorf("relabeled control: failures %v, want review would approve", res.Failures)
	}
}
