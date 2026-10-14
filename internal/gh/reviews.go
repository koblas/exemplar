package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// Review is one PR review.
type Review struct {
	ID       int64
	User     string
	UserType string // "User", "Bot"
	State    string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	CommitID string
}

// Reviews lists a PR's reviews in submission order.
func Reviews(ctx context.Context, c *github.Client, repo string, number int) ([]Review, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	var out []Review
	opts := &github.ListOptions{PerPage: 100}
	for {
		rs, resp, err := c.PullRequests.ListReviews(ctx, owner, name, number, opts)
		if err != nil {
			return nil, restErr(err, fmt.Sprintf("reviews of %s#%d", repo, number))
		}
		for _, r := range rs {
			out = append(out, Review{ID: r.GetID(), User: r.GetUser().GetLogin(), UserType: r.GetUser().GetType(),
				State: r.GetState(), CommitID: r.GetCommitID()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// TeamMember reports whether login is an active member of team
// ("@org/slug" or "org/slug"). Pending invitations are not members.
func TeamMember(ctx context.Context, c *github.Client, team, login string) (bool, error) {
	org, slug, ok := strings.Cut(strings.TrimPrefix(team, "@"), "/")
	if !ok || org == "" || slug == "" {
		return false, fmt.Errorf("gh: team %q must be @org/slug", team)
	}
	m, _, err := c.Teams.GetTeamMembershipBySlug(ctx, org, slug, login)
	if err != nil {
		var er *github.ErrorResponse
		if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("gh: membership of %s in %s: %w", login, team, err)
	}
	return m.GetState() == "active", nil
}
