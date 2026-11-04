package adversarial

import (
	"bytes"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

// TestShippedSetCompromised runs the shipped set compromised on a config
// with no rules/, no evals/, no repos/ and no bug_lane: with the model
// fully controlled by each injection, no attack achieves its goal, and
// every positive control is approved.
func TestShippedSetCompromised(t *testing.T) {
	results := runAll(t, minimalTree(t))
	ran := map[string]map[string]bool{}
	controls := 0
	for _, res := range results {
		if res.Source != SourceShipped {
			t.Errorf("%s: source %s in a config with no evals/", res.Case, res.Source)
		}
		if !res.Passed {
			t.Errorf("%s: %v", res.Case, res.Failures)
		}
		if res.Pending != "" {
			continue
		}
		if res.Goal == schema.GoalControl {
			controls++
		}
		if ran[res.Vector] == nil {
			ran[res.Vector] = map[string]bool{}
		}
		ran[res.Vector][res.Goal] = true
	}
	if controls == 0 {
		t.Error("no positive control ran")
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

// TestShippedSetIgnoresOrgRules is a metamorphic relation: what the
// organization does to its own rule layer (drop the starter rule, extend
// the preset and disable or shadow the rule the cases name) leaves every
// shipped case's result unchanged. Global settings are not part of the
// relation: the bug lane's classes change coverage.
func TestShippedSetIgnoresOrgRules(t *testing.T) {
	type outcome struct {
		Passed   bool
		Verdict  string
		Coverage float64
		Failures string
	}
	outcomes := func(t *testing.T, fsys fstest.MapFS) map[string]outcome {
		t.Helper()
		out := map[string]outcome{}
		for _, res := range runAll(t, fsys) {
			if res.Source == SourceShipped {
				out[res.Case] = outcome{res.Passed, res.Verdict, res.Coverage, strings.Join(res.Failures, "; ")}
			}
		}
		return out
	}
	base := outcomes(t, exampleTree(t))
	variants := map[string]func(fstest.MapFS){
		// The config's own cases name the starter rule, so they go with it.
		"starter rule removed": func(f fstest.MapFS) {
			for p := range f {
				if strings.HasPrefix(p, "rules/reliability/propagate-context") || strings.HasPrefix(p, config.AdversarialDir+"/") {
					delete(f, p)
				}
			}
		},
		"preset rule disabled": func(f fstest.MapFS) {
			appendGlobal(f, "extends: [exemplar:recommended]\ndisabled_rules: [exemplar.propagate-context]\n")
		},
		"preset rule shadowed": func(f fstest.MapFS) {
			appendGlobal(f, "extends: [exemplar:recommended]\nrule_settings:\n  exemplar.propagate-context: { status: shadow }\n")
		},
	}
	for name, edit := range variants {
		t.Run(name, func(t *testing.T) {
			fsys := exampleTree(t)
			edit(fsys)
			got := outcomes(t, fsys)
			if len(got) != len(base) {
				t.Errorf("%d shipped results, want %d", len(got), len(base))
			}
			for id, want := range base {
				if got[id] != want {
					t.Errorf("%s: got %+v, want %+v", id, got[id], want)
				}
			}
		})
	}
}

func runAll(t *testing.T, fsys fstest.MapFS) []Result {
	t.Helper()
	snap, err := config.LoadSnapshot(fsys, "adv")
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
	return results
}

// exampleTree is the example config as a mutable in-memory tree.
func exampleTree(t *testing.T) fstest.MapFS {
	t.Helper()
	root := "../../testdata/exemplar-config"
	out := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS(root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(os.DirFS(root), p)
		out[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// minimalTree keeps only global.yaml, without its bug_lane block, and
// platform/.
func minimalTree(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	for p, f := range exampleTree(t) {
		if p == config.GlobalPath || strings.HasPrefix(p, "platform/") {
			out[p] = f
		}
	}
	global := regexp.MustCompile(`(?ms)^bug_lane:\n(?:[ #].*?\n|\n)*`).ReplaceAll(out[config.GlobalPath].Data, nil)
	if bytes.Contains(global, []byte("bug_lane:")) {
		t.Fatal("bug_lane block not removed from global.yaml")
	}
	out[config.GlobalPath] = &fstest.MapFile{Data: global}
	return out
}

func appendGlobal(fsys fstest.MapFS, lines string) {
	data := slices.Clone(fsys[config.GlobalPath].Data)
	fsys[config.GlobalPath] = &fstest.MapFile{Data: append(data, lines...)}
}

// TestCheckDetectsAttacks makes sure the checker is not vacuous.
func TestCheckDetectsAttacks(t *testing.T) {
	snap, err := config.LoadSnapshot(os.DirFS("../../testdata/exemplar-config"), "adv")
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
	snap, err := config.LoadSnapshot(os.DirFS("../../testdata/exemplar-config"), "adv")
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
	c := r.Snapshot.Adversarial[i]
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
