package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// PlaygroundWorker runs playground_run jobs: the web UI's draft rule
// alone on one PR, as `exemplar try` does. Nothing is posted
// on the PR; the result is for the requester.
type PlaygroundWorker struct {
	river.WorkerDefaults[store.PlaygroundArgs]

	cfg Config
	// draft loads the deployed config with the draft rule file added.
	draft func(ruleYAML []byte) (*config.Snapshot, string, error)
}

// NewPlaygroundWorker validates cfg; it needs a store and the draft loader.
func NewPlaygroundWorker(cfg Config, draft func([]byte) (*config.Snapshot, string, error)) (*PlaygroundWorker, error) {
	if cfg.Store == nil || draft == nil {
		return nil, fmt.Errorf("%w: the playground needs a store and the config", errMissingDependency)
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &PlaygroundWorker{cfg: w.cfg, draft: draft}, nil
}

// Work implements river.Worker. An error is retried once, then recorded.
func (w *PlaygroundWorker) Work(ctx context.Context, job *river.Job[store.PlaygroundArgs]) error {
	status, err := w.Run(ctx, job.Args.ID)
	if err != nil && job.Attempt >= job.MaxAttempts {
		w.cfg.Log.Error("playground run failed", "id", job.Args.ID, "err", err)
		return w.finish(context.WithoutCancel(ctx), job.Args.ID, "failed", "the run failed: "+err.Error(), nil, 0)
	}
	if err != nil {
		return err
	}
	w.cfg.Log.Info("playground run", "id", job.Args.ID, "status", status)
	return nil
}

// Run runs one playground request and returns its final status.
func (w *PlaygroundWorker) Run(ctx context.Context, id int64) (string, error) {
	run, err := w.cfg.Store.GetPlaygroundRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", river.JobCancel(fmt.Errorf("worker: playground run %w", errGone))
	}
	if err != nil {
		return "", fmt.Errorf("worker: get playground run: %w", err)
	}
	if run.Status != "queued" {
		return run.Status, nil
	}
	snap, ruleID, err := w.draft([]byte(run.RuleYaml))
	if err != nil || ruleID != run.RuleID {
		reason := "the rule does not load with exemplar-config"
		if err != nil {
			reason += ": " + err.Error()
		}
		return "refused", w.finish(ctx, id, "refused", reason, nil, 0)
	}
	r := &playground.Runner{
		Engine: w.cfg.Engine, Scanner: w.cfg.Scanner, GitHub: playgroundGitHub{w}, WorkDir: w.cfg.WorkDir,
		CloneURL: w.cfg.CloneURL, Caller: "playground",
	}
	out, err := r.Try(ctx, playground.Request{
		Actor: run.Actor, Snapshot: snap, RuleIDs: []string{ruleID},
		Target: playground.Target{Repo: run.Repo, PR: int(run.PrNumber)},
	})
	if err != nil {
		return "", fmt.Errorf("worker: try playground: %w", err)
	}
	if out.Denied {
		return "refused", w.finish(ctx, id, "refused", "no access to "+run.Repo, nil, 0)
	}
	res := evaluate.TryResult{Ran: out.Ran, Findings: []evaluate.TryFinding{}}
	if !out.Ran {
		res.Note = strings.TrimSpace(strings.ReplaceAll(out.Reply, "`", ""))
	}
	for _, f := range out.Findings {
		res.Findings = append(res.Findings, evaluate.TryFinding{Path: f.Path, Start: f.Start, End: f.End, Message: f.Message, Kept: f.Kept, Reason: f.Reason})
	}
	return "done", w.finish(ctx, id, "done", "", &res, out.CostUSD)
}

func (w *PlaygroundWorker) finish(ctx context.Context, id int64, status, reason string, res *evaluate.TryResult, cost float64) error {
	var data []byte
	if res != nil {
		var err error
		if data, err = json.Marshal(res); err != nil {
			return fmt.Errorf("worker: marshal playground result: %w", err)
		}
	}
	var r *string
	if reason != "" {
		r = &reason
	}
	if err := w.cfg.Store.FinishPlaygroundRun(ctx, queries.FinishPlaygroundRunParams{ID: id, Status: status, Reason: r, Result: data, CostUsd: cost}); err != nil {
		return fmt.Errorf("worker: finish playground run: %w", err)
	}
	return nil
}

// playgroundGitHub is the playground's view of GitHub through the app.
// The requester's read access to the repo was checked by exemplar-api
// with their own token when the run was queued, so CanRead allows.
type playgroundGitHub struct{ w *PlaygroundWorker }

func (g playgroundGitHub) CanRead(context.Context, string, string) (bool, error) { return true, nil }

func (g playgroundGitHub) installation(ctx context.Context, repo string) (int64, error) {
	inst, err := g.w.cfg.Store.RepoInstallationID(ctx, &repo)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && inst == 0) {
		return 0, fmt.Errorf("%w: no installation on %s", gh.ErrNotFound, repo)
	}
	if err != nil {
		return 0, fmt.Errorf("worker: repo installation: %w", err)
	}
	return inst, nil
}

func (g playgroundGitHub) PullRequest(ctx context.Context, repo string, number int) (gh.PullRequest, error) {
	inst, err := g.installation(ctx, repo)
	if err != nil {
		return gh.PullRequest{}, err
	}
	var c *github.Client
	if c, err = g.w.cfg.GitHub.Reader(ctx, inst, repo); err != nil {
		return gh.PullRequest{}, fmt.Errorf("worker: reader client: %w", err)
	}
	pr, err := gh.GetPullRequest(ctx, c, repo, number)
	if err != nil {
		return gh.PullRequest{}, fmt.Errorf("worker: get pull request: %w", err)
	}
	return pr, nil
}

func (g playgroundGitHub) CloneToken(ctx context.Context, repo string) (string, error) {
	inst, err := g.installation(ctx, repo)
	if err != nil {
		return "", err
	}
	tok, err := g.w.cfg.GitHub.CloneToken(ctx, inst, repo)
	if err != nil {
		return "", fmt.Errorf("worker: clone token: %w", err)
	}
	return tok, nil
}
