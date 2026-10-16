package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/learn/evalgen"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// EvaluateWorker runs evaluate jobs: the full review of a
// working-tree diff on its base, for `exemplar review`. Nothing is
// posted or stored as findings; the requester reads the result from
// exemplar-api, and the diff is cleared when the job ends.
type EvaluateWorker struct {
	river.WorkerDefaults[store.EvaluateArgs]

	cfg Config
}

// NewEvaluateWorker validates cfg; it needs a store.
func NewEvaluateWorker(cfg Config) (*EvaluateWorker, error) {
	if cfg.Store == nil {
		return nil, errors.New("worker: evaluations need a store")
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &EvaluateWorker{cfg: w.cfg}, nil
}

// Timeout bounds one evaluation (mode eval runs many cases).
func (w *EvaluateWorker) Timeout(*river.Job[store.EvaluateArgs]) time.Duration { return time.Hour }

// Work implements river.Worker. An error is retried once; after the last
// attempt the evaluation is marked failed so the caller stops waiting.
func (w *EvaluateWorker) Work(ctx context.Context, job *river.Job[store.EvaluateArgs]) error {
	id, err := uuid.Parse(job.Args.ID)
	if err != nil {
		return river.JobCancel(fmt.Errorf("%w: %w", errBadJob, err))
	}
	st, err := w.Evaluate(ctx, id)
	if err != nil && job.Attempt >= job.MaxAttempts {
		w.cfg.Log.Error("evaluation failed", "id", id, "err", err)
		return w.finish(context.WithoutCancel(ctx), id, evaluate.StatusFailed, "the review failed: "+err.Error(), nil, 0)
	}
	if err != nil {
		return err
	}
	w.cfg.Log.Info("evaluation done", "id", id, "status", st)
	return nil
}

// Evaluate runs one evaluation and returns its final status.
func (w *EvaluateWorker) Evaluate(ctx context.Context, id uuid.UUID) (string, error) {
	q := w.cfg.Store.Queries
	row, err := q.StartEvaluation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", river.JobCancel(errors.New("worker: evaluation already finished or gone"))
	}
	if err != nil {
		return "", err
	}
	refuse := func(reason string) (string, error) {
		return evaluate.StatusRefused, w.finish(ctx, id, evaluate.StatusRefused, reason, nil, 0)
	}
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("worker: config: %w", err)
	}
	// exemplar-config is not a reviewed repo: mode eval checks each eval
	// case's repo instead.
	if row.Mode == evaluate.ModeEval {
		return w.eval(ctx, id, row, snap, refuse)
	}
	// Nothing about an ineligible repo reaches a model provider, and
	// nothing is fetched for it.
	if err := snap.CheckEligible(row.Repo); err != nil {
		return refuse("the repo is not eligible for review: " + err.Error())
	}
	repo := row.Repo
	inst, err := q.RepoInstallationID(ctx, &repo)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && inst == 0) {
		return refuse("Exemplar's app is not installed on " + row.Repo)
	}
	if err != nil {
		return "", err
	}
	token, err := w.cfg.GitHub.CloneToken(ctx, inst, row.Repo)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "evaluate-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "worktree"), engine.CheckoutOptions{
		CloneURL: w.cfg.CloneURL(row.Repo), BaseSHA: row.BaseSha, HeadSHA: row.BaseSha, Token: token,
	})
	if err != nil {
		return refuse("the base commit is not on GitHub; push it, or review against a pushed commit with --base")
	}
	if err := ws.ApplyPatch(ctx, []byte(row.Diff)); err != nil {
		return refuse(err.Error())
	}
	info := engine.PRInfo{
		Repo: row.Repo, Title: "exemplar review", Author: row.RequestedBy, AuthorType: "User",
		BaseSHA: row.BaseSha, HeadSHA: ws.HeadSHA, HeadRepo: row.Repo,
	}
	p, err := engine.Prepare(ctx, ws, snap, info, w.cfg.Scanner)
	if errors.Is(err, engine.ErrIneligible) {
		return refuse(err.Error())
	}
	if err != nil {
		return "", err
	}
	res, err := w.cfg.Engine.Review(ctx, p, engine.Options{Caller: row.Caller})
	if err != nil {
		return "", err
	}
	out, cost := Result(res)
	return evaluate.StatusDone, w.finish(ctx, id, evaluate.StatusDone, "", &out, cost)
}

