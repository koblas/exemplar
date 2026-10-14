package gh

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

// Annotation is one check-run annotation (a linter finding).
type Annotation struct {
	Check   string // the check run's name, e.g. golangci-lint
	Path    string
	Start   int
	End     int
	Level   string
	Title   string // usually the linter's rule id
	Message string
}

// maxAnnotationRuns bounds the check runs read per commit.
const maxAnnotationRuns = 20

// Annotations returns the annotations of sha's check runs whose names are
// in checks (case-insensitive), as the repo's CI reported them.
func Annotations(ctx context.Context, c *github.Client, repo, sha string, checks []string) ([]Annotation, error) {
	out, _, err := annotations(ctx, c, repo, sha, checks, false)
	return out, err
}

// LinterAnnotations is Annotations for linter dedupe (spec §6.10): it reads
// only completed runs, and pending names the configured checks that have
// no completed run on sha yet (not started, queued or in progress).
func LinterAnnotations(ctx context.Context, c *github.Client, repo, sha string, checks []string) (anns []Annotation, pending []string, err error) {
	return annotations(ctx, c, repo, sha, checks, true)
}

func annotations(ctx context.Context, c *github.Client, repo, sha string, checks []string, completedOnly bool) ([]Annotation, []string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, nil, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	want := func(n string) bool {
		return slices.ContainsFunc(checks, func(c string) bool { return strings.EqualFold(c, n) })
	}
	runs, _, err := c.Checks.ListCheckRunsForRef(ctx, owner, name, sha, &github.ListCheckRunsOptions{
		Filter: github.Ptr("latest"), ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, nil, restErr(err, "check runs of "+repo+"@"+sha)
	}
	done := map[string]bool{}
	var out []Annotation
	n := 0
	for _, run := range runs.CheckRuns {
		if !want(run.GetName()) || n == maxAnnotationRuns {
			continue
		}
		if completedOnly && run.GetStatus() != "completed" {
			continue
		}
		done[strings.ToLower(run.GetName())] = true
		n++
		opts := &github.ListOptions{PerPage: 50}
		for {
			anns, resp, err := c.Checks.ListCheckRunAnnotations(ctx, owner, name, run.GetID(), opts)
			if err != nil {
				return nil, nil, restErr(err, fmt.Sprintf("annotations of check run %d", run.GetID()))
			}
			for _, a := range anns {
				out = append(out, Annotation{
					Check: run.GetName(), Path: a.GetPath(), Start: a.GetStartLine(), End: a.GetEndLine(),
					Level: a.GetAnnotationLevel(), Title: a.GetTitle(), Message: a.GetMessage(),
				})
			}
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}
	var pending []string
	for _, c := range checks {
		if !done[strings.ToLower(c)] {
			pending = append(pending, c)
		}
	}
	return out, pending, nil
}

// MergedPRs returns up to limit PRs merged into repo since since, newest
// first, with their changed files (at most 300 per PR).
func MergedPRs(ctx context.Context, c *github.Client, repo string, since time.Time, limit int) ([]PullRequest, [][]string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, nil, fmt.Errorf("gh: repo %q must be owner/name", repo)
	}
	var prs []PullRequest
	var files [][]string
	opts := &github.PullRequestListOptions{State: "closed", Sort: "updated", Direction: "desc", ListOptions: github.ListOptions{PerPage: 50}}
	for len(prs) < limit {
		page, resp, err := c.PullRequests.List(ctx, owner, name, opts)
		if err != nil {
			return nil, nil, restErr(err, "PRs of "+repo)
		}
		for _, p := range page {
			// Sorted by update time: once one is older than since, the rest are too.
			if p.GetUpdatedAt().Before(since) {
				return prs, files, nil
			}
			if p.MergedAt == nil || p.GetMergedAt().Before(since) || len(prs) == limit {
				continue
			}
			pr := PullRequest{Repo: repo, Number: p.GetNumber(), Title: p.GetTitle(), Body: p.GetBody(), Author: p.GetUser().GetLogin(),
				State: p.GetState(), BaseSHA: p.GetBase().GetSHA(), HeadSHA: p.GetHead().GetSHA()}
			fs, err := prFileNames(ctx, c, owner, name, pr.Number)
			if err != nil {
				return nil, nil, err
			}
			prs = append(prs, pr)
			files = append(files, fs)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return prs, files, nil
}

func prFileNames(ctx context.Context, c *github.Client, owner, name string, number int) ([]string, error) {
	var out []string
	opts := &github.ListOptions{PerPage: 100}
	for page := 0; page < 3; page++ {
		fs, resp, err := c.PullRequests.ListFiles(ctx, owner, name, number, opts)
		if err != nil {
			return nil, restErr(err, fmt.Sprintf("files of PR #%d", number))
		}
		for _, f := range fs {
			out = append(out, f.GetFilename())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
