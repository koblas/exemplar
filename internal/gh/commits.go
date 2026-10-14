package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v88/github"
)

// CommitFiles lists the paths a commit changed, following GitHub's file
// pagination (at most 3000 files). An unknown commit wraps ErrNotFound.
func CommitFiles(ctx context.Context, c *github.Client, owner, repo, sha string) ([]string, error) {
	var out []string
	opts := &github.ListOptions{PerPage: 100}
	for {
		rc, resp, err := c.Repositories.GetCommit(ctx, owner, repo, sha, opts)
		if err != nil {
			return nil, restErr(err, "commit "+sha)
		}
		for _, f := range rc.Files {
			out = append(out, f.GetFilename())
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// FilePatch is one file of a PR's diff: its path and its hunks without the
// file header. Patch is empty for binary or very large files.
type FilePatch struct {
	Path  string
	Patch string
	// PreviousPath is a renamed file's old path ("" otherwise).
	PreviousPath string
}

// PRFilePatches lists a PR's changed files with their patches (at most
// 3000 files). An unknown PR wraps ErrNotFound.
func PRFilePatches(ctx context.Context, c *github.Client, owner, repo string, number int) ([]FilePatch, error) {
	var out []FilePatch
	opts := &github.ListOptions{PerPage: 100}
	for {
		files, resp, err := c.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, restErr(err, fmt.Sprintf("PR #%d", number))
		}
		for _, f := range files {
			out = append(out, FilePatch{Path: f.GetFilename(), Patch: f.GetPatch(), PreviousPath: f.GetPreviousFilename()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// restErr maps REST errors to ErrRateLimited and ErrNotFound.
func restErr(err error, what string) error {
	var er *github.ErrorResponse
	var rl *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	switch {
	case errors.As(err, &rl) || errors.As(err, &abuse):
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	case errors.As(err, &er) && er.Response != nil &&
		(er.Response.StatusCode == http.StatusNotFound || er.Response.StatusCode == http.StatusUnprocessableEntity):
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return fmt.Errorf("gh: %s: %w", what, err)
}

// ReadFile reads path at ref; a missing file is (nil, false, nil).
func ReadFile(ctx context.Context, c *github.Client, repo, ref, path string) ([]byte, bool, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, false, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	fc, _, resp, err := c.Repositories.GetContents(ctx, owner, name, path, &github.RepositoryContentGetOptions{Ref: ref})
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, restErr(err, path+"@"+ref)
	}
	if fc == nil {
		return nil, false, fmt.Errorf("gh: %s is not a file", path)
	}
	s, err := fc.GetContent()
	if err != nil {
		return nil, false, fmt.Errorf("gh: decode %s: %w", path, err)
	}
	return []byte(s), true, nil
}
