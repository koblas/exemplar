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
	// bot is the app's own account ("<slug>[bot]", App.BotLogin): the only
	// author whose commits it reuses or merges.
	bot string
}

// NewConfigRepo wraps a config-writer client for "owner/exemplar-config";
// bot is the app's account login (App.BotLogin).
func NewConfigRepo(c *github.Client, fullName, bot string) (*ConfigRepo, error) {
	owner, repo, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("%w: config repo %q", ErrInvalidRepo, fullName)
	}
	if bot == "" {
		return nil, fmt.Errorf("%w: the app's login is required", ErrInvalidAppConfig)
	}
	return &ConfigRepo{gh: c, owner: owner, repo: repo, bot: bot}, nil
}

// DefaultBranch returns the default branch and its head commit.
func (r *ConfigRepo) DefaultBranch(ctx context.Context) (string, string, error) {
	repo, _, err := r.gh.Repositories.Get(ctx, r.owner, r.repo)
	if err != nil {
		return "", "", fmt.Errorf("gh: get config repo: %w", err)
	}
	name := repo.GetDefaultBranch()
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

// ErrForeignCommits is a branch or PR the config writer would reuse or
// merge that carries commits it did not write.
var ErrForeignCommits = errors.New("gh: commits not written by the app")

// EnsureBranch creates branch at sha. A branch that already exists (a
// retried run) is reused only when every commit it adds to sha is the
// app's own: anyone with push access could have created it first.
func (r *ConfigRepo) EnsureBranch(ctx context.Context, branch, sha string) error {
	_, _, err := r.gh.Git.CreateRef(ctx, r.owner, r.repo, github.CreateRef{Ref: "refs/heads/" + branch, SHA: sha})
	var ge *github.ErrorResponse
	if errors.As(err, &ge) && ge.Response != nil && ge.Response.StatusCode == http.StatusUnprocessableEntity {
		ref, _, err := r.gh.Git.GetRef(ctx, r.owner, r.repo, "heads/"+branch)
		if err != nil {
			return fmt.Errorf("gh: get branch %s: %w", branch, err)
		}
		tip := ref.GetObject().GetSHA()
		if tip == sha {
			return nil
		}
		cmp, _, err := r.gh.Repositories.CompareCommits(ctx, r.owner, r.repo, sha, tip, &github.ListOptions{PerPage: 100})
		if err != nil {
			return fmt.Errorf("gh: compare %s with %s: %w", branch, sha, err)
		}
		if cmp.GetTotalCommits() > len(cmp.Commits) || !r.ownCommits(cmp.Commits) {
			return fmt.Errorf("%w: branch %s already exists", ErrForeignCommits, branch)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("gh: create branch %s: %w", branch, err)
	}
	return nil
}

// ownCommits reports whether every commit was written by this app through
// the API: authored by its own account and signed by GitHub. Another app
// (GitHub Actions run by a committer's workflow) is not enough, and a
// pushed commit can name the bot's address as its author but cannot carry
// GitHub's signature for it.
func (r *ConfigRepo) ownCommits(cs []*github.RepositoryCommit) bool {
	for _, c := range cs {
		if !strings.EqualFold(c.GetAuthor().GetLogin(), r.bot) || !c.GetCommit().GetVerification().GetVerified() {
			return false
		}
	}
	return true
}

// PutFile writes path on branch, creating or replacing it. It sets no
// author or committer: GitHub signs an app's API commit only without
// them, and EnsureBranch and MergePR accept only signed app commits.
func (r *ConfigRepo) PutFile(ctx context.Context, branch, path string, content []byte, message string) error {
	opts := &github.RepositoryContentFileOptions{
		Message: new(message), Content: content, Branch: new(branch),
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
		Title: new(title), Head: new(branch), Base: new(base),
		Body: new(body), Draft: new(true),
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
	if _, _, err := r.gh.Issues.CreateComment(ctx, r.owner, r.repo, number, &github.IssueComment{Body: new(body)}); err != nil {
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
		return nil, fmt.Errorf("%w: %s", ErrNotAFile, path)
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
		Title: new(title), Head: new(branch), Base: new(base), Body: new(body),
	})
	if err != nil {
		return "", 0, fmt.Errorf("gh: open PR from %s: %w", branch, err)
	}
	return pr.GetHTMLURL(), pr.GetNumber(), nil
}

// MergePR squash-merges a config PR the app opened, holding only the
// app's own commits, at the head it checked: a commit pushed to the
// branch meanwhile makes GitHub refuse the merge. Branch protection that
// needs a review refuses it too; the PR then stays open for a human.
func (r *ConfigRepo) MergePR(ctx context.Context, number int, message string) error {
	pr, _, err := r.gh.PullRequests.Get(ctx, r.owner, r.repo, number)
	if err != nil {
		return fmt.Errorf("gh: get %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	if !strings.EqualFold(pr.GetUser().GetLogin(), r.bot) {
		return fmt.Errorf("%w: %s/%s#%d was opened by %s", ErrForeignCommits, r.owner, r.repo, number, pr.GetUser().GetLogin())
	}
	cs, _, err := r.gh.PullRequests.ListCommits(ctx, r.owner, r.repo, number, &github.ListOptions{PerPage: 100})
	if err != nil {
		return fmt.Errorf("gh: commits of %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	if len(cs) == 0 || len(cs) >= 100 || !r.ownCommits(cs) {
		return fmt.Errorf("%w: %s/%s#%d", ErrForeignCommits, r.owner, r.repo, number)
	}
	opts := &github.PullRequestOptions{MergeMethod: "squash", SHA: pr.GetHead().GetSHA()}
	if _, _, err := r.gh.PullRequests.Merge(ctx, r.owner, r.repo, number, message, opts); err != nil {
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
	return ConfigItem{
		Number: p.GetNumber(), URL: p.GetHTMLURL(), State: p.GetState(), Merged: !p.GetMergedAt().IsZero(),
		CreatedAt: p.GetCreatedAt().Time,
	}, true, nil
}

// HumanActivity reports whether a person (not a bot) commented on, or
// reviewed, PR or issue number since since: the owner responded.
func (r *ConfigRepo) HumanActivity(ctx context.Context, number int, since time.Time) (bool, error) {
	human := func(u *github.User) bool { return u != nil && !strings.EqualFold(u.GetType(), "Bot") }
	cs, _, err := r.gh.Issues.ListComments(ctx, r.owner, r.repo, number, &github.IssueListCommentsOptions{Since: &since, PerPage: 100})
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
	i, _, err := r.gh.Issues.Create(ctx, r.owner, r.repo, &github.IssueRequest{Title: new(title), Body: new(body), Labels: &labels})
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
	if _, _, err := r.gh.Issues.Edit(ctx, r.owner, r.repo, number, &github.IssueRequest{State: new("closed")}); err != nil {
		return fmt.Errorf("gh: close %s/%s#%d: %w", r.owner, r.repo, number, err)
	}
	return nil
}

// ConfigPR is an open PR in exemplar-config and the files it adds.
type ConfigPR struct {
	Number int
	URL    string
	Author string
	// Bot is an author account of type Bot (the app's own PRs among them).
	Bot     bool
	HeadRef string
	HeadSHA string
	// Fork is a head branch outside exemplar-config (or a deleted fork).
	Fork  bool
	Added []string
}

// OpenPRs returns up to 300 open PRs, most recently updated first, each
// with the files it adds (at most 300 per PR).
func (r *ConfigRepo) OpenPRs(ctx context.Context) ([]ConfigPR, error) {
	var out []ConfigPR
	opts := &github.PullRequestListOptions{State: "open", Sort: "updated", Direction: "desc", PerPage: 100}
	for range 3 {
		page, resp, err := r.gh.PullRequests.List(ctx, r.owner, r.repo, opts)
		if err != nil {
			return nil, restErr(err, "open PRs of "+r.owner+"/"+r.repo)
		}
		for _, p := range page {
			head := p.GetHead().GetRepo().GetFullName()
			pr := ConfigPR{
				Number: p.GetNumber(), URL: p.GetHTMLURL(), Author: p.GetUser().GetLogin(),
				Bot: strings.EqualFold(p.GetUser().GetType(), "Bot"), HeadRef: p.GetHead().GetRef(), HeadSHA: p.GetHead().GetSHA(),
				Fork: !strings.EqualFold(head, r.owner+"/"+r.repo),
			}
			if pr.Added, err = r.addedFiles(ctx, pr.Number); err != nil {
				return nil, err
			}
			out = append(out, pr)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (r *ConfigRepo) addedFiles(ctx context.Context, number int) ([]string, error) {
	var out []string
	opts := &github.ListOptions{PerPage: 100}
	for range 3 {
		fs, resp, err := r.gh.PullRequests.ListFiles(ctx, r.owner, r.repo, number, opts)
		if err != nil {
			return nil, restErr(err, fmt.Sprintf("files of PR #%d", number))
		}
		for _, f := range fs {
			if f.GetStatus() == "added" {
				out = append(out, f.GetFilename())
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
