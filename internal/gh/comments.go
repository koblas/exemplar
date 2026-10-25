package gh

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v88/github"
)

// UpsertComment keeps one PR conversation comment identified by marker (a
// hidden HTML comment): it edits the comment that carries the marker, or
// creates it. It returns the comment's id and whether it was created.
// Pull requests write covers a PR's conversation comments.
func (p *Poster) UpsertComment(ctx context.Context, pr PR, marker, body string) (int64, bool, error) {
	text := strings.TrimSpace(body) + "\n\n" + marker
	opts := &github.IssueListCommentsOptions{}
	opts.PerPage = 100
	for {
		comments, resp, err := p.gh.Issues.ListComments(ctx, pr.Owner, pr.Repo, pr.Number, opts)
		if err != nil {
			return 0, false, fmt.Errorf("gh: list comments on %s/%s#%d: %w", pr.Owner, pr.Repo, pr.Number, err)
		}
		for _, c := range comments {
			if !strings.Contains(c.GetBody(), marker) || !strings.EqualFold(c.GetUser().GetType(), "Bot") {
				continue
			}
			if c.GetBody() == text {
				return c.GetID(), false, nil
			}
			if _, _, err := p.gh.Issues.EditComment(ctx, pr.Owner, pr.Repo, c.GetID(), &github.IssueComment{Body: new(text)}); err != nil {
				return 0, false, fmt.Errorf("gh: edit comment %d: %w", c.GetID(), err)
			}
			return c.GetID(), false, nil
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	c, _, err := p.gh.Issues.CreateComment(ctx, pr.Owner, pr.Repo, pr.Number, &github.IssueComment{Body: new(text)})
	if err != nil {
		return 0, false, fmt.Errorf("gh: create comment on %s/%s#%d: %w", pr.Owner, pr.Repo, pr.Number, err)
	}
	return c.GetID(), true, nil
}
