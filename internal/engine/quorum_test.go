package engine

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/buglane"
	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

const bugClaim = "resp is used when err is non-nil."

// verifierCalls counts verifier calls per quorum offset whose task
// carries claim.
func verifierCalls(tr *ScriptedTransport, claim string) map[int]int {
	out := map[int]int{}
	for _, c := range tr.Calls {
		if c.Stage == llm.StageVerifier && strings.Contains(string(c.Body), claim) {
			out[c.RouteOffset]++
		}
	}
	return out
}

// A bug-lane finding goes to verifier.quorum.n verifiers and posts only
// when k confirm (FR-V7); rule findings keep one verifier unless the rule
// sets verify: quorum.
func TestReviewBugQuorum(t *testing.T) {
	t.Parallel()
	nilDeref := fmt.Sprintf(`{"violations": [{"path": "main.go", "side": "RIGHT", "start_line": %d, "end_line": %d,
		"explanation": %q, "trigger": "the inventory host is unreachable", "impact": "the handler panics"}]}`, httpGetLine, httpGetLine, bugClaim)
	const ruleClaim = "http.Get ignores ctx"
	run := func(t *testing.T, voter func(int, string) (string, float64), mutate func(env, *Script)) (*Result, *ScriptedTransport) {
		t.Helper()
		e := setup(t, "Fetch inventory", "Adds a fetch.")
		e.prep.Effective.RepoConfig.BugLane = &schema.RepoBugLane{CriticalPaths: []string{"main.go"}}
		s := bugScript(e, nilDeref)
		s.Voter = voter
		if mutate != nil {
			mutate(e, &s)
		}
		eng, tr := newEngine(e, s)
		res, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval"})
		if err != nil {
			t.Fatal(err)
		}
		return res, tr
	}
	// votes answers the bug claim per offset; rule claims are confirmed.
	votes := func(byOffset map[int]string, imp map[int]float64) func(int, string) (string, float64) {
		return func(off int, task string) (string, float64) {
			if !strings.Contains(task, bugClaim) {
				return VerdictConfirmed, 0.9
			}
			return byOffset[off], imp[off]
		}
	}

	t.Run("two of three", func(t *testing.T) {
		t.Parallel()
		res, tr := run(t, votes(map[int]string{0: VerdictConfirmed, 1: VerdictRefuted, 2: VerdictConfirmed}, map[int]float64{0: 0.7, 1: 0.2, 2: 0.9}), nil)
		bf := findByRule(res.Findings, buglane.NilDeref)
		if len(bf) != 1 || bf[0].Suppressed != "" || bf[0].VerifyVerdict != VerdictConfirmed ||
			bf[0].VerifyQuorumN != 3 || bf[0].VerifyQuorumAgreed != 2 || bf[0].VerifyImportance == nil || *bf[0].VerifyImportance != 0.8 {
			t.Fatalf("bug finding = %+v", bf)
		}
		if got := verifierCalls(tr, bugClaim); got[0] != 1 || got[1] != 1 || got[2] != 1 {
			t.Errorf("bug verifier calls by offset = %v", got)
		}
		// The rule finding was verified once.
		if got := verifierCalls(tr, ruleClaim); got[0] != 1 || got[1]+got[2] != 0 {
			t.Errorf("rule verifier calls by offset = %v", got)
		}
		if cf := findByRule(res.Findings, "propagate-context"); len(cf) != 1 || cf[0].VerifyQuorumN != 0 {
			t.Errorf("rule finding = %+v", cf)
		}
	})

	t.Run("one of three", func(t *testing.T) {
		t.Parallel()
		res, _ := run(t, votes(map[int]string{0: VerdictUncertain, 1: VerdictRefuted, 2: VerdictConfirmed}, nil), nil)
		bf := findByRule(res.Findings, buglane.NilDeref)
		if len(bf) != 1 || bf[0].State != StateDropped || bf[0].Suppressed != SuppressQuorum ||
			bf[0].VerifyQuorumAgreed != 1 || bf[0].VerifyVerdict != VerdictUncertain {
			t.Fatalf("bug finding = %+v", bf)
		}
	})

	t.Run("rule opts in", func(t *testing.T) {
		t.Parallel()
		res, tr := run(t, votes(map[int]string{0: VerdictConfirmed, 1: VerdictConfirmed, 2: VerdictConfirmed}, nil), func(e env, _ *Script) {
			for i := range e.prep.Effective.Rules {
				if e.prep.Effective.Rules[i].ID == "propagate-context" {
					e.prep.Effective.Rules[i].Verify = "quorum"
				}
			}
		})
		if got := verifierCalls(tr, ruleClaim); got[0] != 1 || got[1] != 1 || got[2] != 1 {
			t.Errorf("rule verifier calls by offset = %v", got)
		}
		if cf := findByRule(res.Findings, "propagate-context"); len(cf) != 1 || cf[0].VerifyQuorumN != 3 || cf[0].VerifyQuorumAgreed != 3 {
			t.Errorf("rule finding = %+v", cf)
		}
	})

	t.Run("same family over the confirmers", func(t *testing.T) {
		t.Parallel()
		all := votes(map[int]string{0: VerdictConfirmed, 1: VerdictRefuted, 2: VerdictConfirmed}, nil)
		otherAt := func(at int) func(int) string {
			return func(off int) string {
				if off == at {
					return "other"
				}
				return "scripted"
			}
		}
		// The refuting verifier is another family; both confirmers share
		// the generator's.
		res, _ := run(t, all, func(_ env, s *Script) {
			s.VoterFamily = otherAt(1)
		})
		if bf := findByRule(res.Findings, buglane.NilDeref); len(bf) != 1 || !bf[0].VerifySameFamily {
			t.Errorf("same-family confirmers: %+v", bf)
		}
		// One confirmer from another family makes it independent.
		res, _ = run(t, all, func(_ env, s *Script) {
			s.VoterFamily = otherAt(2)
		})
		if bf := findByRule(res.Findings, buglane.NilDeref); len(bf) != 1 || bf[0].VerifySameFamily {
			t.Errorf("independent confirmer: %+v", bf)
		}
	})
}

