// Package gates computes the auto-approve gates. The first set,
// EvaluateStage1, is strictly deterministic: its input contains only facts
// measured by code, never anything derived from model text.
//
// A run is ineligible on any candidate finding, a changed file excluded
// from review, coverage below 100%,
// degradation, an incomplete stage, same-family verification, a kill
// switch, or a failed policy check (paths, author, fork, size; see
// CheckPolicy). The later approval checks (EvaluateStage2) run only when
// it is eligible.
package gates

// Stage1Input holds only code-measured facts. Do not add fields derived
// from model output (messages, verdicts, importance, dedupe decisions).
type Stage1Input struct {
	// Complete is false for incomplete runs.
	Complete bool `json:"Complete"`
	// Coverage is completed in-scope rules ÷ in-scope rules, from the
	// harness tool-call log.
	Coverage float64 `json:"Coverage"`
	// Candidates counts the findings that keep a run from being clean:
	// those of blocking or important severity, and Security-category ones
	// of any severity, produced by every lane before verification. A
	// refuted finding still counts (system rule 6); suggestions do not
	// (FR-A13). Severity and category come from the rule, never a model.
	Candidates int `json:"Candidates"`
	// CandidatesAll counts every candidate finding, suggestions included.
	// It is recorded in the trace and gates nothing.
	CandidatesAll int `json:"CandidatesAll"`
	// Unreviewed counts changed files excluded from review (binary, over
	// 1 MB, removed, ignored by .gitattributes or .exemplarignore). No
	// lane saw them, so they cannot be called clean.
	Unreviewed int `json:"Unreviewed"`
	// Degraded is true when anything was shed, deferred or ran degraded.
	Degraded bool `json:"Degraded"`
	// SameFamilyVerification is true when any finding was verified by the
	// generator's model family.
	SameFamilyVerification bool `json:"SameFamilyVerification"`
	// BugLaneCovered is true when the bug lane (forced on auto-approve
	// candidates) ran to 100% coverage. A run without it is never
	// eligible.
	BugLaneCovered    bool `json:"BugLaneCovered"`
	GlobalAutoApprove bool `json:"GlobalAutoApprove"`
	RepoAutoApprove   bool `json:"RepoAutoApprove"`
	// RepoShadow is set for a repo reviewed in shadow: it may
	// never be approved.
	RepoShadow bool `json:"RepoShadow"`
	// The policy checks, from CheckPolicy: every path is in
	// auto_approve.paths and off the denylist; the author (and any agent
	// co-author) is allowed; the head is in the base repo; the PR is
	// within the size limits.
	PathPolicy   bool `json:"PathPolicy"`
	AuthorPolicy bool `json:"AuthorPolicy"`
	NotFork      bool `json:"NotFork"`
	WithinSize   bool `json:"WithinSize"`
	// Files and ChangedLines are the PR's size, for the trace.
	Files        int `json:"Files"`
	ChangedLines int `json:"ChangedLines"`
}

// Reasons a run is ineligible.
const (
	ReasonIncomplete    = "incomplete"
	ReasonCoverage      = "coverage_below_100"
	ReasonCandidates    = "candidate_findings"
	ReasonUnreviewed    = "changed_files_not_reviewed"
	ReasonDegraded      = "degraded"
	ReasonSameFamily    = "same_family_verification"
	ReasonBugLane       = "bug_lane_not_covered"
	ReasonGlobalOff     = "auto_approve_disabled_globally"
	ReasonRepoOff       = "auto_approve_disabled_for_repo"
	ReasonRepoShadow    = "repo_in_shadow"
	ReasonPaths         = "paths_not_auto_approvable"
	ReasonAuthor        = "author_not_auto_approvable"
	ReasonForkOrUnknown = "fork_or_unknown_head_repo"
	ReasonSize          = "over_size_limits"
	DecisionIneligible  = "ineligible"
	DecisionApproved    = "approved"
)

// Stage1Result is the gate trace stored in approval_decisions.stage1.
type Stage1Result struct {
	Eligible bool        `json:"eligible"`
	Reasons  []string    `json:"reasons"`
	Input    Stage1Input `json:"input"`
	// MergeSafety is the deterministic score and its terms,
	// logged in this trace. It gates in the later approval checks
	// (EvaluateStage2). It is kept out of
	// Stage1Input, whose fields stay plain numbers and flags. Nil when the
	// caller did not measure it.
	MergeSafety *MergeSafetyResult `json:"merge_safety,omitempty"`
	// Policy is CheckPolicy's trace: which paths or identities failed.
	// Nil when the caller did not run it (the run is then ineligible,
	// since the policy flags in Input are false).
	Policy *PolicyResult `json:"policy,omitempty"`
}

// EvaluateStage1 applies the deterministic gates. Every check must hold.
func EvaluateStage1(in Stage1Input) Stage1Result {
	var reasons []string
	if !in.Complete {
		reasons = append(reasons, ReasonIncomplete)
	}
	if in.Coverage < 1 {
		reasons = append(reasons, ReasonCoverage)
	}
	if in.Candidates > 0 {
		reasons = append(reasons, ReasonCandidates)
	}
	if in.Unreviewed > 0 {
		reasons = append(reasons, ReasonUnreviewed)
	}
	if in.Degraded {
		reasons = append(reasons, ReasonDegraded)
	}
	if in.SameFamilyVerification {
		reasons = append(reasons, ReasonSameFamily)
	}
	if !in.BugLaneCovered {
		reasons = append(reasons, ReasonBugLane)
	}
	if !in.GlobalAutoApprove {
		reasons = append(reasons, ReasonGlobalOff)
	}
	if !in.RepoAutoApprove {
		reasons = append(reasons, ReasonRepoOff)
	}
	if in.RepoShadow {
		reasons = append(reasons, ReasonRepoShadow)
	}
	if !in.PathPolicy {
		reasons = append(reasons, ReasonPaths)
	}
	if !in.AuthorPolicy {
		reasons = append(reasons, ReasonAuthor)
	}
	if !in.NotFork {
		reasons = append(reasons, ReasonForkOrUnknown)
	}
	if !in.WithinSize {
		reasons = append(reasons, ReasonSize)
	}
	return Stage1Result{Eligible: len(reasons) == 0, Reasons: reasons, Input: in}
}
