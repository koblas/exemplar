package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

// ConfigRepo writes proposal branches and draft PRs to exemplar-config. It
// must be built from a FlavorConfigWriter client, the only token that can
// write to the config repo; it never touches a target repo.
type ConfigRepo struct {
	gh    *github.Client
	owner string
	repo  string
}

// NewConfigRepo wraps a config-writer client for "owner/exemplar-config".
func NewConfigRepo(c *github.Client, fullName string) (*ConfigRepo, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("gh: config repo %q must be owner/name", fullName)
	}
	return &ConfigRepo{gh: c, owner: owner, repo: repo}, nil
}

// DefaultBranch returns the default branch and its head commit.
func (r *ConfigRepo) DefaultBranch(ctx context.Context) (name, sha string, err error) {
	repo, _, err := r.gh.Repositories.Get(ctx, r.owner, r.repo)
	if err != nil {
		return "", "", fmt.Errorf("gh: get config repo: %w", err)
	}
	name = repo.GetDefaultBranch()
	ref, _, err := r.gh.Git.GetRef(ctx, r.owner, r.repo, "heads/"+name)
	if err != nil {
		return "", "", fmt.Errorf("gh: get %s: %w", name, err)
	}
	return name, ref.GetObject().GetSHA(), nil
}

// FindPR returns the URL of a PR (any state) from branch, if one exists.
func (r *ConfigRepo) FindPR(ctx context.Context, branch string) (string, bool, error) {
	prs, _, err := r.gh.PullRequests.List(ctx, r.owner, r.repo, &github.PullRequestListOptions{
		Head: r.owner + ":" + branch, State: "all",
	})
	if err != nil {
		return "", false, fmt.Errorf("gh: list PRs for %s: %w", branch, err)
	}
	if len(prs) == 0 {
		return "", false, nil
	}
	return prs[0].GetHTMLURL(), true, nil
}

// EnsureBranch creates branch at sha unless it already exists.
func (r *ConfigRepo) EnsureBranch(ctx context.Context, branch, sha string) error {
	_, _, err := r.gh.Git.CreateRef(ctx, r.owner, r.repo, github.CreateRef{Ref: "refs/heads/" + branch, SHA: sha})
	var ge *github.ErrorResponse
	if errors.As(err, &ge) && ge.Response != nil && ge.Response.StatusCode == http.StatusUnprocessableEntity {
		return nil // already exists (a retried run)
	}
	if err != nil {
		return fmt.Errorf("gh: create branch %s: %w", branch, err)
	}
	return nil
}

// PutFile writes path on branch, creating or replacing it.
func (r *ConfigRepo) PutFile(ctx context.Context, branch, path string, content []byte, message string) error {
	opts := &github.RepositoryContentFileOptions{
		Message: github.Ptr(message), Content: content, Branch: github.Ptr(branch),
	}
	existing, _, resp, err := r.gh.Repositories.GetContents(ctx, r.owner, r.repo, path, &github.RepositoryContentGetOptions{Ref: branch})
	switch {
	case err == nil && existing != nil:
		opts.SHA = existing.SHA
		_, _, err = r.gh.Repositories.UpdateFile(ctx, r.owner, r.repo, path, opts)
	case resp != nil && resp.StatusCode == http.StatusNotFound:
		_, _, err = r.gh.Repositories.CreateFile(ctx, r.owner, r.repo, path, opts)
	}
	if err != nil {
		return fmt.Errorf("gh: write %s on %s: %w", path, branch, err)
	}
	return nil
}

// OpenDraftPR opens a draft PR from branch into base.
func (r *ConfigRepo) OpenDraftPR(ctx context.Context, branch, base, title, body string) (string, error) {
	pr, _, err := r.gh.PullRequests.Create(ctx, r.owner, r.repo, &github.NewPullRequest{
		Title: github.Ptr(title), Head: github.Ptr(branch), Base: github.Ptr(base),
		Body: github.Ptr(body), Draft: github.Ptr(true),
	})
	if err != nil {
		return "", fmt.Errorf("gh: open PR from %s: %w", branch, err)
	}
	return pr.GetHTMLURL(), nil
}

// PullRequest reads a config PR's metadata.
func (r *ConfigRepo) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	return GetPullRequest(ctx, r.gh, r.owner+"/"+r.repo, number)
}