// verify on its own: the quorum's extra calls are shed past the soft
// ceiling, and per-file batching holds for each verifier.
func TestVerifyQuorumShedAndBatching(t *testing.T) {
	t.Parallel()
	e := setup(t, "t", "")
	findings := func() []*Finding {
		fs := make([]*Finding, 0, 2)
		for _, line := range []int{httpGetLine, httpGetLine + 1} {
			fs = append(fs, &Finding{
				RuleID: buglane.NilDeref, Lane: LaneBug, Severity: schema.SeverityImportant, CCLabel: "issue (bug)", Message: "m", State: StateCandidate,
				Anchor: scope.Anchor{Path: "main.go", Side: scope.SideRight, StartLine: line, EndLine: line},
			})
		}
		return fs
	}
	all := func(*Finding) bool { return true }
	script := Script{Quote: func(path string, line int) string { return headLineNoT(e.prep, path, line) }}

	eng, tr := newEngine(e, script)
	fs := findings()
	out := eng.verify(t.Context(), e.prep, fs, nil, CallMeta{Caller: "eval"}, all, func() bool { return true })
	if !out.shed || len(tr.Calls) != 1 {
		t.Fatalf("shed = %v, calls %d", out.shed, len(tr.Calls))
	}
	for _, f := range fs {
		if f.Suppressed != SuppressQuorumShed || f.VerifyQuorumN != 3 || f.VerifyQuorumAgreed != 1 {
			t.Errorf("shed finding = %+v", f)
		}
	}
	// A candidate the votes it has already fail is a failed quorum.
	refute := script
	refute.Voter = func(int, string) (string, float64) { return VerdictRefuted, 0 }
	eng, _ = newEngine(e, refute)
	p := *e.prep
	eff := *p.Effective
	eff.Global.Verifier.Quorum = &schema.Quorum{N: 3, K: 3}
	p.Effective = &eff
	fs = findings()
	eng.verify(t.Context(), &p, fs, nil, CallMeta{Caller: "eval"}, all, func() bool { return true })
	if fs[0].Suppressed != SuppressQuorum {
		t.Errorf("already failed = %+v", fs[0])
	}

	// One candidate per call: each of the three verifiers makes two calls.
	eng, tr = newEngine(e, script)
	eff = *e.prep.Effective
	eff.Global.Verifier.MaxCandidatesPerCall = 1
	p.Effective = &eff
	fs = findings()
	out = eng.verify(t.Context(), &p, fs, nil, CallMeta{Caller: "eval"}, all, nil)
	offsets := map[int]int{}
	for _, c := range tr.Calls {
		offsets[c.RouteOffset]++
	}
	if out.shed || len(out.runs) != 6 || offsets[0] != 2 || offsets[1] != 2 || offsets[2] != 2 {
		t.Errorf("runs %d, calls by offset %v", len(out.runs), offsets)
	}
	for _, f := range fs {
		if f.State != StateCandidate || f.VerifyQuorumAgreed != 3 {
			t.Errorf("batched finding = %+v", f)
		}
	}
}

