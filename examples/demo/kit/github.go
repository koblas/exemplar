package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
)

var (
	errNoReadme  = errors.New("base tree has no README.md")
	errEmptyRepo = errors.New("the sandbox repository is empty; run the repo phase first")
)

// ghErr wraps an error from the GitHub client.
func ghErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("github: %w", err)
}

// account is one demo person: a token-backed client and its login.
type account struct {
	login string
	c     *github.Client
}

// repoClient writes to the sandbox repository as the demo's people.
type repoClient struct {
	org, repo string
	author    account
	reviewers [2]account // reviewers[0] is the rule owner
	// pace is the pause after each write, to stay under GitHub's
	// content-creation limits.
	pace time.Duration
	log  *slog.Logger
}

// wrote pauses after a write and backs off on a rate limit: GitHub's
// secondary limits answer 403 or 429 with Retry-After, and the write is
// retried once after it.
func (r *repoClient) wrote(ctx context.Context, resp *github.Response, err error, what string, retry func() (*github.Response, error)) error {
	var abuse *github.AbuseRateLimitError
	var rate *github.RateLimitError
	switch {
	case errors.As(err, &abuse):
		wait := time.Minute
		if d := abuse.GetRetryAfter(); d > 0 {
			wait = d
		}
		r.log.Warn("secondary rate limit; waiting", "what", what, "wait", wait)
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		resp, err = retry()
	case errors.As(err, &rate):
		wait := time.Until(rate.Rate.Reset.Time)
		r.log.Warn("rate limited; waiting for the reset", "what", what, "wait", wait)
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		resp, err = retry()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	_ = resp
	return sleep(ctx, r.pace)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

func notFound(err error) bool {
	var e *github.ErrorResponse
	return errors.As(err, &e) && e.Response != nil && e.Response.StatusCode == http.StatusNotFound
}

// mainSHA is the head of main, or "" when the repository is empty.
func (r *repoClient) mainSHA(ctx context.Context) (string, error) {
	ref, _, err := r.author.c.Git.GetRef(ctx, r.org, r.repo, "heads/main")
	var e *github.ErrorResponse
	switch {
	case notFound(err), errors.As(err, &e) && e.Response != nil && e.Response.StatusCode == http.StatusConflict:
		return "", nil // 409: the repository is empty
	case err != nil:
		return "", fmt.Errorf("main of %s/%s: %w", r.org, r.repo, err)
	}
	return ref.GetObject().GetSHA(), nil
}

// commit writes files on top of parent and returns the new commit.
func (r *repoClient) commit(ctx context.Context, parent string, files map[string]string, msg string) (string, error) {
	base, _, err := r.author.c.Git.GetCommit(ctx, r.org, r.repo, parent)
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", parent, err)
	}
	entries := make([]*github.TreeEntry, 0, len(files))
	for _, p := range sortedKeys(files) {
		entries = append(entries, &github.TreeEntry{Path: new(p), Mode: new("100644"), Type: new("blob"), Content: new(files[p])})
	}
	var tree *github.Tree
	var resp *github.Response
	create := func() (*github.Response, error) {
		tree, resp, err = r.author.c.Git.CreateTree(ctx, r.org, r.repo, base.GetTree().GetSHA(), entries)
		return resp, ghErr(err)
	}
	resp, err = create()
	if err := r.wrote(ctx, resp, err, "create tree", create); err != nil {
		return "", err
	}
	var c *github.Commit
	mk := func() (*github.Response, error) {
		c, resp, err = r.author.c.Git.CreateCommit(ctx, r.org, r.repo, github.Commit{
			Message: new(msg), Tree: &github.Tree{SHA: tree.SHA}, Parents: []*github.Commit{{SHA: new(parent)}},
		}, nil)
		return resp, ghErr(err)
	}
	resp, err = mk()
	if err := r.wrote(ctx, resp, err, "create commit", mk); err != nil {
		return "", err
	}
	return c.GetSHA(), nil
}

// pushBase writes the sandbox repository's base tree to main. An empty
// repository first gets its README through the contents API, since the
// git data API cannot write to an empty repository. A main that already
// has commits is left alone.
func (r *repoClient) pushBase(ctx context.Context, files map[string]string) (string, error) {
	sha, err := r.mainSHA(ctx)
	if err != nil || sha != "" {
		return sha, err
	}
	readme, ok := files["README.md"]
	if !ok {
		return "", errNoReadme
	}
	var resp *github.Response
	create := func() (*github.Response, error) {
		_, resp, err = r.author.c.Repositories.CreateFile(ctx, r.org, r.repo, "README.md", &github.RepositoryContentFileOptions{
			Message: new("Start the ledger service"), Content: []byte(readme), Branch: new("main"),
		})
		return resp, ghErr(err)
	}
	resp, err = create()
	if err := r.wrote(ctx, resp, err, "create README", create); err != nil {
		return "", err
	}
	if sha, err = r.mainSHA(ctx); err != nil {
		return "", err
	}
	rest := map[string]string{}
	for p, c := range files {
		if p != "README.md" {
			rest[p] = c
		}
	}
	head, err := r.commit(ctx, sha, rest, "Add the ledger service")
	if err != nil {
		return "", err
	}
	return head, r.setRef(ctx, "main", head, false)
}

// setRef points branch at sha, creating it when it is missing.
func (r *repoClient) setRef(ctx context.Context, branch, sha string, create bool) error {
	var resp *github.Response
	var err error
	write := func() (*github.Response, error) {
		if create {
			_, resp, err = r.author.c.Git.CreateRef(ctx, r.org, r.repo, github.CreateRef{Ref: "refs/heads/" + branch, SHA: sha})
		} else {
			// main moves forward only; a demo branch is rewritten on a
			// re-run that pushed it but failed before opening its PR.
			_, resp, err = r.author.c.Git.UpdateRef(ctx, r.org, r.repo, "heads/"+branch, github.UpdateRef{SHA: sha, Force: new(branch != "main")})
		}
		return resp, ghErr(err)
	}
	resp, err = write()
	return r.wrote(ctx, resp, err, "set "+branch, write)
}

// openPR returns the PR from branch, pushing the branch and opening the
// PR when it does not exist yet (merged or closed ones count).
func (r *repoClient) openPR(ctx context.Context, p DemoPR) (*github.PullRequest, error) {
	prs, _, err := r.author.c.PullRequests.List(ctx, r.org, r.repo, &github.PullRequestListOptions{Head: r.org + ":" + p.Branch, State: "all", PerPage: 10})
	if err != nil {
		return nil, fmt.Errorf("PRs from %s: %w", p.Branch, err)
	}
	if len(prs) > 0 {
		return prs[0], nil
	}
	main, err := r.mainSHA(ctx)
	if err != nil {
		return nil, err
	}
	if main == "" {
		return nil, errEmptyRepo
	}
	head, err := r.commit(ctx, main, p.Files, p.Title)
	if err != nil {
		return nil, err
	}
	_, _, err = r.author.c.Git.GetRef(ctx, r.org, r.repo, "heads/"+p.Branch)
	switch {
	case notFound(err):
		err = r.setRef(ctx, p.Branch, head, true)
	case err == nil:
		err = r.setRef(ctx, p.Branch, head, false)
	}
	if err != nil {
		return nil, fmt.Errorf("branch %s: %w", p.Branch, err)
	}
	var pr *github.PullRequest
	var resp *github.Response
	open := func() (*github.Response, error) {
		pr, resp, err = r.author.c.PullRequests.Create(ctx, r.org, r.repo, &github.NewPullRequest{
			Title: new(p.Title), Body: new(p.Body), Head: new(p.Branch), Base: new("main"),
		})
		return resp, ghErr(err)
	}
	resp, err = open()
	if err := r.wrote(ctx, resp, err, "open "+p.Branch, open); err != nil {
		return nil, err
	}
	r.log.Info("opened PR", "number", pr.GetNumber(), "branch", p.Branch)
	return pr, nil
}

// review submits a COMMENT review as who, with inline comments on the
// PR's head.
func (r *repoClient) review(ctx context.Context, who account, pr *github.PullRequest, body string, comments []*github.DraftReviewComment) error {
	var resp *github.Response
	var err error
	submit := func() (*github.Response, error) {
		_, resp, err = who.c.PullRequests.CreateReview(ctx, r.org, r.repo, pr.GetNumber(), &github.PullRequestReviewRequest{
			CommitID: new(pr.GetHead().GetSHA()), Body: new(body), Event: new("COMMENT"), Comments: comments,
		})
		return resp, ghErr(err)
	}
	resp, err = submit()
	return r.wrote(ctx, resp, err, fmt.Sprintf("review #%d as %s", pr.GetNumber(), who.login), submit)
}

// reviewed reports whether who has submitted a review on the PR, which
// makes them a participant whose feedback counts at reviewer weight.
func (r *repoClient) reviewed(ctx context.Context, who account, number int) (bool, error) {
	rs, _, err := r.author.c.PullRequests.ListReviews(ctx, r.org, r.repo, number, &github.ListOptions{PerPage: 100})
	if err != nil {
		return false, fmt.Errorf("reviews of #%d: %w", number, err)
	}
	for _, rv := range rs {
		if strings.EqualFold(rv.GetUser().GetLogin(), who.login) && rv.GetState() != "PENDING" {
			return true, nil
		}
	}
	return false, nil
}

// botComments are the PR's inline comments by an app carrying rule's id,
// with each one's first reply (comment id -> reply id).
func (r *repoClient) botComments(ctx context.Context, number int, rule string) ([]*github.PullRequestComment, map[int64]int64, error) {
	var bot []*github.PullRequestComment
	replied := map[int64]int64{}
	opts := &github.PullRequestListCommentsOptions{PerPage: 100}
	for {
		cs, resp, err := r.author.c.PullRequests.ListComments(ctx, r.org, r.repo, number, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("comments on #%d: %w", number, err)
		}
		for _, c := range cs {
			if to := c.GetInReplyTo(); to != 0 {
				if _, ok := replied[to]; !ok {
					replied[to] = c.GetID()
				}
				continue
			}
			if c.GetUser().GetType() == "Bot" && strings.Contains(c.GetBody(), "Rule `"+rule+"`") {
				bot = append(bot, c)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return bot, replied, nil
		}
		opts.Page = resp.NextPage
	}
}

// reply answers a review comment's thread as who and returns the reply.
func (r *repoClient) reply(ctx context.Context, who account, number int, commentID int64, body string) (int64, error) {
	var c *github.PullRequestComment
	var resp *github.Response
	var err error
	write := func() (*github.Response, error) {
		c, resp, err = who.c.PullRequests.CreateCommentInReplyTo(ctx, r.org, r.repo, number, body, commentID)
		return resp, ghErr(err)
	}
	resp, err = write()
	if err := r.wrote(ctx, resp, err, fmt.Sprintf("reply on #%d as %s", number, who.login), write); err != nil {
		return 0, err
	}
	return c.GetID(), nil
}

// acknowledged waits up to wait for the worker's +1 on a command reply,
// which it adds once the feedback is recorded. No reaction means the
// command was refused or not seen (a reviewer GitHub does not count as a
// participant, a webhook not delivered), so it must not be counted.
func (r *repoClient) acknowledged(ctx context.Context, replyID int64, poll, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		rs, _, err := r.author.c.Reactions.ListPullRequestCommentReactions(ctx, r.org, r.repo, replyID, &github.ListReactionOptions{Content: "+1"})
		if err != nil {
			return false, fmt.Errorf("reactions on %d: %w", replyID, err)
		}
		for _, x := range rs {
			if x.GetUser().GetType() == "Bot" {
				return true, nil
			}
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		if err := sleep(ctx, poll); err != nil {
			return false, err
		}
	}
}

// merge squash-merges the PR as the author, unless it is already merged.
func (r *repoClient) merge(ctx context.Context, pr *github.PullRequest) error {
	if pr.GetMerged() || pr.MergedAt != nil {
		return nil
	}
	var resp *github.Response
	var err error
	write := func() (*github.Response, error) {
		_, resp, err = r.author.c.PullRequests.Merge(ctx, r.org, r.repo, pr.GetNumber(), "", &github.PullRequestOptions{MergeMethod: "squash"})
		return resp, ghErr(err)
	}
	resp, err = write()
	return r.wrote(ctx, resp, err, fmt.Sprintf("merge #%d", pr.GetNumber()), write)
}
