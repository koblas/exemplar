package gh

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

// maxParticipationPages bounds each list Participated reads.
const maxParticipationPages = 10

// Participated reports whether login reviewed or commented on repo#number
// before at: a submitted review, an inline review comment, or a comment in the
// PR conversation (the "reviewer" feedback role). except is a comment id not
// to count (the command being judged).
func Participated(ctx context.Context, c *github.Client, repo string, number int, login string, at time.Time, except int64) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return false, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	is := func(u *github.User) bool { return strings.EqualFold(u.GetLogin(), login) }
	before := func(t github.Timestamp) bool { return !t.IsZero() && t.Before(at) }

	opts := &github.ListOptions{PerPage: 100}
	for range maxParticipationPages {
		rs, resp, err := c.PullRequests.ListReviews(ctx, owner, name, number, opts)
		if err != nil {
			return false, restErr(err, fmt.Sprintf("reviews of %s#%d", repo, number))
		}
		for _, r := range rs {
			if is(r.GetUser()) && r.GetState() != "PENDING" && before(r.GetSubmittedAt()) {
				return true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	copts := &github.PullRequestListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for range maxParticipationPages {
		cs, resp, err := c.PullRequests.ListComments(ctx, owner, name, number, copts)
		if err != nil {
			return false, restErr(err, fmt.Sprintf("review comments of %s#%d", repo, number))
		}
		for _, rc := range cs {
			if rc.GetID() != except && is(rc.GetUser()) && before(rc.GetCreatedAt()) {
				return true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		copts.Page = resp.NextPage
	}
	iopts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for range maxParticipationPages {
		cs, resp, err := c.Issues.ListComments(ctx, owner, name, number, iopts)
		if err != nil {
			return false, restErr(err, fmt.Sprintf("comments of %s#%d", repo, number))
		}
		for _, ic := range cs {
			if ic.GetID() != except && is(ic.GetUser()) && before(ic.GetCreatedAt()) {
				return true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		iopts.Page = resp.NextPage
	}
	return false, nil
}

// React adds a reaction (e.g. "+1") to an inline review comment.
func (p *Poster) React(ctx context.Context, pr PR, commentID int64, content string) error {
	if _, _, err := p.gh.Reactions.CreatePullRequestCommentReaction(ctx, pr.Owner, pr.Repo, commentID, content); err != nil {
		return fmt.Errorf("gh: react to comment %d: %w", commentID, err)
	}
	return nil
}
