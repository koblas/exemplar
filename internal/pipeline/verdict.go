package pipeline

import (
	"fmt"
	"strings"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gates"
)

// Verdict is the answer to "who reviews this PR?", as the check run
// states it: approved with no human review needed, or routed to humans.
// Reasons are the gates that routed it, in plain words, from the
// code-measured traces in approval_decisions; never model text.
type Verdict struct {
	Approved bool
	// Suggestions counts the suggestion findings posted beside an
	// approval: they do not keep a run from being clean (FR-A13).
	Suggestions int
	// Reasons is empty when Approved. Otherwise the first reasons, most
	// decisive first: auto-approve off, policy, the review's outcome, then
	// the approval checks.
	Reasons []string
}

// maxVerdictReasons bounds the reasons on the check run; the audit page
// has them all.
const maxVerdictReasons = 3

// verdict derives the verdict from a finished review's gate traces.
func verdict(res *engine.Result) Verdict {
	if res.Decision() == gates.DecisionApproved {
		posted := 0
		for _, f := range res.Findings {
			if f.State == engine.StatePosted {
				posted++
			}
		}
		return Verdict{Approved: true, Suggestions: posted}
	}
	s1 := res.Stage1
	in := s1.Input
	var reasons []string
	add := func(r string) {
		if r != "" && len(reasons) < maxVerdictReasons {
			reasons = append(reasons, r)
		}
	}
	// Auto-approve off: say so and stop. Listing gate failures would
	// suggest the PR was ever a candidate.
	switch {
	case !in.GlobalAutoApprove:
		return Verdict{Reasons: []string{"auto-approve is off in global.yaml"}}
	case !in.RepoAutoApprove:
		return Verdict{Reasons: []string{"auto-approve is off for this repository"}}
	case in.RepoShadow:
		return Verdict{Reasons: []string{"the repository is in shadow mode"}}
	}
	// Policy: facts about the PR itself.
	if !in.PathPolicy {
		add(pathReason(s1.Policy))
	}
	if !in.AuthorPolicy {
		add(authorReason(s1.Policy))
	}
	if !in.NotFork {
		add("the head is on a fork, or its repository is unknown")
	}
	if !in.WithinSize {
		add(fmt.Sprintf("the change is over the size limits (%d files, %d changed lines)", in.Files, in.ChangedLines))
	}
	// The review's outcome.
	if in.Unreviewed > 0 {
		add(fmt.Sprintf("%d changed file(s) were excluded from review", in.Unreviewed))
	}
	if in.Candidates > 0 {
		add(fmt.Sprintf("the review raised %d blocking or important finding(s), counting any the verifier refuted", in.Candidates))
	}
	if !in.Complete {
		add("the review did not complete")
	}
	if in.Coverage < 1 {
		add(fmt.Sprintf("coverage was %.0f%%, not 100%%", in.Coverage*100))
	}
	if !in.BugLaneCovered {
		add("the bug lane did not run to full coverage")
	}
	if in.Degraded {
		add("the review ran degraded")
	}
	if in.SameFamilyVerification {
		add("a finding was verified by the generator's own model family")
	}
	// The approval checks, only reached when the gates above all passed.
	if s2 := res.Stage2; s2 != nil {
		if s2.Decision == gates.DecisionBlockedCompliance {
			add("a compliance repository needs two independent model families to categorise the change")
		}
		for _, r := range s2.Reasons {
			add(stage2Reason(r, s2))
		}
	}
	if len(reasons) == 0 {
		reasons = []string{"a gate failed; the audit page has the trace"}
	}
	return Verdict{Reasons: reasons}
}

func pathReason(p *gates.PolicyResult) string {
	if p == nil {
		return "changed paths are outside the auto-approve allow list"
	}
	switch {
	case len(p.Denylisted) > 0:
		return fmt.Sprintf("%d changed path(s) are denylisted (%s)", len(p.Denylisted), first(p.Denylisted))
	case len(p.NotAllowed) > 0:
		return fmt.Sprintf("%d changed path(s) are outside auto_approve.paths (%s)", len(p.NotAllowed), first(p.NotAllowed))
	case len(p.OutOfScope) > 0:
		return fmt.Sprintf("%d changed path(s) are outside the bot author's allowed paths (%s)", len(p.OutOfScope), first(p.OutOfScope))
	}
	return "changed paths are outside the auto-approve allow list"
}

