package gates

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/codeowners"
)

// merge_policy_check mode (spec §6.13): the repo requires the
// exemplar/merge-policy check instead of counting Exemplar's APPROVE.
// The check passes when every auto-approve gate passed on the head, or
// when people approved it the way a code-owner review requirement would
// ask. Repos keep a break-glass bypass actor on the ruleset for it.

// PolicyReview is one PR review, as GitHub lists it.
type PolicyReview struct {
	User     string
	UserType string // "User", "Bot"
	State    string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	CommitID string
}

// MergePolicyResult is the human side of the check.
type MergePolicyResult struct {
	Pass      bool
	Approvers []string
	// Missing are the owner sets with no approval yet, in path order.
	Missing []string
}

// MemberFunc reports whether login is an active member of an @org/team.
type MemberFunc func(ctx context.Context, team, login string) (bool, error)

// HumanMergePolicy decides the check from people's reviews of head:
//   - an approver is a person other than the author whose latest review
//     (comments aside) is an approval of head itself, so a push needs a
//     new approval;
//   - every touched path with CODEOWNERS owners needs an approver among
//     them (a user, or a member of a team; an email owner cannot be
//     matched);
//   - a PR touching no owned path needs one approver.
func HumanMergePolicy(ctx context.Context, touched []string, co codeowners.File, author, head string, reviews []PolicyReview, member MemberFunc) (MergePolicyResult, error) {
	latest := map[string]PolicyReview{}
	var order []string
	for _, r := range reviews {
		if r.State == "COMMENTED" || r.User == "" {
			continue
		}
		u := strings.ToLower(r.User)
		if _, seen := latest[u]; !seen {
			order = append(order, u)
		}
		latest[u] = r
	}
	var res MergePolicyResult
	for _, u := range order {
		r := latest[u]
		if r.State == "APPROVED" && r.CommitID == head && strings.EqualFold(r.UserType, "User") && !strings.EqualFold(u, author) {
			res.Approvers = append(res.Approvers, u)
		}
	}
	if len(res.Approvers) == 0 {
		res.Missing = []string{"an approval of this commit"}
		return res, nil
	}
	satisfied := map[string]bool{}
	for _, p := range touched {
		owners := co.Owners(p)
		if len(owners) == 0 {
			continue
		}
		key := strings.Join(owners, " ")
		if ok, seen := satisfied[key]; seen {
			if !ok && !slices.Contains(res.Missing, key) {
				res.Missing = append(res.Missing, key)
			}
			continue
		}
		ok, err := anyOwner(ctx, owners, res.Approvers, member)
		if err != nil {
			return res, err
		}
		satisfied[key] = ok
		if !ok {
			res.Missing = append(res.Missing, key)
		}
	}
	res.Pass = len(res.Missing) == 0
	return res, nil
}

func anyOwner(ctx context.Context, owners, approvers []string, member MemberFunc) (bool, error) {
	for _, o := range owners {
		if !strings.HasPrefix(o, "@") {
			continue // an email: GitHub maps it to an account we cannot see
		}
		name := strings.ToLower(strings.TrimPrefix(o, "@"))
		if !strings.Contains(name, "/") {
			if slices.Contains(approvers, name) {
				return true, nil
			}
			continue
		}
		for _, a := range approvers {
			ok, err := member(ctx, o, a)
			if err != nil {
				return false, fmt.Errorf("gates: membership of %s in %s: %w", a, o, err)
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}
