package gh

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

// PR identifies a pull request at one head commit.
type PR struct {
	Owner   string
	Repo    string
	Number  int
	HeadSHA string
}

// ParsePR builds a PR from "owner/name".
func ParsePR(fullName string, number int, headSHA string) (PR, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return PR{}, fmt.Errorf("%w: %q", ErrInvalidRepo, fullName)
	}
	return PR{Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA}, nil
}

// Comment is one inline review comment. Anchors must already have passed
// scope.ValidateAnchor; the poster does not adjust them.
type Comment struct {
	Path      string
	Side      string // RIGHT or LEFT
	StartLine int
	EndLine   int
	Body      string
}

// ReviewPost is what the poster submits for one Exemplar review.
type ReviewPost struct {
	// ReviewID is Exemplar's reviews.id; it goes into the hidden marker that
	// makes posting idempotent.
	ReviewID int64
	Summary  string
	Comments []Comment
}

// Poster submits reviews and check runs. It must be built from a
// FlavorPoster client. It posts COMMENT reviews and, only when every
// auto-approve gate passed, an APPROVE (Approve). Exemplar never blocks a
// merge, so there is no REQUEST_CHANGES path.
type Poster struct {
	gh *github.Client
}

// NewPoster wraps a poster-flavor client.
func NewPoster(c *github.Client) *Poster { return &Poster{gh: c} }

// Marker is the hidden tag that identifies an Exemplar review on a head.
func Marker(reviewID int64, headSHA string) string {
	return fmt.Sprintf("<!-- exemplar:review id=%d head=%s -->", reviewID, headSHA)
}

// PostReview submits one COMMENT review carrying every comment. If a review
// with this review's marker already exists on the head (a retried job), it
// returns that review's id and posted=false instead of posting again.
func (p *Poster) PostReview(ctx context.Context, pr PR, rv ReviewPost) (int64, bool, error) {
	marker := Marker(rv.ReviewID, pr.HeadSHA)
	existing, err := p.findReview(ctx, pr, marker)
	if err != nil {
		return 0, false, err
	}
	if existing != 0 {
		return existing, false, nil
	}

	req := &github.PullRequestReviewRequest{
		CommitID: new(pr.HeadSHA),
		Body:     new(strings.TrimSpace(rv.Summary) + "\n\n" + marker),
		Event:    new("COMMENT"),
	}
	for _, c := range rv.Comments {
		if c.StartLine < 1 || c.EndLine < c.StartLine {
			return 0, false, fmt.Errorf("%w %s:%d-%d", errCommentRange, c.Path, c.StartLine, c.EndLine)
		}
		dc := &github.DraftReviewComment{
			Path: new(c.Path),
			Body: new(c.Body),
			Side: new(c.Side),
			Line: new(c.EndLine),
		}
		if c.StartLine != c.EndLine {
			dc.StartLine = new(c.StartLine)
			dc.StartSide = new(c.Side)
		}
		req.Comments = append(req.Comments, dc)
	}
	review, _, err := p.gh.PullRequests.CreateReview(ctx, pr.Owner, pr.Repo, pr.Number, req)
	if err != nil {
		return 0, false, fmt.Errorf("gh: create review on %s/%s#%d: %w", pr.Owner, pr.Repo, pr.Number, err)
	}
	return review.GetID(), true, nil
}

// Posted reports whether review reviewID's post is on pr's head (its
// marker), whatever happened to the job that posted it.
func (p *Poster) Posted(ctx context.Context, pr PR, reviewID int64) (bool, error) {
	id, err := p.findReview(ctx, pr, Marker(reviewID, pr.HeadSHA))
	return id != 0, err
}

func (p *Poster) findReview(ctx context.Context, pr PR, marker string) (int64, error) {
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := p.gh.PullRequests.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number, opts)
		if err != nil {
			return 0, fmt.Errorf("gh: list reviews: %w", err)
		}
		for _, r := range reviews {
			if r.GetCommitID() == pr.HeadSHA && strings.Contains(r.GetBody(), marker) {
				return r.GetID(), nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return 0, nil
		}
		opts.Page = resp.NextPage
	}
}

// CheckName is the check run Exemplar reports on every reviewed head.
const CheckName = "exemplar"

