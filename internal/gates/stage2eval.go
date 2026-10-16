package gates

import "math"

// DefaultMinMergeSafety is the auto-approve merge_safety_score bar.
// A repo's auto_approve.min_merge_safety_score can only raise it.
const DefaultMinMergeSafety = 0.7

// Decisions beyond approved and ineligible (approval_decisions.decision).
const (
	// DecisionBlockedCompliance: a compliance repo whose providers cannot
	// give two independent judgments can never auto-approve, which
	// is intended; the decision says so instead of "ineligible".
	DecisionBlockedCompliance = "blocked_compliance"
)

// Reasons from the auto-approve approval checks.
const (
	ReasonOwnersNotChecked      = "codeowners_not_checked"
	ReasonOwners                = "codeowners"
	ReasonBranchRulesNotChecked = "branch_rules_not_checked"
	ReasonBranchRules           = "branch_rules"
	ReasonMergeSafetyNotScored  = "merge_safety_not_scored"
	ReasonMergeSafetyBelowBar   = "merge_safety_below_bar"
	ReasonCategorizationNotRun  = "categorization_not_run"
	ReasonConfidenceBelowBar    = "review_confidence_below_bar"
)

// MergeSafetyBar is the merge_safety_score a PR must reach: the default,
// or a repo's stricter value. Config can raise a bar but never lower it.
func MergeSafetyBar(configured float64) float64 { return bar(DefaultMinMergeSafety, configured) }

// ReviewConfidenceBar is the review_confidence a PR must reach, likewise.
func ReviewConfidenceBar(configured float64) float64 {
	return bar(DefaultMinReviewConfidence, configured)
}

func bar(def, configured float64) float64 {
	if math.IsNaN(configured) {
		return def
	}
	return math.Max(def, configured)
}

// Stage2Input is everything the approval checks combine. A nil part was not
// measured, and the checks fail closed on it. The bars are configured values
// (0 for unset); EvaluateStage2 applies the defaults.
type Stage2Input struct {
	Repo           string
	Owners         *OwnersResult
	BranchRules    *BranchRulesResult
	MergeSafety    *MergeSafetyResult
	Categorization *Stage2Categorization

	MinMergeSafety      float64
	MinReviewConfidence float64
}

// Stage2Result is the trace stored in approval_decisions.stage2.
type Stage2Result struct {
	Pass     bool     `json:"pass"`
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`

	Owners         *OwnersResult         `json:"codeowners"`
	BranchRules    *BranchRulesResult    `json:"branch_rules"`
	MergeSafety    *float64              `json:"merge_safety_score"`
	Categorization *Stage2Categorization `json:"categorization"`

	MergeSafetyBar      float64 `json:"merge_safety_bar"`
	ReviewConfidenceBar float64 `json:"review_confidence_bar"`
}

// EvaluateStage2 combines the auto-approve approval checks. All must hold: the
// CODEOWNERS lookup, the branch-rules lookup, merge_safety_score ≥ its bar and
// review_confidence ≥ its bar. It only means anything after the deterministic
// gates (EvaluateStage1) passed; Decide combines the two. A compliance repo
// whose categorizer pair is not independent is blocked_compliance rather than
// ineligible.
//
// The deterministic checks come first in the reasons, and a caller may
// skip the categorizer when they already fail (it costs model calls); a
// missing categorization then adds categorization_not_run.
func EvaluateStage2(in Stage2Input) Stage2Result {
	res := Stage2Result{
		Reasons: []string{}, Owners: in.Owners, BranchRules: in.BranchRules, Categorization: in.Categorization,
		MergeSafetyBar: MergeSafetyBar(in.MinMergeSafety), ReviewConfidenceBar: ReviewConfidenceBar(in.MinReviewConfidence),
	}
	switch {
	case in.Owners == nil:
		res.Reasons = append(res.Reasons, ReasonOwnersNotChecked)
	case !in.Owners.Pass:
		res.Reasons = append(res.Reasons, ReasonOwners)
	}
	switch {
	case in.BranchRules == nil:
		res.Reasons = append(res.Reasons, ReasonBranchRulesNotChecked)
	case !in.BranchRules.Pass:
		res.Reasons = append(res.Reasons, ReasonBranchRules)
	}
	if in.MergeSafety == nil {
		res.Reasons = append(res.Reasons, ReasonMergeSafetyNotScored)
	} else {
		s := in.MergeSafety.Score
		res.MergeSafety = &s
		if !(s >= res.MergeSafetyBar) { // NaN fails
			res.Reasons = append(res.Reasons, ReasonMergeSafetyBelowBar)
		}
	}
	switch c := in.Categorization; {
	case c == nil:
		res.Reasons = append(res.Reasons, ReasonCategorizationNotRun)
	case !(c.ReviewConfidence >= res.ReviewConfidenceBar):
		res.Reasons = append(res.Reasons, ReasonConfidenceBelowBar)
	}
	res.Pass = len(res.Reasons) == 0
	switch {
	case res.Pass:
		res.Decision = DecisionApproved
	case IsComplianceRepo(in.Repo) && in.Categorization != nil && in.Categorization.NotIndependent:
		res.Decision = DecisionBlockedCompliance
	default:
		res.Decision = DecisionIneligible
	}
	return res
}

// Decide is approval_decisions.decision for a run: approved only when
// the deterministic gates are eligible and the approval checks ran and
// passed. s2 is nil when they did not run, which they never do after a
// deterministic-gate failure.
func Decide(s1 Stage1Result, s2 *Stage2Result) string {
	switch {
	case !s1.Eligible:
		return DecisionIneligible
	case s2 == nil:
		return DecisionIneligible
	}
	return s2.Decision
}
