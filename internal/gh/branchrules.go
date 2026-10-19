package gh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/go-github/v88/github"

	"github.com/koblas/exemplar/internal/gates"
)

// BranchRules reads the merge requirements on repo's branch for the
// auto-approve branch-rules lookup, combining classic branch protection
// with every active ruleset that applies (GitHub enforces all of them, so
// the strictest value of each wins). It needs a FlavorGate token:
// Administration read for classic protection. An unprotected branch is
// "no rules", not an error; anything that could not be read is an error,
// and the gate fails closed on errors.
func BranchRules(ctx context.Context, c *github.Client, repo, branch string) (gates.BranchRules, error) {
	var br gates.BranchRules
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || branch == "" {
		return br, fmt.Errorf("%w: branch rules need a repo and branch, got %q", ErrInvalidRepo, repo)
	}
	addChecks := func(cs ...string) {
		for _, ctxName := range cs {
			if ctxName != "" && !slices.Contains(br.RequiredChecks, ctxName) {
				br.RequiredChecks = append(br.RequiredChecks, ctxName)
			}
		}
	}

	prot, _, err := c.Repositories.GetBranchProtection(ctx, owner, name, branch)
	switch {
	case errors.Is(err, github.ErrBranchNotProtected):
	case err != nil:
		return br, restErr(err, fmt.Sprintf("branch protection of %s@%s", repo, branch))
	default:
		br.Sources = append(br.Sources, "branch_protection")
		if r := prot.RequiredPullRequestReviews; r != nil {
			addReviews(&br, r.RequiredApprovingReviewCount, r.RequireCodeOwnerReviews, r.RequireLastPushApproval, r.DismissStaleReviews)
		}
		if s := prot.RequiredStatusChecks; s != nil {
			if s.Contexts != nil {
				addChecks(*s.Contexts...)
			}
			if s.Checks != nil {
				for _, ch := range *s.Checks {
					addChecks(ch.Context)
				}
			}
		}
	}

	opts := &github.ListOptions{PerPage: 100}
	for {
		rules, resp, err := c.Repositories.ListRulesForBranch(ctx, owner, name, branch, opts)
		if err != nil {
			return br, restErr(err, fmt.Sprintf("rulesets of %s@%s", repo, branch))
		}
		if rules != nil {
			source := func(m github.BranchRuleMetadata) {
				s := "ruleset:" + strconv.FormatInt(m.RulesetID, 10)
				if !slices.Contains(br.Sources, s) {
					br.Sources = append(br.Sources, s)
				}
			}
			for _, r := range rules.PullRequest {
				source(r.BranchRuleMetadata)
				p := r.Parameters
				addReviews(&br, p.RequiredApprovingReviewCount, p.RequireCodeOwnerReview, p.RequireLastPushApproval, p.DismissStaleReviewsOnPush)
			}
			for _, r := range rules.RequiredStatusChecks {
				source(r.BranchRuleMetadata)
				for _, ch := range r.Parameters.RequiredStatusChecks {
					addChecks(ch.Context)
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	slices.Sort(br.RequiredChecks)
	return br, nil
}

// addReviews merges one source's pull request review requirements into br,
// keeping the strictest of each.
func addReviews(br *gates.BranchRules, approvals int, codeOwner, lastPush, dismissStale bool) {
	br.RequiredApprovals = max(br.RequiredApprovals, approvals)
	br.CodeOwnerReview = br.CodeOwnerReview || codeOwner
	br.LastPushApproval = br.LastPushApproval || lastPush
	br.DismissStaleReviews = br.DismissStaleReviews || dismissStale
}
