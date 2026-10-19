package gates

import "slices"

// Approval modes (repos/<org>/<repo>.yaml approval_mode).
const (
	ApprovalNative      = "native_approve"
	ApprovalMergePolicy = "merge_policy_check"
)

// MergePolicyCheck is the status check merge_policy_check repos require.
const MergePolicyCheck = "exemplar/merge-policy"

// Branch-rule reasons for the auto-approve approval checks.
const (
	ReasonBranchRulesUnknown       = "branch_rules_unknown"
	ReasonComplianceApprovals      = "compliance_repo_below_two_approvals"
	ReasonCodeOwnerReviewRequired  = "code_owner_review_required"
	ReasonStaleApprovalsKept       = "stale_approvals_not_dismissed_on_push"
	ReasonMergePolicyNotRequired   = "merge_policy_check_not_required"
	ReasonUnknownApprovalMode      = "unknown_approval_mode"
	NoteHumanApprovalStillRequired = "human_approval_still_required"
)

// BranchRules are the merge requirements on a PR's base branch, combined
// from classic branch protection and every active ruleset: the strictest
// value of each wins.
type BranchRules struct {
	RequiredApprovals int  `json:"required_approvals"`
	CodeOwnerReview   bool `json:"code_owner_review"`
	LastPushApproval  bool `json:"last_push_approval"`
	// DismissStaleReviews is set when a push dismisses earlier approvals
	// (classic protection or a ruleset's dismiss_stale_reviews_on_push).
	DismissStaleReviews bool     `json:"dismiss_stale_reviews"`
	RequiredChecks      []string `json:"required_checks,omitempty"`
	// Sources names where the rules came from ("branch_protection",
	// "ruleset:<id>"), for the trace.
	Sources []string `json:"sources,omitempty"`
}

// BranchRulesResult is the auto-approve branch-rules lookup trace.
type BranchRulesResult struct {
	Pass       bool         `json:"pass"`
	Reasons    []string     `json:"reasons,omitempty"`
	Notes      []string     `json:"notes,omitempty"`
	Mode       string       `json:"mode"`
	Compliance bool         `json:"compliance"`
	Rules      *BranchRules `json:"rules,omitempty"`
	// LookupError is why the rules could not be read, when they could not.
	LookupError string `json:"lookup_error,omitempty"`
	// BotApprovals is how many required approvals Exemplar's approval
	// can count as; HumanApprovals is how many must still come from people.
	BotApprovals   int `json:"bot_approvals"`
	HumanApprovals int `json:"human_approvals"`
}

// CheckBranchRules is the auto-approve branch-rules lookup. It
// decides whether Exemplar's approval means what the repo's mode says:
//   - rules that could not be read fail closed (nil rules);
//   - a compliance repo must keep at least two required approvals, of
//     which the bot is at most maxBotApprovals;
//   - native_approve (the default) cannot satisfy a code-owner review
//     requirement, since an App is never a code owner; such repos belong in
//     merge_policy_check mode;
//   - when the approval counts toward the merge, native_approve needs the
//     branch to dismiss stale approvals on push: Exemplar dismisses its own
//     approval only when the next review job runs, and an approval must
//     never carry forward to a commit nobody reviewed;
//   - merge_policy_check is pointless unless exemplar/merge-policy is a
//     required check.
//
// Required approvals beyond the bot's do not fail the gate: the approval
// is still one of them, and the trace records that people must add the rest.
func CheckBranchRules(repo, mode string, rules *BranchRules) BranchRulesResult {
	if mode == "" {
		mode = ApprovalNative
	}
	res := BranchRulesResult{Mode: mode, Compliance: IsComplianceRepo(repo), Rules: rules}
	if rules == nil {
		res.Reasons = []string{ReasonBranchRulesUnknown}
		return res
	}
	switch mode {
	case ApprovalNative:
		if rules.CodeOwnerReview {
			res.Reasons = append(res.Reasons, ReasonCodeOwnerReviewRequired)
		}
		if rules.RequiredApprovals > 0 && !rules.DismissStaleReviews {
			res.Reasons = append(res.Reasons, ReasonStaleApprovalsKept)
		}
		res.BotApprovals = min(maxBotApprovals, rules.RequiredApprovals)
	case ApprovalMergePolicy:
		if !slices.Contains(rules.RequiredChecks, MergePolicyCheck) {
			res.Reasons = append(res.Reasons, ReasonMergePolicyNotRequired)
		}
	default:
		res.Reasons = append(res.Reasons, ReasonUnknownApprovalMode)
	}
	if res.Compliance && rules.RequiredApprovals < 2 {
		res.Reasons = append(res.Reasons, ReasonComplianceApprovals)
	}
	res.HumanApprovals = max(0, rules.RequiredApprovals-res.BotApprovals)
	if res.HumanApprovals > 0 {
		res.Notes = append(res.Notes, NoteHumanApprovalStillRequired)
	}
	res.Pass = len(res.Reasons) == 0
	return res
}
