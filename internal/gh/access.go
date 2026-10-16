package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// CanRead reports whether login can read repo, from the repo's
// collaborator permission (which includes team and org base permissions),
// read with a client authenticated for repo. An unknown user or a repo the
// client cannot see is "no", never an error; any other failure is an
// error, and callers deny on errors.
func CanRead(ctx context.Context, c *github.Client, repo, login string) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || login == "" {
		return false, nil
	}
	lvl, _, err := c.Repositories.GetPermissionLevel(ctx, owner, name, login)
	if err != nil {
		var er *github.ErrorResponse
		if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("gh: permission of %s on %s: %w", login, repo, err)
	}
	switch lvl.GetPermission() {
	case "admin", "maintain", "write", "triage", "read":
		return true, nil
	}
	return false, nil
}

// PullRequest is what a playground run needs about a PR.
type PullRequest struct {
	Repo    string
	Number  int
	Title   string
	Body    string
	Author  string
	Draft   bool
	State   string
	BaseSHA string
	HeadSHA string
	// AuthorType is the author's account type ("User", "Bot").
	AuthorType string
	// HeadRepo is the head branch's repository; it differs from Repo for a
	// fork, and is empty when the fork was deleted.
	HeadRepo string
	// BaseRef is the base branch name.
	BaseRef string
	// Labels are the PR's label names.
	Labels []string
}

// GetPullRequest reads a PR's metadata.
func GetPullRequest(ctx context.Context, c *github.Client, repo string, number int) (PullRequest, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return PullRequest{}, fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}
	pr, _, err := c.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return PullRequest{}, restErr(err, fmt.Sprintf("%s#%d", repo, number))
	}
	return PullRequest{
		Repo: repo, Number: number, Title: pr.GetTitle(), Body: pr.GetBody(), Author: pr.GetUser().GetLogin(),
		Draft: pr.GetDraft(), State: pr.GetState(), BaseSHA: pr.GetBase().GetSHA(), HeadSHA: pr.GetHead().GetSHA(),
		AuthorType: pr.GetUser().GetType(), HeadRepo: pr.GetHead().GetRepo().GetFullName(), BaseRef: pr.GetBase().GetRef(),
		Labels: labelNames(pr.Labels),
	}, nil
}

// MaxPRCommits is the most commits GitHub lists for one PR.
const MaxPRCommits = 250

// PRCommitMessages returns the messages of the PR's commits (at most 250,
// GitHub's limit), for the auto-approve gates' Co-authored-by check.
func PRCommitMessages(ctx context.Context, c *github.Client, repo string, number int) ([]string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}
	var out []string
	opts := &github.ListOptions{PerPage: 100}
	for {
		commits, resp, err := c.PullRequests.ListCommits(ctx, owner, name, number, opts)
		if err != nil {
			return nil, restErr(err, fmt.Sprintf("commits of %s#%d", repo, number))
		}
		for _, cm := range commits {
			out = append(out, cm.GetCommit().GetMessage())
		}
		if resp == nil || resp.NextPage == 0 || len(out) >= MaxPRCommits {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// IsAncestor reports whether old is an ancestor of (or equal to) head in
// repo, from GitHub's compare API: the worker's shallow checkout cannot
// tell. A push that rewrote history (rebase, force-push) answers false,
// including when old is no longer reachable (404). Any other failure is
// an error, and callers treat the history as unknown.
func IsAncestor(ctx context.Context, c *github.Client, repo, old, head string) (bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return false, fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}
	cmp, _, err := c.Repositories.CompareCommits(ctx, owner, name, old, head, &github.ListOptions{PerPage: 1})
	if err != nil {
		var er *github.ErrorResponse
		if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("gh: compare %s...%s on %s: %w", old, head, repo, err)
	}
	switch cmp.GetStatus() {
	case "ahead", "identical":
		return true, nil
	case "behind", "diverged":
		return false, nil
	}
	return false, fmt.Errorf("gh: compare %s...%s on %s: %w %q", old, head, repo, errCompareStatus, cmp.GetStatus())
}

func labelNames(ls []*github.Label) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.GetName())
	}
	return out
}
