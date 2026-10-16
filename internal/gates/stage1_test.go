package gates

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

// TestStage1Invariants is a property test: any candidate
// finding, coverage below 1.0, degradation, incompleteness or same-family
// verification forces ineligible, whatever else is true; and a run is
// eligible exactly when every check holds.
func TestStage1Invariants(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		in := Stage1Input{
			Complete:               r.IntN(2) == 0,
			Coverage:               []float64{0, 0.5, 0.99, 1}[r.IntN(4)],
			Candidates:             r.IntN(3),
			Degraded:               r.IntN(2) == 0,
			SameFamilyVerification: r.IntN(2) == 0,
			BugLaneCovered:         r.IntN(4) != 0,
			GlobalAutoApprove:      r.IntN(2) == 0,
			RepoAutoApprove:        r.IntN(2) == 0,
			RepoShadow:             r.IntN(4) == 0,
			PathPolicy:             r.IntN(4) != 0,
			AuthorPolicy:           r.IntN(4) != 0,
			NotFork:                r.IntN(4) != 0,
			WithinSize:             r.IntN(4) != 0,
		}
		res := EvaluateStage1(in)
		forced := !in.Complete || in.Coverage < 1 || in.Candidates > 0 || in.Degraded || in.SameFamilyVerification
		if forced && res.Eligible {
			t.Fatalf("eligible with a forcing condition: %+v", in)
		}
		all := !forced && !in.RepoShadow && in.BugLaneCovered && in.GlobalAutoApprove && in.RepoAutoApprove && in.PathPolicy && in.AuthorPolicy && in.NotFork && in.WithinSize
		if res.Eligible != all {
			t.Fatalf("eligible = %v with every check %v: %+v", res.Eligible, all, in)
		}
		check := func(cond bool, reason string) {
			if cond != slices.Contains(res.Reasons, reason) {
				t.Fatalf("reason %s present=%v, condition=%v: %+v", reason, !cond, cond, in)
			}
		}
		check(!in.Complete, ReasonIncomplete)
		check(in.Coverage < 1, ReasonCoverage)
		check(in.Candidates > 0, ReasonCandidates)
		check(in.SameFamilyVerification, ReasonSameFamily)
		check(!in.BugLaneCovered, ReasonBugLane)
		check(in.RepoShadow, ReasonRepoShadow)
		check(!in.PathPolicy, ReasonPaths)
		check(!in.AuthorPolicy, ReasonAuthor)
		check(!in.NotFork, ReasonForkOrUnknown)
		check(!in.WithinSize, ReasonSize)
	}
}

// TestStage1InputHasNoModelText guards the trust boundary structurally: the
// input type may not grow string fields, which is where model text would
// leak in (messages, verdicts, explanations).
func TestStage1InputHasNoModelText(t *testing.T) {
	typ := reflect.TypeOf(Stage1Input{})
	for f := range typ.Fields() {
		switch f.Type.Kind() {
		case reflect.Bool, reflect.Int, reflect.Float64:
		default:
			t.Errorf("Stage1Input.%s has type %s; only code-measured numbers and flags are allowed", f.Name, f.Type)
		}
	}
}
