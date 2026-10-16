package gh

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-github/v88/github"
)

// MergeState is what GitHub says about a PR's merge requirements.
type MergeState struct {
	// ReviewDecision is APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or
	// empty when the base branch requires no reviews.
	ReviewDecision string `json:"review_decision"`
	// MergeStateStatus is GitHub's overall status (CLEAN, BLOCKED, ...).
	MergeStateStatus string `json:"merge_state_status"`
}

// ApprovalProbe is the approval probe's result for one sandbox PR: whether a
// native APPROVE from the review app counts toward the base branch's required
// approvals. Repos where it does not use merge_policy_check mode.
type ApprovalProbe struct {
	Repo      string     `json:"repo"`
	PR        int        `json:"pr"`
	Before    MergeState `json:"before"`
	After     MergeState `json:"after"`
	Counted   bool       `json:"counted"`
	Dismissed bool       `json:"dismissed"`
	Mode      string     `json:"recommended_approval_mode"`
}

// ErrAlreadyApproved means the probe PR already had a counting approval, so
// the app's approval cannot be measured.
var ErrAlreadyApproved = errors.New("gh: probe PR is already approved; use a PR with no approvals")

// ProbeApproval runs the approval probe on a sandbox PR. It submits an APPROVE
// review, waits up to wait for GitHub to recompute the review decision, then
// dismisses the approval so the sandbox PR is left as it was. Never run it on
// a production repo.
//
//nolint:nonamedreturns // the deferred dismissal records res.Dismissed on every return path
func (p *Poster) ProbeApproval(ctx context.Context, pr PR, wait time.Duration) (res ApprovalProbe, err error) {
	res = ApprovalProbe{Repo: pr.Owner + "/" + pr.Repo, PR: pr.Number}
	before, err := p.mergeState(ctx, pr)
	if err != nil {
		return res, err
	}
	res.Before = before
	if before.ReviewDecision == "APPROVED" {
		return res, ErrAlreadyApproved
	}

	review, _, err := p.gh.PullRequests.CreateReview(ctx, pr.Owner, pr.Repo, pr.Number, &github.PullRequestReviewRequest{
		CommitID: new(pr.HeadSHA),
		Body:     new("Exemplar T-0.3 approval probe. This approval is dismissed automatically."),
		Event:    new("APPROVE"),
	})
	if err != nil {
		return res, fmt.Errorf("gh: submit probe approval: %w", err)
	}
	defer func() {
		// Always try to leave the sandbox PR unapproved, even on error.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _, derr := p.gh.PullRequests.DismissReview(dctx, pr.Owner, pr.Repo, pr.Number, review.GetID(),
			&github.PullRequestReviewDismissalRequest{Message: new("T-0.3 probe complete")})
		res.Dismissed = derr == nil
	}()

	deadline := time.Now().Add(wait)
	for {
		after, err := p.mergeState(ctx, pr)
		if err != nil {
			return res, err
		}
		res.After = after
		if after.ReviewDecision == "APPROVED" || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("gh: approval probe: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	res.Counted = res.After.ReviewDecision == "APPROVED"
	res.Mode = "merge_policy_check"
	if res.Counted {
		res.Mode = "native_approve"
	}
	return res, nil
}

const mergeStateQuery = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) { reviewDecision mergeStateStatus }
  }
}`

func (p *Poster) mergeState(ctx context.Context, pr PR) (MergeState, error) {
	body := map[string]any{
		"query":     mergeStateQuery,
		"variables": map[string]any{"owner": pr.Owner, "repo": pr.Repo, "number": pr.Number},
	}
	req, err := p.gh.NewRequest(ctx, "POST", "graphql", body)
	if err != nil {
		return MergeState{}, fmt.Errorf("gh: build merge state query: %w", err)
	}
	var out struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					ReviewDecision   *string `json:"reviewDecision"`
					MergeStateStatus string  `json:"mergeStateStatus"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := p.gh.Do(req, &out); err != nil {
		return MergeState{}, fmt.Errorf("gh: query merge state: %w", err)
	}
	if len(out.Errors) > 0 {
		return MergeState{}, fmt.Errorf("gh: query merge state: %w: %s", errGraphQL, out.Errors[0].Message)
	}
	prs := out.Data.Repository.PullRequest
	if prs == nil {
		return MergeState{}, fmt.Errorf("%w: %s/%s#%d", ErrNotFound, pr.Owner, pr.Repo, pr.Number)
	}
	ms := MergeState{MergeStateStatus: prs.MergeStateStatus}
	if prs.ReviewDecision != nil {
		ms.ReviewDecision = *prs.ReviewDecision
	}
	return ms, nil
}