func authorReason(p *gates.PolicyResult) string {
	if p == nil {
		return "the author is not auto-approvable"
	}
	switch p.AuthorKind {
	case gates.AuthorBot:
		return "the bot or agent author is not allowlisted for these paths"
	case gates.AuthorUnknown:
		return "the author could not be identified"
	}
	if len(p.Agents) > 0 {
		return "an agent co-author is not allowlisted for these paths"
	}
	return "the author is not in auto_approve.allowed_authors"
}

func stage2Reason(r string, s2 *gates.Stage2Result) string {
	switch r {
	case gates.ReasonOwnersNotChecked:
		return "CODEOWNERS could not be checked"
	case gates.ReasonOwners:
		if o := s2.Owners; o != nil {
			if len(o.Unowned) > 0 {
				return fmt.Sprintf("%d touched path(s) have no CODEOWNERS owner (%s)", len(o.Unowned), first(o.Unowned))
			}
			for path, owners := range o.NotApprovable {
				return fmt.Sprintf("%s is owned by %s, not in auto_approve.approvable_owners", path, strings.Join(owners, ", "))
			}
		}
		return "a touched path's CODEOWNERS owners are not all auto-approvable"
	case gates.ReasonBranchRulesNotChecked:
		return "the branch rules could not be checked"
	case gates.ReasonBranchRules:
		if b := s2.BranchRules; b != nil {
			for _, br := range b.Reasons {
				switch br {
				case gates.ReasonStaleApprovalsKept:
					return "the branch rules keep stale approvals on push, so Exemplar's approval could not be trusted"
				case gates.ReasonCodeOwnerReviewRequired:
					return "the branch rules require code-owner review (use merge_policy_check mode)"
				case gates.ReasonMergePolicyNotRequired:
					return "the branch rules do not require the exemplar/merge-policy check"
				case gates.ReasonComplianceApprovals:
					return "a compliance repository must require at least two approvals"
				}
			}
		}
		return "the branch rules cannot carry Exemplar's approval"
	case gates.ReasonMergeSafetyNotScored:
		return "the merge-safety score could not be measured"
	case gates.ReasonMergeSafetyBelowBar:
		if s2.MergeSafety != nil {
			return fmt.Sprintf("the merge-safety score %.2f is below the bar %.2f", *s2.MergeSafety, s2.MergeSafetyBar)
		}
		return "the merge-safety score is below the bar"
	case gates.ReasonCategorizationNotRun:
		return "the two-model risk check did not run"
	case gates.ReasonConfidenceBelowBar:
		if c := s2.Categorization; c != nil {
			if c.Why != "" {
				return "the two-model risk check could not approve: " + c.Why
			}
			return fmt.Sprintf("the two-model risk check did not rate the change low-risk (confidence %.2f, bar %.2f)", c.ReviewConfidence, s2.ReviewConfidenceBar)
		}
		return "the two-model risk check did not rate the change low-risk"
	}
	return ""
}

func first(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	s := "`" + paths[0] + "`"
	if len(paths) > 1 {
		s += ", …"
	}
	return s
}

// Title is the check run's title fragment: what a reader sees in the
// checks list before opening it.
func (v Verdict) Title() string {
	if v.Approved {
		return "approved"
	}
	return "human review needed"
}

// Line is the check run summary's first line.
func (v Verdict) Line() string {
	if v.Approved {
		s := "**No human review needed.** Every auto-approve gate passed; Exemplar approves this head."
		if v.Suggestions > 0 {
			s += fmt.Sprintf(" %d suggestion(s) posted beside the approval.", v.Suggestions)
		}
		return s
	}
	return "**Human review needed.** Not auto-approved because " + strings.Join(v.Reasons, "; ") + "."
}
