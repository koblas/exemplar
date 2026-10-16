package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/codeowners"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// mergePolicyInput is what exemplar/merge-policy is decided from.
type mergePolicyInput struct {
	repo, author string
	number       int
	touched      []string
	owners       codeowners.File
	gatesPassed  bool
}

// reportMergePolicy sets exemplar/merge-policy on the head:
// success when every auto-approve gate passed, or when people approved
// it as a code-owner review requirement would ask; otherwise in progress,
// naming what it waits for. It reports whether the check passed.
func reportMergePolicy(ctx context.Context, reader *github.Client, member gates.MemberFunc, poster *gh.Poster, target gh.PR, in mergePolicyInput) (bool, error) {
	if in.gatesPassed {
		if err := poster.SetMergePolicy(ctx, target, true, "Auto-approve gates passed",
			"Exemplar found nothing to flag on this commit, and every auto-approve gate passed."); err != nil {
			return true, fmt.Errorf("worker: set merge policy: %w", err)
		}
		return true, nil
	}
	rs, err := gh.Reviews(ctx, reader, in.repo, in.number)
	if err != nil {
		return false, fmt.Errorf("worker: list reviews: %w", err)
	}
	reviews := make([]gates.PolicyReview, 0, len(rs))
	for _, r := range rs {
		reviews = append(reviews, gates.PolicyReview{User: r.User, UserType: r.UserType, State: r.State, CommitID: r.CommitID})
	}
	res, err := gates.HumanMergePolicy(ctx, in.touched, in.owners, in.author, target.HeadSHA, reviews, member)
	if err != nil {
		return false, fmt.Errorf("worker: merge policy: %w", err)
	}
	if res.Pass {
		if err := poster.SetMergePolicy(ctx, target, true, "Approved", "Approved on this commit by "+strings.Join(res.Approvers, ", ")+"."); err != nil {
			return true, fmt.Errorf("worker: set merge policy: %w", err)
		}
		return true, nil
	}
	var b strings.Builder
	b.WriteString("Exemplar's auto-approve gates did not all pass on this commit, so it needs people's approval. Waiting for:\n\n")
	for _, m := range res.Missing {
		b.WriteString("- " + m + "\n")
	}
	b.WriteString("\nAn approval counts once it is on this commit; a push needs a new one.")
	if err := poster.SetMergePolicy(ctx, target, false, "Waiting for a code-owner approval", b.String()); err != nil {
		return false, fmt.Errorf("worker: set merge policy: %w", err)
	}
	return false, nil
}

// MergePolicyWorker runs merge_policy jobs: a PR's reviews changed, so
// exemplar/merge-policy is decided again for its head.
type MergePolicyWorker struct {
	river.WorkerDefaults[store.MergePolicyArgs]

	cfg Config
}

// NewMergePolicyWorker validates cfg; it needs GitHub, Store and Snapshot.
func NewMergePolicyWorker(cfg Config) (*MergePolicyWorker, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: merge policy needs a store", errMissingDependency)
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &MergePolicyWorker{cfg: w.cfg}, nil
}

// Work implements river.Worker.
func (w *MergePolicyWorker) Work(ctx context.Context, job *river.Job[store.MergePolicyArgs]) error {
	a := job.Args
	pass, ignored, err := w.Handle(ctx, a)
	if err != nil {
		return err
	}
	w.cfg.Log.Info("merge policy", "repo", a.Repo, "pr", a.PR, "action", a.Action, "pass", pass, "ignored", ignored)
	return nil
}

// Handle decides the check for the PR's current head. It returns why
// nothing was done, if nothing was.
func (w *MergePolicyWorker) Handle(ctx context.Context, a store.MergePolicyArgs) (bool, string, error) {
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return false, "", fmt.Errorf("worker: config: %w", err)
	}
	rc, ok := repoConfig(snap, a.Repo)
	switch {
	case !ok || !rc.Enabled:
		return false, "repo not opted in", nil
	case rc.ApprovalMode != gates.ApprovalMergePolicy:
		return false, "repo not in merge_policy_check mode", nil
	case rc.Shadow:
		return false, "shadow repo", nil
	}
	reader, err := w.cfg.GitHub.Reader(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return false, "", fmt.Errorf("worker: reader client: %w", err)
	}
	pr, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return false, "", fmt.Errorf("worker: get pull request: %w", err)
	}
	if pr.State != "open" {
		return false, "pull request is " + pr.State, nil
	}
	passed, err := w.cfg.Store.HeadAutoApproved(ctx, queries.HeadAutoApprovedParams{Repo: a.Repo, PrNumber: int32(a.PR), HeadSha: pr.HeadSHA})
	if err != nil {
		return false, "", fmt.Errorf("worker: head auto-approved: %w", err)
	}
	owner, name, _ := strings.Cut(a.Repo, "/")
	files, err := gh.PRFilePatches(ctx, reader, owner, name, a.PR)
	if err != nil {
		return false, "", fmt.Errorf("worker: list pr files: %w", err)
	}
	var touched []string
	for _, f := range files {
		touched = append(touched, f.Path)
		if f.PreviousPath != "" {
			touched = append(touched, f.PreviousPath)
		}
	}
	owners, err := baseOwners(ctx, reader, a.Repo, pr.BaseSHA)
	if err != nil {
		return false, "", err
	}
	posterClient, err := w.cfg.GitHub.Poster(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return false, "", fmt.Errorf("worker: poster client: %w", err)
	}
	target, err := gh.ParsePR(a.Repo, a.PR, pr.HeadSHA)
	if err != nil {
		return false, "", fmt.Errorf("worker: parse pr: %w", err)
	}
	gate := lazyGateFor(w.cfg.GitHub, a.InstallationID, a.Repo)
	member := func(ctx context.Context, team, login string) (bool, error) {
		c, err := gate(ctx)
		if err != nil {
			return false, err
		}
		return gh.TeamMember(ctx, c, team, login)
	}
	pass, err := reportMergePolicy(ctx, reader, member, gh.NewPoster(posterClient), target, mergePolicyInput{
		repo: a.Repo, author: pr.Author, number: a.PR, touched: touched, owners: owners, gatesPassed: passed,
	})
	return pass, "", err
}

// baseOwners reads the CODEOWNERS GitHub uses at the base commit.
func baseOwners(ctx context.Context, c *github.Client, repo, base string) (codeowners.File, error) {
	for _, loc := range codeowners.Locations {
		data, ok, err := gh.ReadFile(ctx, c, repo, base, loc)
		if err != nil {
			return codeowners.File{}, fmt.Errorf("worker: read %s: %w", loc, err)
		}
		if ok {
			return codeowners.Parse(string(data)), nil
		}
	}
	return codeowners.File{}, nil
}
