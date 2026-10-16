package engine

import (
	"context"

	"github.com/koblas/exemplar/internal/gates"
)

// evaluateStage1 is gates.EvaluateStage1. Tests replace it to reach the
// approval checks through Review without satisfying every gate.
var evaluateStage1 = gates.EvaluateStage1

// Decision is approval_decisions.decision for the run.
func (r *Result) Decision() string { return gates.Decide(r.Stage1, r.Stage2) }

// stage2 runs the auto-approve approval checks after the deterministic gates
// passed: the CODEOWNERS lookup (base commit), the branch-rules lookup
// (opt.BranchRules), the merge_safety_score (opt.MergeSafety) and, only if
// those pass, the two-model categorization, whose calls are appended to
// res.Runs. Every input that is missing or fails to load fails them.
func (e *Engine) stage2(ctx context.Context, p *Prepared, opt Options, meta CallMeta, res *Result) *gates.Stage2Result {
	in := gates.Stage2Input{Repo: p.PR.Repo, MergeSafety: opt.MergeSafety}
	if aa := p.Effective.RepoConfig.AutoApprove; aa != nil {
		in.MinMergeSafety, in.MinReviewConfidence = aa.MinMergeSafetyScore, aa.MinReviewConfidence
	}
	owners := p.CheckOwners()
	in.Owners = &owners

	var rules *gates.BranchRules
	lookupErr := ""
	if opt.BranchRules != nil {
		if br, err := opt.BranchRules(ctx); err != nil {
			lookupErr = err.Error()
		} else {
			rules = &br
		}
	}
	brr := gates.CheckBranchRules(p.PR.Repo, p.Effective.RepoConfig.ApprovalMode, rules)
	brr.LookupError = lookupErr
	in.BranchRules = &brr

	// The categorizer costs two model calls; skip it when the
	// deterministic checks have already failed.
	if pre := gates.EvaluateStage2(in); onlyCategorizationMissing(pre.Reasons) {
		cat, runs := e.categorize(ctx, p, meta)
		res.Runs = append(res.Runs, runs...)
		in.Categorization = &cat
	}
	out := gates.EvaluateStage2(in)
	return &out
}

func onlyCategorizationMissing(reasons []string) bool {
	return len(reasons) == 1 && reasons[0] == gates.ReasonCategorizationNotRun
}