// Within a severity, a higher quorum share ranks first (§6.10).
func TestAssembleRanksByQuorumShare(t *testing.T) {
	t.Parallel()
	e := setup(t, "t", "")
	eng, _ := newEngine(e, Script{})
	mk := func(line, agreed int) *Finding {
		f := occurrences("propagate-context", schema.SeverityImportant, 1)[0]
		f.Anchor.StartLine, f.Anchor.EndLine = line, line
		if agreed > 0 {
			f.VerifyQuorumN, f.VerifyQuorumAgreed = 3, agreed
		}
		return f
	}
	res := &Result{}
	eng.assemble(t.Context(), e.prep, []*Finding{mk(8, 0), mk(9, 2), mk(10, 3)}, Options{ReviewID: 1}, dedupers{}, res)
	lines := make([]int, 0, len(res.Findings))
	for _, f := range res.Findings {
		lines = append(lines, f.Anchor.StartLine)
	}
	if !slices.Equal(lines, []int{10, 9, 8}) {
		t.Errorf("order = %v", lines)
	}
}

// quorumDecision over random vote vectors: confirmed exactly when at
// least k votes confirm with resolving evidence, importance the median of
// those votes.
func TestQuorumDecisionProperty(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(7, 21))
	verdicts := []string{VerdictConfirmed, VerdictRefuted, VerdictUncertain, ""}
	for range 2000 {
		n := 2 + r.IntN(5)
		k := 1 + r.IntN(n)
		votes := make([]vote, 1+r.IntN(n))
		var imps []float64
		for i := range votes {
			v := vote{answered: r.IntN(5) > 0, verdict: verdicts[r.IntN(len(verdicts))], evidenceOK: r.IntN(4) > 0, importance: float64(r.IntN(101)) / 100}
			votes[i] = v
			if v.verdict == VerdictConfirmed && v.evidenceOK {
				imps = append(imps, v.importance)
			}
		}
		agreed, confirmed, imp := quorumDecision(votes, k)
		if agreed != len(imps) || confirmed != (len(imps) >= k) {
			t.Fatalf("votes %+v k %d: agreed %d confirmed %v", votes, k, agreed, confirmed)
		}
		slices.Sort(imps)
		want := 0.0
		switch m := len(imps); {
		case m == 0:
		case m%2 == 1:
			want = imps[m/2]
		default:
			want = (imps[m/2-1] + imps[m/2]) / 2
		}
		if imp != want {
			t.Fatalf("votes %+v: importance %v, want %v", votes, imp, want)
		}
	}
}