// Result is what an evaluation returns from a review: the findings that
// would have been posted, never shadow rules' or held-back ones.
func Result(res *engine.Result) (evaluate.Result, float64) {
	out := evaluate.Result{Findings: []evaluate.Finding{}, Coverage: res.Coverage, Degraded: res.Degraded, Incomplete: res.Incomplete, HeldBack: res.HeldBack}
	cost := 0.0
	rules := map[string]bool{}
	for _, r := range res.Runs {
		cost += r.CostUSD
		if r.RuleID != "" {
			rules[r.RuleID] = true
		}
	}
	out.Rules = len(rules)
	for _, f := range res.Findings {
		if f.State != engine.StatePosted {
			continue
		}
		ef := evaluate.Finding{
			RuleID: f.RuleID, Lane: f.Lane, Severity: f.Severity, Category: f.Category, Path: f.Anchor.Path, Side: string(f.Anchor.Side),
			StartLine: f.Anchor.StartLine, EndLine: f.Anchor.EndLine, Message: f.Message,
		}
		if f.FixValid != nil && *f.FixValid {
			ef.Suggestion = f.Fix
		}
		out.Findings = append(out.Findings, ef)
	}
	return out, cost
}

func (w *EvaluateWorker) finish(ctx context.Context, id uuid.UUID, status, reason string, res *evaluate.Result, cost float64) error {
	var data []byte
	if res != nil {
		var err error
		if data, err = json.Marshal(res); err != nil {
			return err
		}
	}
	var r *string
	if reason != "" {
		r = &reason
	}
	return w.cfg.Store.FinishEvaluation(ctx, queries.FinishEvaluationParams{ID: id, Status: status, Reason: r, Result: data, CostUsd: cost})
}

// installation finds a repo's installation from its webhook deliveries.
func (w *EvaluateWorker) installation(ctx context.Context, repo string) (int64, error) {
	inst, err := w.cfg.Store.RepoInstallationID(ctx, &repo)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && inst == 0) {
		return 0, fmt.Errorf("exemplar's app is not installed on %s", repo)
	}
	return inst, err
}

// eval is mode eval: the rules a config PR changes, on their
// eval cases at its head, as `exemplar eval` runs them. Eligibility comes
// from the deployed config, so a config PR cannot un-denylist a repo to
// evaluate on it.
func (w *EvaluateWorker) eval(ctx context.Context, id uuid.UUID, row queries.StartEvaluationRow, snap *config.Snapshot, refuse func(string) (string, error)) (string, error) {
	if w.cfg.ConfigRepo == "" || !strings.EqualFold(row.Repo, w.cfg.ConfigRepo) {
		return refuse("mode eval evaluates exemplar-config only")
	}
	inst, err := w.installation(ctx, row.Repo)
	if err != nil {
		return refuse(err.Error())
	}
	token, err := w.cfg.GitHub.CloneToken(ctx, inst, row.Repo)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "eval-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "config"), engine.CheckoutOptions{
		CloneURL: w.cfg.CloneURL(row.Repo), BaseSHA: row.BaseSha, HeadSHA: row.HeadSha, Token: token,
	})
	if err != nil {
		return refuse("the base or head commit is not on GitHub; push the branch")
	}
	head, err := config.LoadSnapshot(os.DirFS(ws.Dir), row.HeadSha)
	if err != nil {
		return refuse("the config at the head does not load: " + err.Error())
	}
	changed, err := ws.ChangedSince(ctx, row.BaseSha)
	if err != nil {
		return "", err
	}
	ids := append(evalrun.ChangedRules(head, changed), row.Rules...)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	ev := &evalrun.Evaluator{
		Snapshot: head, Eligible: func(repo string) bool { return snap.CheckEligible(repo) == nil },
		Engine: w.cfg.Engine, Matcher: w.cfg.Matcher, Scanner: w.cfg.Scanner, WorkDir: filepath.Join(dir, "cases"),
		Files: &evalgen.CheckoutFiles{
			Root: filepath.Join(dir, "checkouts"), CloneURL: w.cfg.CloneURL,
			Token: func(ctx context.Context, repo string) (string, error) {
				inst, err := w.installation(ctx, repo)
				if err != nil {
					return "", err
				}
				return w.cfg.GitHub.CloneToken(ctx, inst, repo)
			},
		},
	}
	reports := []evalrun.Report{}
	cost := 0.0
	pass := true
	for _, rid := range ids {
		rep, err := evalrun.EvaluateRule(ctx, ev, head, os.DirFS(ws.Dir), rid)
		if err != nil {
			return "", fmt.Errorf("eval %s: %w", rid, err)
		}
		reports = append(reports, rep)
		cost += rep.CostUSD
		pass = pass && rep.Pass
	}
	raw, err := json.Marshal(reports)
	if err != nil {
		return "", err
	}
	md := evalrun.Markdown(reports, head.Global.EvalThresholds)
	if len(ids) == 0 {
		md = "### Exemplar eval\n\nNo rules to evaluate.\n"
	}
	res := evaluate.Result{Findings: []evaluate.Finding{}, Eval: &evaluate.EvalResult{Reports: raw, Markdown: md, Pass: pass, Rules: append([]string{}, ids...)}}
	return evaluate.StatusDone, w.finish(ctx, id, evaluate.StatusDone, "", &res, cost)
}