// Comment posts a comment on a config PR (the /exemplar try reply).
func (r *ConfigRepo) Comment(ctx context.Context, number int, body string) error {
	if _, _, err := r.gh.Issues.CreateComment(ctx, r.owner, r.repo, number, &github.IssueComment{Body: github.Ptr(body)}); err != nil {
		return fmt.Errorf("gh: comment on %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	return nil
}

// GetFile reads path at ref.
func (r *ConfigRepo) GetFile(ctx context.Context, ref, path string) ([]byte, error) {
	fc, _, _, err := r.gh.Repositories.GetContents(ctx, r.owner, r.repo, path, &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return nil, restErr(err, path+"@"+ref)
	}
	if fc == nil {
		return nil, fmt.Errorf("gh: %s is not a file", path)
	}
	s, err := fc.GetContent()
	if err != nil {
		return nil, fmt.Errorf("gh: decode %s: %w", path, err)
	}
	return []byte(s), nil
}

// OpenPR opens a ready-for-review PR from branch into base and returns its
// URL and number.
func (r *ConfigRepo) OpenPR(ctx context.Context, branch, base, title, body string) (string, int, error) {
	pr, _, err := r.gh.PullRequests.Create(ctx, r.owner, r.repo, &github.NewPullRequest{
		Title: github.Ptr(title), Head: github.Ptr(branch), Base: github.Ptr(base), Body: github.Ptr(body),
	})
	if err != nil {
		return "", 0, fmt.Errorf("gh: open PR from %s: %w", branch, err)
	}
	return pr.GetHTMLURL(), pr.GetNumber(), nil
}

// MergePR squash-merges a config PR. Branch protection that needs a review
// refuses it; the PR then stays open for a human.
func (r *ConfigRepo) MergePR(ctx context.Context, number int, message string) error {
	if _, _, err := r.gh.PullRequests.Merge(ctx, r.owner, r.repo, number, message, &github.PullRequestOptions{MergeMethod: "squash"}); err != nil {
		return fmt.Errorf("gh: merge %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	return nil
}

// ConfigItem is a PR or issue in exemplar-config.
type ConfigItem struct {
	Number    int
	URL       string
	State     string // open | closed
	Merged    bool
	CreatedAt time.Time
}

// PRByBranch returns the PR (any state) from branch, if one exists.
func (r *ConfigRepo) PRByBranch(ctx context.Context, branch string) (ConfigItem, bool, error) {
	prs, _, err := r.gh.PullRequests.List(ctx, r.owner, r.repo, &github.PullRequestListOptions{
		Head: r.owner + ":" + branch, State: "all",
	})
	if err != nil {
		return ConfigItem{}, false, fmt.Errorf("gh: list PRs for %s: %w", branch, err)
	}
	if len(prs) == 0 {
		return ConfigItem{}, false, nil
	}
	p := prs[0]
	return ConfigItem{Number: p.GetNumber(), URL: p.GetHTMLURL(), State: p.GetState(), Merged: !p.GetMergedAt().IsZero(),
		CreatedAt: p.GetCreatedAt().Time}, true, nil
}

// HumanActivity reports whether a person (not a bot) commented on, or
// reviewed, PR or issue number since since: the owner responded.
func (r *ConfigRepo) HumanActivity(ctx context.Context, number int, since time.Time) (bool, error) {
	human := func(u *github.User) bool { return u != nil && !strings.EqualFold(u.GetType(), "Bot") }
	cs, _, err := r.gh.Issues.ListComments(ctx, r.owner, r.repo, number, &github.IssueListCommentsOptions{Since: &since, ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		return false, fmt.Errorf("gh: comments on %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	for _, c := range cs {
		if human(c.GetUser()) && c.GetCreatedAt().After(since) {
			return true, nil
		}
	}
	rs, resp, err := r.gh.PullRequests.ListReviews(ctx, r.owner, r.repo, number, &github.ListOptions{PerPage: 100})
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return false, nil // an issue, not a PR
	}
	if err != nil {
		return false, fmt.Errorf("gh: reviews of %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	for _, rv := range rs {
		if human(rv.GetUser()) && rv.GetSubmittedAt().After(since) {
			return true, nil
		}
	}
	return false, nil
}

// FindIssue returns the issue (any state) labeled label with exactly
// title, among the most recent 300 with that label.
func (r *ConfigRepo) FindIssue(ctx context.Context, label, title string) (ConfigItem, bool, error) {
	opts := &github.IssueListByRepoOptions{State: "all", Labels: []string{label}, ListOptions: github.ListOptions{PerPage: 100}}
	for range 3 {
		is, resp, err := r.gh.Issues.ListByRepo(ctx, r.owner, r.repo, opts)
		if err != nil {
			return ConfigItem{}, false, fmt.Errorf("gh: issues of %s/%s: %w", r.owner, r.repo, err)
		}
		for _, i := range is {
			if !i.IsPullRequest() && i.GetTitle() == title {
				return ConfigItem{Number: i.GetNumber(), URL: i.GetHTMLURL(), State: i.GetState(), CreatedAt: i.GetCreatedAt().Time}, true, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return ConfigItem{}, false, nil
}

// CreateIssue opens an issue in exemplar-config.
func (r *ConfigRepo) CreateIssue(ctx context.Context, title, body string, labels []string) (ConfigItem, error) {
	i, _, err := r.gh.Issues.Create(ctx, r.owner, r.repo, &github.IssueRequest{Title: github.Ptr(title), Body: github.Ptr(body), Labels: &labels})
	if err != nil {
		return ConfigItem{}, fmt.Errorf("gh: open issue %q: %w", title, err)
	}
	return ConfigItem{Number: i.GetNumber(), URL: i.GetHTMLURL(), State: i.GetState(), CreatedAt: i.GetCreatedAt().Time}, nil
}

// CloseIssue comments on and closes an issue.
func (r *ConfigRepo) CloseIssue(ctx context.Context, number int, comment string) error {
	if comment != "" {
		if err := r.Comment(ctx, number, comment); err != nil {
			return err
		}
	}
	if _, _, err := r.gh.Issues.Edit(ctx, r.owner, r.repo, number, &github.IssueRequest{State: github.Ptr("closed")}); err != nil {
		return fmt.Errorf("gh: close %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	return nil
}
