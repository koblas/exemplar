package engine

import (
	"testing"

	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// occurrences builds n candidate findings of one active rule on main.go.
func occurrences(rule, severity string, n int) []*Finding {
	out := make([]*Finding, 0, n)
	for i := range n {
		out = append(out, &Finding{
			RuleID: rule, Lane: LaneExemplar, Severity: severity, Category: "Reliability", CCLabel: "issue", Message: "m",
			Anchor: scope.Anchor{Path: "main.go", Side: scope.SideRight, StartLine: 9 + i%3, EndLine: 9 + i%3},
			State:  StateCandidate,
		})
	}
	return out
}

func states(fs []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.State+"/"+f.Suppressed]++
	}
	return m
}

// More than posting.class_threshold occurrences of one rule post as one
// comment at the first; the group takes one budget slot and shares its
// leader's fate (FR-C9).
func TestAssembleGroupsByClass(t *testing.T) {
	t.Parallel()
	e := setup(t, "t", "")
	eng, _ := newEngine(e, Script{})
	run := func(fs []*Finding, opt Options, mutate func(*Prepared)) *Result {
		t.Helper()
		p := *e.prep
		eff := *p.Effective
		p.Effective = &eff
		if mutate != nil {
			mutate(&p)
		}
		res := &Result{}
		eng.assemble(t.Context(), &p, fs, opt, dedupers{}, res)
		return res
	}
	// Distinct lines, so exact-duplicate dedupe keeps them all.
	four := func() []*Finding {
		fs := occurrences("propagate-context", schema.SeverityImportant, 4)
		for i, f := range fs {
			f.Anchor.StartLine, f.Anchor.EndLine = 8+i, 8+i
		}
		return fs
	}

	res := run(four(), Options{ReviewID: 7}, nil)
	var leader *Finding
	keys := map[string]bool{}
	for i := range res.Findings {
		f := &res.Findings[i]
		keys[f.GroupKey] = true
		if len(f.Members) > 0 {
			leader = f
		}
	}
	if leader == nil || len(leader.Members) != 3 || len(keys) != 1 || res.Grouped != 3 || states(res.Findings)["posted/"] != 4 {
		t.Fatalf("grouped = %+v, keys %v, folded %d", res.Findings, keys, res.Grouped)
	}
	members := 0
	for _, f := range res.Findings {
		if f.GroupMember() {
			members++
		}
	}
	if leader.Anchor.StartLine != 8 || leader.GroupMember() || members != 3 {
		t.Errorf("leader = %+v, %d members", leader, members)
	}
	again := run(four(), Options{ReviewID: 7}, nil)
	if again.Findings[0].GroupKey != res.Findings[0].GroupKey {
		t.Error("group key not deterministic")
	}
	if other := run(four(), Options{ReviewID: 8}, nil); other.Findings[0].GroupKey == res.Findings[0].GroupKey {
		t.Error("group key ignores the review")
	}

	// At the threshold nothing is grouped.
	three := occurrences("propagate-context", schema.SeverityImportant, 3)
	if res := run(three, Options{ReviewID: 7}, nil); res.Grouped != 0 || res.Findings[0].GroupKey != "" {
		t.Errorf("three grouped: %+v", res.Findings)
	}

	// One budget slot: with per_pr 1, the group posts and a second rule's
	// finding is held back.
	extra := &Finding{
		RuleID: "wrap-errors-with-cause", Lane: LaneExemplar, Severity: schema.SeveritySuggestion, Category: "Maintainability",
		CCLabel: "suggestion", Message: "m", State: StateCandidate,
		Anchor: scope.Anchor{Path: "main.go", Side: scope.SideRight, StartLine: 3, EndLine: 3},
	}
	tight := func(p *Prepared) { p.Effective.Global.CommentBudget = schema.CommentBudget{PerPR: 1, PerFile: 1} }
	if res := run(append(four(), extra), Options{ReviewID: 7}, tight); states(res.Findings)["posted/"] != 4 || res.HeldBack != 1 {
		t.Errorf("budget = %v, held back %d", states(res.Findings), res.HeldBack)
	}
	// Past the round cap the whole group is summarized; in a shadow repo
	// the whole group is shadow.
	if res := run(four(), Options{ReviewID: 7, RoundCapped: true}, nil); states(res.Findings)["summarized/round_cap"] != 4 || res.Summarized != 4 {
		t.Errorf("round cap = %v", states(res.Findings))
	}
	shadow := func(p *Prepared) { p.Effective.RepoConfig.Shadow = true }
	if res := run(four(), Options{ReviewID: 7}, shadow); states(res.Findings)["shadow/repo_shadow"] != 4 {
		t.Errorf("shadow = %v", states(res.Findings))
	}
}
