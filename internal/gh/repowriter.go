package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// RepoWriter writes generated files to one target repo and opens their PR
// (rule exports). It must be built from a FlavorExport client.
type RepoWriter struct {
	gh          *github.Client
	owner, repo string
}

// NewRepoWriter wraps an export client for "owner/name".
func NewRepoWriter(c *github.Client, repo string) (*RepoWriter, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}
	return &RepoWriter{gh: c, owner: owner, repo: name}, nil
}

// DefaultBranch returns the default branch and its head commit.
func (w *RepoWriter) DefaultBranch(ctx context.Context) (string, string, error) {
	r, _, err := w.gh.Repositories.Get(ctx, w.owner, w.repo)
	if err != nil {
		return "", "", restErr(err, w.owner+"/"+w.repo)
	}
	name := r.GetDefaultBranch()
	ref, _, err := w.gh.Git.GetRef(ctx, w.owner, w.repo, "heads/"+name)
	if err != nil {
		return "", "", restErr(err, "branch "+name)
	}
	return name, ref.GetObject().GetSHA(), nil
}

// GetFile reads path at ref; a missing file is (nil, false, nil).
func (w *RepoWriter) GetFile(ctx context.Context, ref, path string) ([]byte, bool, error) {
	fc, _, resp, err := w.gh.Repositories.GetContents(ctx, w.owner, w.repo, path, &github.RepositoryContentGetOptions{Ref: ref})
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, restErr(err, path+"@"+ref)
	}
	if fc == nil {
		return nil, false, fmt.Errorf("%w: %s", ErrNotAFile, path)
	}
	s, err := fc.GetContent()
	if err != nil {
		return nil, false, fmt.Errorf("gh: decode %s: %w", path, err)
	}
	return []byte(s), true, nil
}

// ResetBranch points branch at sha, creating it or moving it (force).
func (w *RepoWriter) ResetBranch(ctx context.Context, branch, sha string) error {
	_, _, err := w.gh.Git.CreateRef(ctx, w.owner, w.repo, github.CreateRef{Ref: "refs/heads/" + branch, SHA: sha})
	var ge *github.ErrorResponse
	if errors.As(err, &ge) && ge.Response != nil && ge.Response.StatusCode == http.StatusUnprocessableEntity {
		_, _, err = w.gh.Git.UpdateRef(ctx, w.owner, w.repo, "heads/"+branch, github.UpdateRef{SHA: sha, Force: new(true)})
	}
	if err != nil {
		return fmt.Errorf("gh: reset %s: %w", branch, err)
	}
	return nil
}

// PutFile writes path on branch, creating or replacing it.
func (w *RepoWriter) PutFile(ctx context.Context, branch, path string, content []byte, message string) error {
	opts := &github.RepositoryContentFileOptions{Message: new(message), Content: content, Branch: new(branch)}
	existing, _, resp, err := w.gh.Repositories.GetContents(ctx, w.owner, w.repo, path, &github.RepositoryContentGetOptions{Ref: branch})
	switch {
	case err == nil && existing != nil:
		opts.SHA = existing.SHA
		_, _, err = w.gh.Repositories.UpdateFile(ctx, w.owner, w.repo, path, opts)
	case resp != nil && resp.StatusCode == http.StatusNotFound:
		_, _, err = w.gh.Repositories.CreateFile(ctx, w.owner, w.repo, path, opts)
	case err == nil:
		err = ErrNotAFile
	}
	if err != nil {
		return fmt.Errorf("gh: write %s on %s: %w", path, branch, err)
	}
	return nil
}

// OpenPRFor returns branch's open PR, if any.
func (w *RepoWriter) OpenPRFor(ctx context.Context, branch string) (ConfigItem, string, bool, error) {
	prs, _, err := w.gh.PullRequests.List(ctx, w.owner, w.repo, &github.PullRequestListOptions{Head: w.owner + ":" + branch, State: "open"})
	if err != nil {
		return ConfigItem{}, "", false, fmt.Errorf("gh: list PRs for %s: %w", branch, err)
	}
	if len(prs) == 0 {
		return ConfigItem{}, "", false, nil
	}
	p := prs[0]
	return ConfigItem{Number: p.GetNumber(), URL: p.GetHTMLURL(), State: p.GetState(), CreatedAt: p.GetCreatedAt().Time}, p.GetNodeID(), true, nil
}

// OpenPR opens a PR from branch into base; it returns the PR and its node id.
func (w *RepoWriter) OpenPR(ctx context.Context, branch, base, title, body string) (ConfigItem, string, error) {
	p, _, err := w.gh.PullRequests.Create(ctx, w.owner, w.repo, &github.NewPullRequest{
		Title: new(title), Head: new(branch), Base: new(base), Body: new(body),
	})
	if err != nil {
		return ConfigItem{}, "", fmt.Errorf("gh: open PR from %s: %w", branch, err)
	}
	return ConfigItem{Number: p.GetNumber(), URL: p.GetHTMLURL(), State: p.GetState(), CreatedAt: p.GetCreatedAt().Time}, p.GetNodeID(), nil
}

const autoMergeMutation = `mutation($id: ID!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: SQUASH}) { pullRequest { autoMergeRequest { enabledAt } } } }`

// EnableAutoMerge turns on GitHub's native auto-merge: the PR merges once
// the repo's branch protection is satisfied, never sooner.
func (w *RepoWriter) EnableAutoMerge(ctx context.Context, nodeID string) error {
	var out map[string]any
	if err := GraphQL(ctx, w.gh, autoMergeMutation, map[string]any{"id": nodeID}, &out); err != nil {
		return fmt.Errorf("gh: enable auto-merge: %w", err)
	}
	return nil
}
