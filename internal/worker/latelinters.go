package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Late-CI linter dedupe: a configured linter whose check run
// completes after Exemplar posted is compared with the findings posted on
// that head. Each duplicate's comment is minimized as outdated, recorded as
// minimized / linter_duplicate, and gets a one-line reply naming the
// linter.

// LinterRecheckWait is how long a linter check waits for a review of the
// same head that is still queued or running, so it sees what that review
// posts.
const LinterRecheckWait = 30 * time.Second

// LinterCheckWorker runs linter_check jobs.
type LinterCheckWorker struct {
	river.WorkerDefaults[store.LinterCheckArgs]

	cfg Config
}

// NewLinterCheckWorker validates cfg; it needs GitHub, Store, Snapshot and
// Engine.
func NewLinterCheckWorker(cfg Config) (*LinterCheckWorker, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: linter checks need a store", errMissingDependency)
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &LinterCheckWorker{cfg: w.cfg}, nil
}

// LinterCheckOutcome is what one linter check did, for logs and tests.
type LinterCheckOutcome struct {
	// Ignored says why nothing was compared.
	Ignored   string
	Compared  int
	Minimized int
}

// Work implements river.Worker.
func (w *LinterCheckWorker) Work(ctx context.Context, job *river.Job[store.LinterCheckArgs]) error {
	a := job.Args
	out, err := w.Check(ctx, a)
	if errors.Is(err, errReviewRunning) {
		return river.JobSnooze(LinterRecheckWait)
	}
	if err != nil {
		return err
	}
	if out.Ignored == "" {
		w.cfg.Log.Info("late linter check", "repo", a.Repo, "pr", a.PR, "head", a.HeadSHA, "check", a.Check,
			"compared", out.Compared, "minimized", out.Minimized)
	}
	return nil
}

var errReviewRunning = errors.New("worker: a review of this head may still post")

// Check runs one linter check.
func (w *LinterCheckWorker) Check(ctx context.Context, a store.LinterCheckArgs) (LinterCheckOutcome, error) {
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return LinterCheckOutcome{}, fmt.Errorf("worker: config: %w", err)
	}
	rc, ok := repoConfig(snap, a.Repo)
	switch {
	case !ok:
		return LinterCheckOutcome{Ignored: "repo not configured"}, nil
	case rc.Shadow:
		return LinterCheckOutcome{Ignored: "repo in shadow: nothing posted"}, nil
	case !slices.ContainsFunc(rc.Linters, func(l string) bool { return strings.EqualFold(l, a.Check) }):
		return LinterCheckOutcome{Ignored: "not a configured linter"}, nil
	}
	if open, err := w.cfg.Store.ReviewJobOpen(ctx, a.Repo, a.PR, a.HeadSHA); err != nil {
		return LinterCheckOutcome{}, fmt.Errorf("worker: review job open: %w", err)
	} else if open {
		return LinterCheckOutcome{}, errReviewRunning
	}
	rows, err := w.cfg.Store.PostedFindingsAtHead(ctx, queries.PostedFindingsAtHeadParams{Repo: a.Repo, PrNumber: int32(a.PR), HeadSha: a.HeadSHA})
	if err != nil {
		return LinterCheckOutcome{}, fmt.Errorf("worker: posted findings: %w", err)
	}
	if len(rows) == 0 {
		return LinterCheckOutcome{Ignored: "nothing posted on this head"}, nil
	}
	target, err := gh.ParsePR(a.Repo, a.PR, a.HeadSHA)
	if err != nil {
		return LinterCheckOutcome{}, river.JobCancel(fmt.Errorf("worker: parse pr: %w", err))
	}
	c, err := w.cfg.GitHub.Poster(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return LinterCheckOutcome{}, fmt.Errorf("worker: poster client: %w", err)
	}
	anns, pending, err := gh.LinterAnnotations(ctx, c, a.Repo, a.HeadSHA, []string{a.Check})
	if err != nil {
		return LinterCheckOutcome{}, fmt.Errorf("worker: linter annotations: %w", err)
	}
	if len(pending) > 0 {
		// Re-run since this event: its own completion comes later.
		return LinterCheckOutcome{Ignored: "check is running again"}, nil
	}
	fs := make([]engine.Finding, len(rows))
	for i, r := range rows {
		fs[i] = engine.Finding{
			ID: r.ID, RuleID: r.RuleID, Lane: r.Lane, Severity: r.Severity, Message: r.Message, Fingerprint: r.Fingerprint,
			Anchor: scope.Anchor{Path: r.Path, Side: scope.Side(r.Side), StartLine: int(r.StartLine), EndLine: int(r.EndLine)},
		}
	}
	meta := engine.CallMeta{Caller: Caller, Repo: a.Repo}
	alerts, err := gh.CodeScanningAnnotations(ctx, c, a.Repo, a.PR, []string{a.Check})
	if err != nil {
		return LinterCheckOutcome{}, fmt.Errorf("worker: code scanning annotations: %w", err)
	}
	dups, trace := w.cfg.Engine.LinterDuplicates(ctx, meta, snap.Rules, fs, linterAnnotations(append(anns, alerts...)))
	out := LinterCheckOutcome{Compared: len(rows)}
	if trace.Error != "" {
		w.cfg.Log.Warn("late linter dedupe incomplete", "repo", a.Repo, "pr", a.PR, "err", trace.Error)
	}
	poster := gh.NewPoster(c)
	var errs []error
	dupIDs, noted := map[int64]bool{}, map[int64]bool{}
	for i := range dups {
		dupIDs[rows[i].ID] = true
	}
	for i := range rows {
		check, ok := dups[i]
		if !ok {
			continue
		}
		r := rows[i]
		// A class-of-bug group's comment is hidden only when every open
		// member is a linter duplicate; until then the member is recorded
		// alone (FR-C9).
		if r.GroupKey != nil && *r.GroupKey != "" {
			members, err := w.cfg.Store.GroupMembers(ctx, r.ID)
			if err != nil {
				errs = append(errs, fmt.Errorf("finding %d: group members: %w", r.ID, err))
				continue
			}
			if slices.ContainsFunc(members, func(id int64) bool { return !dupIDs[id] }) {
				if _, err := w.cfg.Store.MinimizeFinding(ctx, r.ID); err != nil {
					errs = append(errs, fmt.Errorf("finding %d: %w", r.ID, err))
				}
				continue
			}
		}
		// Minimize, then record, then reply: a retry re-minimizes (a no-op)
		// but never replies twice, since the finding is no longer posted.
		if err := poster.MinimizeComment(ctx, target, *r.GhCommentID); err != nil {
			errs = append(errs, fmt.Errorf("finding %d: %w", r.ID, err))
			continue
		}
		n, err := w.cfg.Store.MinimizeFinding(ctx, r.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("finding %d: %w", r.ID, err))
			continue
		}
		if n == 0 {
			continue // recorded by an earlier attempt
		}
		out.Minimized++
		if noted[*r.GhCommentID] {
			continue // a group's comment gets one note
		}
		noted[*r.GhCommentID] = true
		note := fmt.Sprintf("`%s` reports this too, so Exemplar has hidden its comment as a duplicate.", check)
		if err := poster.Reply(ctx, target, *r.GhCommentID, note); err != nil {
			w.cfg.Log.Warn("linter duplicate minimized without a note", "repo", a.Repo, "pr", a.PR, "finding", r.ID, "err", err)
		}
	}
	return out, errors.Join(errs...)
}
