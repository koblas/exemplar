package pipeline

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gates"
)

// eligible is a Stage 1 input that passes every deterministic gate.
func eligible() gates.Stage1Input {
	return gates.Stage1Input{
		Complete: true, Coverage: 1, BugLaneCovered: true, GlobalAutoApprove: true, RepoAutoApprove: true,
		PathPolicy: true, AuthorPolicy: true, NotFork: true, WithinSize: true, Files: 2, ChangedLines: 40,
	}
}

func result(in gates.Stage1Input, policy *gates.PolicyResult, s2 *gates.Stage2Result) *engine.Result {
	s1 := gates.EvaluateStage1(in)
	s1.Policy = policy
	return &engine.Result{Stage1: s1, Stage2: s2}
}

func stage2(in gates.Stage2Input) *gates.Stage2Result {
	s := gates.EvaluateStage2(in)
	return &s
}

func TestVerdict(t *testing.T) {
	score := func(v float64) *gates.MergeSafetyResult { return &gates.MergeSafetyResult{Score: v} }
	pass := gates.Stage2Input{
		Owners: &gates.OwnersResult{Pass: true}, BranchRules: &gates.BranchRulesResult{Pass: true},
		MergeSafety: score(0.9), Categorization: &gates.Stage2Categorization{ReviewConfidence: 0.95},
	}
	tests := []struct {
		name     string
		res      *engine.Result
		approved bool
		title    string
		contains []string
		absent   []string
	}{
		{
			name: "approved", res: result(eligible(), nil, stage2(pass)), approved: true,
			title: "approved", contains: []string{"No human review needed", "Every auto-approve gate passed"},
		},
		{
			// Suggestions do not keep a run from being clean; the posted ones
			// are named beside the approval (FR-A13).
			name: "approved with suggestions", res: func() *engine.Result {
				r := result(func() gates.Stage1Input { in := eligible(); in.CandidatesAll = 3; return in }(), nil, stage2(pass))
				r.Findings = []engine.Finding{{State: engine.StatePosted}, {State: engine.StatePosted}, {State: engine.StateDropped}}
				return r
			}(), approved: true,
			title: "approved", contains: []string{"No human review needed", "2 suggestion(s) posted beside the approval"},
		},
		{
			name: "global off hides the gates",
			res: result(func() gates.Stage1Input {
				in := eligible()
				in.GlobalAutoApprove, in.RepoAutoApprove, in.Candidates = false, false, 3
				return in
			}(), nil, nil),
			title: "human review needed", contains: []string{"auto-approve is off in global.yaml"},
			absent: []string{"candidate", "repository"},
		},
		{
			name: "repo off",
			res: result(func() gates.Stage1Input {
				in := eligible()
				in.RepoAutoApprove = false
				return in
			}(), nil, nil),
			contains: []string{"Not auto-approved because auto-approve is off for this repository."},
		},
		{
			name: "policy with detail, three reasons at most",
			res: result(func() gates.Stage1Input {
				in := eligible()
				in.PathPolicy, in.AuthorPolicy, in.WithinSize, in.Candidates = false, false, false, 1
				in.Files, in.ChangedLines = 14, 1200
				return in
			}(), &gates.PolicyResult{
				Denylisted: []string{".github/workflows/ci.yml", "go.mod"}, AuthorKind: gates.AuthorBot,
			}, nil),
			contains: []string{
				"2 changed path(s) are denylisted (`.github/workflows/ci.yml`, …)",
				"the bot or agent author is not allowlisted for these paths",
				"over the size limits (14 files, 1200 changed lines)",
			},
			absent: []string{"candidate"},
		},
		{
			name: "findings and coverage",
			res: result(func() gates.Stage1Input {
				in := eligible()
				in.Candidates, in.Coverage, in.Unreviewed = 2, 0.8, 1
				return in
			}(), nil, nil),
			contains: []string{
				"1 changed file(s) were excluded from review",
				"the review raised 2 blocking or important finding(s), counting any the verifier refuted",
				"coverage was 80%, not 100%",
			},
		},
		{
			name: "stage 2 owners and branch rules",
			res: result(eligible(), nil, stage2(gates.Stage2Input{
				Owners:      &gates.OwnersResult{NotApprovable: map[string][]string{"internal/auth/x.go": {"@acme/security"}}},
				BranchRules: &gates.BranchRulesResult{Reasons: []string{gates.ReasonStaleApprovalsKept}},
				MergeSafety: score(0.9), Categorization: &gates.Stage2Categorization{ReviewConfidence: 0.95},
			})),
			contains: []string{
				"internal/auth/x.go is owned by @acme/security, not in auto_approve.approvable_owners",
				"keep stale approvals on push",
			},
		},
		{
			name: "stage 2 merge safety and categorizer",
			res: result(eligible(), nil, stage2(gates.Stage2Input{
				Owners: &gates.OwnersResult{Pass: true}, BranchRules: &gates.BranchRulesResult{Pass: true},
				MergeSafety: score(0.55), Categorization: &gates.Stage2Categorization{ReviewConfidence: 0.6},
			})),
			contains: []string{
				"the merge-safety score 0.55 is below the bar 0.70",
				"did not rate the change low-risk (confidence 0.60, bar",
			},
		},
		{
			name: "categorizer not run",
			res: result(eligible(), nil, stage2(gates.Stage2Input{
				Owners: &gates.OwnersResult{Pass: true}, BranchRules: &gates.BranchRulesResult{Pass: true}, MergeSafety: score(0.9),
			})),
			contains: []string{"the two-model risk check did not run"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := verdict(tc.res)
			if v.Approved != tc.approved {
				t.Fatalf("approved = %v, want %v (%+v)", v.Approved, tc.approved, v)
			}
			if tc.title != "" && v.Title() != tc.title {
				t.Errorf("title = %q, want %q", v.Title(), tc.title)
			}
			if !tc.approved && (len(v.Reasons) == 0 || len(v.Reasons) > maxVerdictReasons) {
				t.Errorf("reasons = %v", v.Reasons)
			}
			line := v.Line()
			for _, want := range tc.contains {
				if !strings.Contains(line, want) {
					t.Errorf("line %q\n  lacks %q", line, want)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(line, bad) {
					t.Errorf("line %q\n  should not mention %q", line, bad)
				}
			}
		})
	}
}