// RerunAction is the check-run action identifier that re-enqueues an
// incomplete review.
const RerunAction = "rerun"

// ReviewCheckPrefix starts the external id of every review's check run
// ("review-<id>"), as opposed to the "not reviewed" check.
const ReviewCheckPrefix = "review-"

// StartCheck starts the in-progress check run for a head. A review check
// run another attempt left in progress on the head (a killed or retried
// attempt, or a duplicate review) is taken over and renamed to
// externalID rather than joined by a second one; the newest is taken and
// any older ones stay for the review that owns them to complete.
func (p *Poster) StartCheck(ctx context.Context, pr PR, externalID string) (int64, error) {
	open, err := p.openChecks(ctx, pr, func(id string) bool { return strings.HasPrefix(id, ReviewCheckPrefix) })
	if err != nil {
		return 0, err
	}
	if len(open) > 0 {
		id := slices.Max(open)
		if _, _, err := p.gh.Checks.UpdateCheckRun(ctx, pr.Owner, pr.Repo, id, github.UpdateCheckRunOptions{
			Name:       CheckName,
			ExternalID: new(externalID),
			Status:     new("in_progress"),
		}); err != nil {
			return 0, fmt.Errorf("gh: take over check run %d: %w", id, err)
		}
		return id, nil
	}
	run, _, err := p.gh.Checks.CreateCheckRun(ctx, pr.Owner, pr.Repo, github.CreateCheckRunOptions{
		Name:       CheckName,
		HeadSHA:    pr.HeadSHA,
		ExternalID: new(externalID),
		Status:     new("in_progress"),
		StartedAt:  &github.Timestamp{Time: time.Now()},
	})
	if err != nil {
		return 0, fmt.Errorf("gh: create check run: %w", err)
	}
	return run.GetID(), nil
}

// OpenChecks returns the ids of Exemplar's check runs on pr's head with
// externalID that have not completed.
func (p *Poster) OpenChecks(ctx context.Context, pr PR, externalID string) ([]int64, error) {
	return p.openChecks(ctx, pr, func(id string) bool { return id == externalID })
}

