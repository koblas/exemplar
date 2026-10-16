package gh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v88/github"
)

// PostedComment is one inline comment of a submitted review.
type PostedComment struct {
	ID   int64
	Path string
	Side string
	Line int
	Body string
}

// ReviewComments lists a submitted review's inline comments, oldest first,
// so posted findings can be mapped to their comment ids.
func (p *Poster) ReviewComments(ctx context.Context, pr PR, reviewID int64) ([]PostedComment, error) {
	var out []PostedComment
	opts := &github.ListOptions{PerPage: 100}
	for {
		cs, resp, err := p.gh.PullRequests.ListReviewComments(ctx, pr.Owner, pr.Repo, pr.Number, reviewID, opts)
		if err != nil {
			return nil, fmt.Errorf("gh: comments of review %d: %w", reviewID, err)
		}
		for _, c := range cs {
			out = append(out, PostedComment{ID: c.GetID(), Path: c.GetPath(), Side: c.GetSide(), Line: c.GetLine(), Body: c.GetBody()})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	slices.SortFunc(out, func(a, b PostedComment) int { return int(a.ID - b.ID) })
	return out, nil
}

// ErrNoThread is returned when no review thread starts with the comment.
var ErrNoThread = errors.New("gh: review thread not found")

const threadsQuery = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        nodes { id isResolved comments(first: 1) { nodes { databaseId } } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`

// ThreadOf returns the node id of the review thread that commentID starts,
// and whether it is resolved.
func (p *Poster) ThreadOf(ctx context.Context, pr PR, commentID int64) (string, bool, error) {
	vars := map[string]any{"owner": pr.Owner, "name": pr.Repo, "number": pr.Number, "after": nil}
	for range 20 {
		var res struct {
			Repository struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
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
		if err := GraphQL(ctx, p.gh, threadsQuery, vars, &res); err != nil {
			return "", false, err
		}
		if res.Repository.PullRequest == nil {
			return "", false, ErrNoThread
		}
		rt := res.Repository.PullRequest.ReviewThreads
		for _, t := range rt.Nodes {
			if len(t.Comments.Nodes) > 0 && t.Comments.Nodes[0].DatabaseID == commentID {
				return t.ID, t.IsResolved, nil
			}
		}
		if !rt.PageInfo.HasNextPage {
			break
		}
		vars["after"] = rt.PageInfo.EndCursor
	}
	return "", false, ErrNoThread
}

const resolveMutation = `mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { id isResolved } } }`

// CloseThread replies to an Exemplar comment and resolves its thread
// ("Addressed in <sha7>", a retired rule). threadID may be
// empty; it is then looked up. It returns the thread id. A thread already
// resolved, by a person or earlier, gets no reply.
func (p *Poster) CloseThread(ctx context.Context, pr PR, commentID int64, threadID, reply string) (string, error) {
	resolved := false
	if threadID == "" {
		var err error
		if threadID, resolved, err = p.ThreadOf(ctx, pr, commentID); err != nil {
			return "", err
		}
	}
	if resolved {
		return threadID, nil
	}
	if _, _, err := p.gh.PullRequests.CreateCommentInReplyTo(ctx, pr.Owner, pr.Repo, pr.Number, reply, commentID); err != nil {
		return threadID, fmt.Errorf("gh: reply to comment %d: %w", commentID, err)
	}
	var out struct {
		ResolveReviewThread struct {
			Thread struct {
				IsResolved bool `json:"isResolved"`
			} `json:"thread"`
		} `json:"resolveReviewThread"`
	}
	if err := GraphQL(ctx, p.gh, resolveMutation, map[string]any{"id": threadID}, &out); err != nil {
		return threadID, fmt.Errorf("gh: resolve thread: %w", err)
	}
	return threadID, nil
}

const minimizeMutation = `mutation($id: ID!) { minimizeComment(input: {subjectId: $id, classifier: OUTDATED}) { minimizedComment { isMinimized } } }`

// MinimizeComment hides an Exemplar review comment as outdated (a
// linter that reported after posting also flags it). Minimizing
// an already-minimized comment is a no-op, so a retry is safe.
func (p *Poster) MinimizeComment(ctx context.Context, pr PR, commentID int64) error {
	c, _, err := p.gh.PullRequests.GetComment(ctx, pr.Owner, pr.Repo, commentID)
	if err != nil {
		return fmt.Errorf("gh: comment %d: %w", commentID, err)
	}
	if c.GetNodeID() == "" {
		return fmt.Errorf("gh: comment %d has no node id", commentID)
	}
	var out struct {
		MinimizeComment struct {
			MinimizedComment struct {
				IsMinimized bool `json:"isMinimized"`
			} `json:"minimizedComment"`
		} `json:"minimizeComment"`
	}
	if err := GraphQL(ctx, p.gh, minimizeMutation, map[string]any{"id": c.GetNodeID()}, &out); err != nil {
		return fmt.Errorf("gh: minimize comment %d: %w", commentID, err)
	}
	return nil
}

// Reply posts body as a reply in the thread commentID starts.
func (p *Poster) Reply(ctx context.Context, pr PR, commentID int64, body string) error {
	if _, _, err := p.gh.PullRequests.CreateCommentInReplyTo(ctx, pr.Owner, pr.Repo, pr.Number, body, commentID); err != nil {
		return fmt.Errorf("gh: reply to comment %d: %w", commentID, err)
	}
	return nil
}

// ReviewComment is one inline review comment on a PR, where GitHub
// currently places it on the latest diff.
type ReviewComment struct {
	ID          int64
	Author      string
	AuthorType  string
	Association string // author_association
	Path        string
	Side        string
	Start, End  int
	Body        string
}

// maxReviewCommentPages bounds PRComments to 1,000 comments.
const maxReviewCommentPages = 10

// PRComments lists repo#number's inline review comments for human-comment
// dedupe. Outdated comments, which GitHub no longer places on
// the latest diff, are left out.
func PRComments(ctx context.Context, c *github.Client, repo string, number int) ([]ReviewComment, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	var out []ReviewComment
	opts := &github.PullRequestListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for range maxReviewCommentPages {
		cs, resp, err := c.PullRequests.ListComments(ctx, owner, name, number, opts)
		if err != nil {
			return nil, restErr(err, fmt.Sprintf("review comments of %s#%d", repo, number))
		}
		for _, rc := range cs {
			if rc.GetLine() == 0 {
				continue // outdated
			}
			start := rc.GetStartLine()
			if start == 0 {
				start = rc.GetLine()
			}
			out = append(out, ReviewComment{
				ID: rc.GetID(), Author: rc.GetUser().GetLogin(), AuthorType: rc.GetUser().GetType(), Association: rc.GetAuthorAssociation(),
				Path: rc.GetPath(), Side: rc.GetSide(), Start: start, End: rc.GetLine(), Body: rc.GetBody(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
