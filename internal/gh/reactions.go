package gh

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v88/github"
)

// Reaction is one person's reaction to a review comment.
type Reaction struct {
	Login   string
	Bot     bool
	Content string // THUMBS_UP, THUMBS_DOWN, ...
}

const reactionsQuery = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 50, after: $after) {
        nodes { comments(first: 1) { nodes { databaseId reactionGroups { content reactors(first: 50) {
          nodes { __typename ... on User { login } ... on Bot { login } } } } } } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`

// CommentReactions returns the reactions on the first comment of each of
// repo#number's review threads, by comment id (spec §9.1: one GraphQL
// query per PR, paged by threads).
func CommentReactions(ctx context.Context, c *github.Client, repo string, number int) (map[int64][]Reaction, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	out := map[int64][]Reaction{}
	vars := map[string]any{"owner": owner, "name": name, "number": number, "after": nil}
	for range 20 {
		var res struct {
			Repository struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							Comments struct {
								Nodes []struct {
									DatabaseID     int64 `json:"databaseId"`
									ReactionGroups []struct {
										Content  string `json:"content"`
										Reactors struct {
											Nodes []struct {
												Typename string `json:"__typename"`
												Login    string `json:"login"`
											} `json:"nodes"`
										} `json:"reactors"`
									} `json:"reactionGroups"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		if err := GraphQL(ctx, c, reactionsQuery, vars, &res); err != nil {
			return nil, err
		}
		if res.Repository.PullRequest == nil {
			return nil, ErrNotFound
		}
		rt := res.Repository.PullRequest.ReviewThreads
		for _, t := range rt.Nodes {
			for _, cm := range t.Comments.Nodes {
				for _, g := range cm.ReactionGroups {
					for _, r := range g.Reactors.Nodes {
						if r.Login != "" {
							out[cm.DatabaseID] = append(out[cm.DatabaseID], Reaction{Login: r.Login, Bot: r.Typename == "Bot", Content: g.Content})
						}
					}
				}
			}
		}
		if !rt.PageInfo.HasNextPage {
			break
		}
		vars["after"] = rt.PageInfo.EndCursor
	}
	return out, nil
}