// openChecks returns the ids of Exemplar's check runs on pr's head whose
// external id matches and that have not completed.
func (p *Poster) openChecks(ctx context.Context, pr PR, match func(externalID string) bool) ([]int64, error) {
	opts := &github.ListCheckRunsOptions{CheckName: new(CheckName), Filter: new("all"), PerPage: 100}
	var out []int64
	for {
		runs, resp, err := p.gh.Checks.ListCheckRunsForRef(ctx, pr.Owner, pr.Repo, pr.HeadSHA, opts)
		if err != nil {
			return nil, fmt.Errorf("gh: check runs of %s/%s@%s: %w", pr.Owner, pr.Repo, pr.HeadSHA, err)
		}
		for _, r := range runs.CheckRuns {
			if match(r.GetExternalID()) && r.GetStatus() != "completed" {
				out = append(out, r.GetID())
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// CheckResult completes a check run.
type CheckResult struct {
	// Conclusion is "neutral" for every completed review: the check informs
	// and never fails the PR. Incomplete runs are also "neutral" and carry
	// the Re-run action.
	Conclusion string
	Title      string
	Summary    string
	Text       string
	Incomplete bool
}

// CompleteCheck finishes a check run.
func (p *Poster) CompleteCheck(ctx context.Context, pr PR, checkRunID int64, res CheckResult) error {
	if res.Conclusion == "" {
		return errNoConclusion
	}
	opts := github.UpdateCheckRunOptions{
		Name:        CheckName,
		Status:      new("completed"),
		Conclusion:  new(res.Conclusion),
		CompletedAt: &github.Timestamp{Time: time.Now()},
		Output: &github.CheckRunOutput{
			Title:   new(res.Title),
			Summary: new(res.Summary),
			Text:    new(res.Text),
		},
	}
	if res.Incomplete {
		opts.Actions = []*github.CheckRunAction{{
			Label:       "Re-run",
			Description: "Re-run the Exemplar review",
			Identifier:  RerunAction,
		}}
	}
	if _, _, err := p.gh.Checks.UpdateCheckRun(ctx, pr.Owner, pr.Repo, checkRunID, opts); err != nil {
		return fmt.Errorf("gh: complete check run: %w", err)
	}
	return nil
}

// MergePolicyCheckName is the check merge_policy_check repos require
// instead of counting Exemplar's APPROVE.
const MergePolicyCheckName = "exemplar/merge-policy"

// SetMergePolicy reports exemplar/merge-policy on the head: completed with
// success when pass, otherwise in progress with what it waits for, so the
// repo's required check holds the merge until people approve. The repo
// chose this check in place of an approval requirement, so it is the one
// way Exemplar's result can hold a merge; Exemplar's own review check
// stays neutral. Each call adds a run; GitHub uses the latest.
func (p *Poster) SetMergePolicy(ctx context.Context, pr PR, pass bool, title, summary string) error {
	opts := github.CreateCheckRunOptions{
		Name:    MergePolicyCheckName,
		HeadSHA: pr.HeadSHA,
		Output:  &github.CheckRunOutput{Title: new(title), Summary: new(summary)},
	}
	if pass {
		opts.Status, opts.Conclusion = new("completed"), new("success")
		opts.CompletedAt = &github.Timestamp{Time: time.Now()}
	} else {
		opts.Status = new("in_progress")
		opts.StartedAt = &github.Timestamp{Time: time.Now()}
	}
	if _, _, err := p.gh.Checks.CreateCheckRun(ctx, pr.Owner, pr.Repo, opts); err != nil {
		return fmt.Errorf("gh: set %s: %w", MergePolicyCheckName, err)
	}
	return nil
}

// ApproveMarker identifies an Exemplar approval on a head.
func ApproveMarker(reviewID int64, headSHA string) string {
	return fmt.Sprintf("<!-- exemplar:approve id=%d head=%s -->", reviewID, headSHA)
}

const approvePrefix = "<!-- exemplar:approve "

// Approve submits Exemplar's native APPROVE on the head: the caller
// must only call it when every auto-approve gate passed. It
// is idempotent per review, like PostReview.
func (p *Poster) Approve(ctx context.Context, pr PR, reviewID int64, summary string) (int64, bool, error) {
	marker := ApproveMarker(reviewID, pr.HeadSHA)
	existing, err := p.findReview(ctx, pr, marker)
	if err != nil || existing != 0 {
		return existing, false, err
	}
	review, _, err := p.gh.PullRequests.CreateReview(ctx, pr.Owner, pr.Repo, pr.Number, &github.PullRequestReviewRequest{
		CommitID: new(pr.HeadSHA),
		Body:     new(strings.TrimSpace(summary) + "\n\n" + marker),
		Event:    new("APPROVE"),
	})
	if err != nil {
		return 0, false, fmt.Errorf("gh: approve %s/%s#%d: %w", pr.Owner, pr.Repo, pr.Number, err)
	}
	return review.GetID(), true, nil
}

// DismissApprovals dismisses every standing Exemplar approval on an
// earlier head of the PR (an approval never carries forward to a
// new push). Only reviews that carry Exemplar's approve marker and come
// from a bot account are touched, so a human's approval is never
// dismissed; one on pr.HeadSHA (a retried review) is kept. It returns how
// many were dismissed.
func (p *Poster) DismissApprovals(ctx context.Context, pr PR, message string) (int, error) {
	n := 0
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := p.gh.PullRequests.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number, opts)
		if err != nil {
			return n, fmt.Errorf("gh: list reviews on %s/%s#%d: %w", pr.Owner, pr.Repo, pr.Number, err)
		}
		for _, r := range reviews {
			if r.GetState() != "APPROVED" || r.GetUser().GetType() != "Bot" || !strings.Contains(r.GetBody(), approvePrefix) ||
				r.GetCommitID() == pr.HeadSHA {
				continue
			}
			if _, _, err := p.gh.PullRequests.DismissReview(ctx, pr.Owner, pr.Repo, pr.Number, r.GetID(),
				&github.PullRequestReviewDismissalRequest{Message: new(message)}); err != nil {
				return n, fmt.Errorf("gh: dismiss review %d on %s/%s#%d: %w", r.GetID(), pr.Owner, pr.Repo, pr.Number, err)
			}
			n++
		}
		if resp == nil || resp.NextPage == 0 {
			return n, nil
		}
		opts.Page = resp.NextPage
	}
}
