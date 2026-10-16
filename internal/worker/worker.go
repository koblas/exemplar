// Package worker reviews live pull requests: the pr_review River job
// (spec §5 orchestrator, worker and poster, run as one process for now).
// For one PR head it reads the PR from GitHub, checks it out, gathers the
// facts the auto-approve gates need (author, head repo, commits, merge
// safety, branch rules, team membership), runs the engine, persists the
// result, posts the review and check run, and approves only when every
// gate passed.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/mergesafety"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/telemetry"
)

// Caller is reviews.caller for live reviews.
const Caller = "production"

// GitHub mints the scoped clients a review needs (spec §5 token table);
// AppClients implements it over the GitHub App.
type GitHub interface {
	// Reader reads the PR repo (FlavorWorker).
	Reader(ctx context.Context, installationID int64, repo string) (*github.Client, error)
	// Gate reads branch rules and team membership (FlavorGate).
	Gate(ctx context.Context, installationID int64, repo string) (*github.Client, error)
	// Poster writes reviews and check runs (FlavorPoster).
	Poster(ctx context.Context, installationID int64, repo string) (*github.Client, error)
	// CloneToken is a read-only token for cloning the repo.
	CloneToken(ctx context.Context, installationID int64, repo string) (string, error)
}

// Config wires a Worker.
type Config struct {
	GitHub GitHub
	// Store persists reviews and answers merge safety. Required in
	// production; nil only in tests, which then persist nothing and
	// measure no merge safety (so Stage 2 fails closed).
	Store *store.Store
	// Snapshot returns the current exemplar-config snapshot.
	Snapshot func(ctx context.Context) (*config.Snapshot, error)
	Engine   *engine.Engine
	Scanner  *redact.Scanner
	// WorkDir holds one checkout per running review.
	WorkDir string
	// CloneURL overrides https://github.com/<repo>.git (tests).
	CloneURL func(repo string) string
	Log      *slog.Logger
	Now      func() time.Time
	// Links, when set, signs feedback links into each comment (spec §6.12).
	Links *feedbacklink.Signer
	// LinterPoll is how often a review re-reads the linters' check runs
	// while it waits for them (default 10 s).
	LinterPoll time.Duration
	// ConfigRepo is exemplar-config (owner/name), the only repo mode eval
	// evaluates; empty turns mode eval off.
	ConfigRepo string
	// Matcher runs compiled rules for mode eval (compiled.Runner).
	Matcher evalrun.Matcher
}

// Worker runs pr_review jobs.
type Worker struct {
	river.WorkerDefaults[store.PRReviewArgs]

	cfg Config
}

// New validates cfg.
func New(cfg Config) (*Worker, error) {
	switch {
	case cfg.GitHub == nil:
		return nil, errors.New("worker: GitHub is required")
	case cfg.Snapshot == nil:
		return nil, errors.New("worker: Snapshot is required")
	case cfg.Engine == nil || cfg.Scanner == nil:
		return nil, errors.New("worker: Engine and Scanner are required")
	case cfg.WorkDir == "":
		return nil, errors.New("worker: WorkDir is required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.LinterPoll <= 0 {
		cfg.LinterPoll = 10 * time.Second
	}
	if cfg.CloneURL == nil {
		cfg.CloneURL = func(repo string) string { return "https://github.com/" + repo + ".git" }
	}
	return &Worker{cfg: cfg}, nil
}

// Timeout bounds one review job; River cancels its context after it.
func (w *Worker) Timeout(*river.Job[store.PRReviewArgs]) time.Duration { return 30 * time.Minute }

// Work implements river.Worker. A bad job is cancelled, not retried.
func (w *Worker) Work(ctx context.Context, job *river.Job[store.PRReviewArgs]) error {
	// Per-repo fairness (FR-D6): wait for one of the repo's slots.
	if w.cfg.Store != nil {
		snap, err := w.cfg.Snapshot(ctx)
		if err != nil {
			return err
		}
		release, ok, err := NewSlots(w.cfg.Store.Pool).Acquire(ctx, job.Args.Repo, RepoLimit(snap, job.Args.Repo))
		if err != nil {
			return err
		}
		if !ok {
			w.cfg.Log.Debug("repo at its review limit; waiting", "repo", job.Args.Repo, "pr", job.Args.PR)
			return river.JobSnooze(SlotWait)
		}
		defer release()
	}
	start := time.Now()
	out, err := w.Review(ctx, job.Args)
	outcome := out.Status
	if err != nil {
		outcome = "error"
	}
	telemetry.ReviewDone(ctx, outcome, time.Since(start))
	if err == nil && out.Posted > 0 {
		telemetry.QueueToPost(ctx, time.Since(job.CreatedAt))
	}
	if errors.Is(err, errBadJob) {
		return river.JobCancel(err)
	}
	if errors.Is(err, rivertype.ErrJobCancelledRemotely) {
		w.cfg.Log.Info("review superseded", "repo", job.Args.Repo, "pr", job.Args.PR, "head", job.Args.HeadSHA, "review_id", out.ReviewID)
		return err
	}
	if err != nil {
		w.cfg.Log.Error("review failed", "repo", job.Args.Repo, "pr", job.Args.PR, "head", job.Args.HeadSHA, "attempt", job.Attempt, "err", err)
		return err
	}
	w.cfg.Log.Info("review done", "repo", job.Args.Repo, "pr", job.Args.PR, "head", job.Args.HeadSHA,
		"outcome", out.Status, "review_id", out.ReviewID, "decision", out.Decision, "posted", out.Posted, "closed", out.Closed, "moved", out.Moved, "shadow", out.Shadow)
	return nil
}

var errBadJob = errors.New("worker: invalid job")

// deletedFork stands in for the head repo of a PR whose fork was deleted.
const deletedFork = "(deleted fork)"

// Outcome statuses.
const (
	StatusReviewed   = "reviewed"
	StatusIncomplete = "incomplete"
	StatusSuperseded = "superseded" // the PR's head moved on
	StatusClosed     = "closed"
	StatusIneligible = "ineligible"
)

// Outcome is what one job did.
type Outcome struct {
	Status   string
	Reason   string
	ReviewID int64
	Decision string
	Posted   int
	Approved bool
	// Dismissed counts earlier Exemplar approvals dismissed (FR-A9).
	Dismissed int
	// Closed counts earlier findings this head resolved or outdated.
	Closed int
	// Moved counts findings re-anchored after a rebase (FR-C4).
	Moved int
	// Shadow is set when the repo is reviewed in shadow: nothing posted.
	Shadow bool
	// LinterDuplicates counts findings withheld as linter duplicates.
	LinterDuplicates int
	// HumanDuplicates counts findings withheld because a reviewer
	// already raised them.
	HumanDuplicates int
	// AppliedSuggestions counts suggestions found committed on this head.
	AppliedSuggestions int
	// SARIF is set when Security findings were uploaded as code scanning.
	SARIF bool
	// Fixed counts findings this head addressed, as the light model judged.
	Fixed int
}

// Review reviews one PR head.
func (w *Worker) Review(ctx context.Context, a store.PRReviewArgs) (Outcome, error) {
	if a.Repo == "" || a.PR < 1 || a.HeadSHA == "" || a.InstallationID == 0 || !strings.Contains(a.Repo, "/") {
		return Outcome{}, fmt.Errorf("%w: %+v", errBadJob, a)
	}
	reader, err := w.cfg.GitHub.Reader(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return Outcome{}, err
	}
	pr, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return Outcome{}, err
	}
	switch {
	case pr.State != "open":
		return Outcome{Status: StatusClosed}, nil
	case pr.HeadSHA != a.HeadSHA:
		// A newer push has its own job; this head is superseded.
		return Outcome{Status: StatusSuperseded, Reason: "head is now " + short(pr.HeadSHA)}, nil
	}
	target, err := gh.ParsePR(a.Repo, a.PR, a.HeadSHA)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %w", errBadJob, err)
	}
	posterClient, err := w.cfg.GitHub.Poster(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return Outcome{}, err
	}
	poster := gh.NewPoster(posterClient)

	// FR-A9: dismiss approvals on earlier heads before reviewing this one.
	out := Outcome{}
	if out.Dismissed, err = poster.DismissApprovals(ctx, target, "New commits were pushed; Exemplar re-reviews before approving again."); err != nil {
		return out, err
	}

	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return out, fmt.Errorf("worker: config: %w", err)
	}
	commits, err := gh.PRCommitMessages(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return out, err
	}
	token, err := w.cfg.GitHub.CloneToken(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return out, err
	}
	dir, err := os.MkdirTemp(w.cfg.WorkDir, "review-")
	if err != nil {
		return out, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "worktree"), engine.CheckoutOptions{
		CloneURL: w.cfg.CloneURL(a.Repo), BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA, Token: token,
	})
	if err != nil {
		return out, err
	}
	headRepo := pr.HeadRepo
	if headRepo == "" {
		headRepo = deletedFork // the fork was deleted: still a fork
	}
	info := engine.PRInfo{
		Repo: a.Repo, Number: a.PR, Title: pr.Title, Body: pr.Body, Author: pr.Author, Draft: pr.Draft,
		BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA,
		AuthorType: pr.AuthorType, HeadRepo: headRepo, CommitMessages: commits, Labels: pr.Labels,
	}
	p, err := engine.Prepare(ctx, ws, snap, info, w.cfg.Scanner)
	if errors.Is(err, engine.ErrIneligible) {
		// Spec §6.2: no review, and a neutral check run says why; but a
		// shadow repo's PRs get nothing at all.
		out.Status, out.Reason = StatusIneligible, err.Error()
		if rc, ok := repoConfig(snap, a.Repo); ok && rc.Shadow {
			return out, nil
		}
		return out, w.neutralCheck(ctx, poster, target, "Not reviewed", ineligibleText(err))
	}
	if err != nil {
		return out, err
	}

	rec, err := pipeline.Begin(ctx, w.cfg.Store, p, Caller, a.Attempt, p.Scoped)
	if err != nil {
		return out, err
	}
	rec.Links = w.cfg.Links
	out.ReviewID = rec.ReviewID
	// A shadow repo (spec §6.9) is reviewed in full and stored, but
	// nothing is written to its PRs: no check run, comments, replies or
	// approval. Revoking an earlier Exemplar approval (above) still happens.
	shadow := p.Effective.RepoConfig.Shadow
	out.Shadow = shadow
	var checkID int64
	if !shadow {
		if checkID, err = poster.StartCheck(ctx, target, pipeline.CheckExternalID(rec.ReviewID)); err != nil {
			return w.failed(ctx, poster, target, 0, rec, out, err)
		}
	}

	opt := engine.Options{ReviewID: rec.ReviewID, Caller: Caller, RuleRunIDs: rec.RunIDs}
	// Findings earlier pushes posted: not posted again, and closed below
	// when this head settles them (spec §6.2).
	var open []pipeline.OpenFinding
	if w.cfg.Store != nil {
		if open, err = w.cfg.Store.OpenFindings(ctx, queries.OpenFindingsParams{Repo: a.Repo, PrNumber: int32(a.PR)}); err != nil {
			return w.failed(ctx, poster, target, checkID, rec, out, err)
		}
		if !shadow {
			open = w.reanchor(ctx, reader, poster, target, p, open, &out)
		}
		// Suggestions committed since they were posted (spec §6.11). A
		// failure loses a signal, not the review.
		if n, err := pipeline.RecordAppliedSuggestions(ctx, w.cfg.Store, p, pipeline.AppliedSuggestions(ctx, p, open)); err != nil {
			w.cfg.Log.Warn("applied suggestions not all recorded", "repo", a.Repo, "pr", a.PR, "err", err)
		} else {
			out.AppliedSuggestions = n
		}
		opt.PostedFingerprints = map[string]bool{}
		for _, f := range open {
			opt.PostedFingerprints[f.Fingerprint] = true
		}
	}
	opt.Carry, opt.CarryFrom = w.carry(ctx, p, open)
	opt.MergeSafety = w.mergeSafety(ctx, reader, p)
	gate := w.lazyGate(a)
	opt.BranchRules = func(ctx context.Context) (gates.BranchRules, error) {
		c, err := gate(ctx)
		if err != nil {
			return gates.BranchRules{}, err
		}
		return gh.BranchRules(ctx, c, a.Repo, pr.BaseRef)
	}
	opt.TeamMember = func(ctx context.Context, team, login string) (bool, error) {
		c, err := gate(ctx)
		if err != nil {
			return false, err
		}
		return gh.TeamMember(ctx, c, team, login)
	}

	// Linter dedupe reads other check runs, which only the poster's
	// checks permission covers. Reading writes nothing, so a shadow repo
	// dedupes too, and shadow precision counts what would have posted.
	opt.Linters = w.linters(posterClient, a.Repo, a.PR, a.HeadSHA, p.Effective.RepoConfig.Linters, snap.Global.LinterWaitSeconds())
	opt.HumanComments = func(ctx context.Context) ([]engine.HumanComment, error) {
		cs, err := gh.PRComments(ctx, reader, a.Repo, a.PR)
		out := make([]engine.HumanComment, 0, len(cs))
		for _, c := range cs {
			out = append(out, engine.HumanComment{
				ID: c.ID, Author: c.Author, AuthorType: c.AuthorType, Association: c.Association,
				Path: c.Path, Side: c.Side, Start: c.Start, End: c.End, Body: c.Body,
			})
		}
		return out, err
	}

	res, err := w.cfg.Engine.Review(ctx, p, opt)
	if err != nil {
		// Superseded (cleaned up below), or shut down (River retries).
		return w.failed(ctx, poster, target, checkID, rec, out, err)
	}
	if err := pipeline.Finish(ctx, w.cfg.Store, rec, res); err != nil {
		return w.failed(ctx, poster, target, checkID, rec, out, err)
	}
	out.Decision = res.Decision()
	if lt := res.Linters; lt != nil {
		out.LinterDuplicates = lt.Duplicates
		if lt.Error != "" {
			w.cfg.Log.Warn("linter dedupe skipped", "repo", a.Repo, "pr", a.PR, "err", lt.Error)
		}
	}
	if ht := res.Humans; ht != nil {
		out.HumanDuplicates = ht.Duplicates
		if ht.Error != "" {
			w.cfg.Log.Warn("human-comment dedupe skipped", "repo", a.Repo, "pr", a.PR, "err", ht.Error)
		}
	}

	if res.Incomplete != "" {
		// Spec §6.15: no comments, no approval; the check says why.
		out.Status, out.Reason = StatusIncomplete, res.Incomplete
		if shadow {
			return out, nil
		}
		w.mergePolicy(ctx, reader, opt.TeamMember, poster, target, p, false)
		return out, poster.CompleteCheck(ctx, target, checkID, gh.CheckResult{
			Conclusion: "neutral", Title: "Review did not complete", Incomplete: true,
			Summary: "Exemplar could not complete this review: " + res.Incomplete + ". Nothing was posted; use Re-run to try again.",
		})
	}
	if shadow {
		out.Status = StatusReviewed
		return out, nil
	}
	// A push during the review makes its comments stale: post nothing.
	// The routing job normally cancels this review first; this catches a
	// cancel that has not arrived yet.
	if now, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR); err != nil {
		return w.failed(ctx, poster, target, checkID, rec, out, err)
	} else if now.HeadSHA != a.HeadSHA || now.State != "open" {
		return w.supersede(ctx, poster, target, checkID, rec, out, nil)
	}
	po, err := pipeline.Post(ctx, w.cfg.Store, poster, p, rec, res, checkID)
	if err != nil {
		return w.failed(ctx, poster, target, checkID, rec, out, err)
	}
	out.Status, out.Posted = StatusReviewed, po.Comments
	// Security findings as code-scanning alerts (spec §6.12). The review
	// is posted, so a failure is logged, not retried.
	if out.SARIF, err = pipeline.UploadSARIF(ctx, w.cfg.Store, poster, p, res); err != nil {
		w.cfg.Log.Warn("SARIF not uploaded", "repo", a.Repo, "pr", a.PR, "err", err)
	}

	// Close what this head settles. The review is already posted, so a
	// failure here is logged, not retried; the next push tries again.
	closures := pipeline.PlanClosures(open, p, res)
	n, err := pipeline.CloseFindings(ctx, w.cfg.Store, poster, target, closures)
	out.Closed += n // after any re-anchoring closures
	if err != nil {
		w.cfg.Log.Warn("earlier findings not all closed", "repo", a.Repo, "pr", a.PR, "closed", n, "err", err)
	}
	// A finding addressed without its suggestion: the light model checks
	// the change (spec §9.1). A lost signal does not fail the review.
	if out.Fixed, err = w.recordFixed(ctx, p, closures); err != nil {
		w.cfg.Log.Warn("fixed findings not all recorded", "repo", a.Repo, "pr", a.PR, "err", err)
	}

	switch {
	case p.Effective.RepoConfig.ApprovalMode == gates.ApprovalMergePolicy:
		out.Approved = w.mergePolicy(ctx, reader, opt.TeamMember, poster, target, p, out.Decision == gates.DecisionApproved)
	case out.Decision == gates.DecisionApproved:
		if out.Approved, err = w.approve(ctx, reader, poster, target, p, rec.ReviewID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// mergePolicy reports exemplar/merge-policy for a merge_policy_check
// repo's reviewed head. The review is posted, so a failure is logged; the
// next review event decides the check again.
func (w *Worker) mergePolicy(ctx context.Context, reader *github.Client, member gates.MemberFunc, poster *gh.Poster, target gh.PR, p *engine.Prepared, gatesPassed bool) bool {
	if p.Effective.RepoConfig.ApprovalMode != gates.ApprovalMergePolicy {
		return false
	}
	pass, err := reportMergePolicy(ctx, reader, member, poster, target, mergePolicyInput{
		repo: p.PR.Repo, author: p.PR.Author, number: p.PR.Number, touched: p.Touched(), owners: p.CodeOwners, gatesPassed: gatesPassed,
	})
	if err != nil {
		w.cfg.Log.Warn("merge policy not reported", "repo", p.PR.Repo, "pr", p.PR.Number, "err", err)
		return false
	}
	return pass && gatesPassed
}

// linters returns the engine's linter-dedupe source (spec §6.10): it
// re-reads the configured linters' check runs on sha every LinterPoll until
// all have completed or wait has passed, then returns the completed runs'
// annotations, with the PR's open code-scanning alerts from tools of the
// same names (spec §6.10). Nil when the repo configures no linters.
func (w *Worker) linters(c *github.Client, repo string, pr int, sha string, checks []string, wait int) func(context.Context) ([]engine.LinterAnnotation, error) {
	if len(checks) == 0 {
		return nil
	}
	return func(ctx context.Context) ([]engine.LinterAnnotation, error) {
		deadline := time.NewTimer(time.Duration(max(wait, 0)) * time.Second)
		defer deadline.Stop()
		for {
			anns, pending, err := gh.LinterAnnotations(ctx, c, repo, sha, checks)
			if err != nil {
				return nil, err
			}
			if len(pending) == 0 || wait <= 0 {
				return withAlerts(ctx, c, repo, pr, checks, anns)
			}
			poll := time.NewTimer(w.cfg.LinterPoll)
			select {
			case <-ctx.Done():
				poll.Stop()
				return nil, ctx.Err()
			case <-deadline.C:
				poll.Stop()
				// Post with what has reported; a linter that completes
				// later is a linter_check job (latelinters.go).
				anns, _, err := gh.LinterAnnotations(ctx, c, repo, sha, checks)
				if err != nil {
					return nil, err
				}
				return withAlerts(ctx, c, repo, pr, checks, anns)
			case <-poll.C:
			}
		}
	}
}

// maxFixedJudgements bounds the fixed-without-suggestion judge per review.
const maxFixedJudgements = 10

// recordFixed records fixed for findings this head closed as addressed
// when the light model confirms the change addresses them, unless their
// suggestion was applied (already recorded as applied_suggestion).
func (w *Worker) recordFixed(ctx context.Context, p *engine.Prepared, closures []pipeline.Closure) (int, error) {
	if w.cfg.Store == nil {
		return 0, nil
	}
	var evs []pipeline.Event
	judged := 0
	for _, c := range closures {
		o := c.Finding
		if c.Reason != pipeline.ClosedAddressed || o.AnchorCode == nil || judged == maxFixedJudgements {
			continue
		}
		applied, err := w.cfg.Store.FindingHasEventKind(ctx, queries.FindingHasEventKindParams{FindingID: o.ID, Kind: pipeline.EventAppliedSuggestion})
		if err != nil || applied {
			if err != nil {
				return 0, err
			}
			continue
		}
		d, err := w.cfg.Store.FindingDetail(ctx, o.ID)
		if err != nil {
			return 0, err
		}
		after, err := p.Workspace.Show(ctx, p.PR.HeadSHA, o.Path)
		if err != nil {
			continue // the file is gone: not this judge's case
		}
		judged++
		if w.cfg.Engine.ChangeAddresses(ctx, engine.CallMeta{Caller: Caller, Repo: p.PR.Repo}, findingOf(d), *o.AnchorCode, window(string(after), int(o.StartLine), int(o.EndLine), 3)) {
			evs = append(evs, pipeline.Event{
				FindingID: o.ID, Kind: "fixed", Actor: p.PR.Author, Role: pipeline.RoleAuthor, Via: pipeline.ViaInferred,
				Evidence: map[string]any{"head_sha": p.PR.HeadSHA},
			})
		}
	}
	return pipeline.RecordEvents(ctx, w.cfg.Store, p.Effective.Global.Feedback, evs)
}

// window is lines start-ctx..end+ctx of text (1-based, clamped).
func window(text string, start, end, ctx int) string {
	lines := strings.Split(text, "\n")
	from, to := max(start-ctx, 1), min(end+ctx, len(lines))
	if from > to {
		return ""
	}
	return strings.Join(lines[from-1:to], "\n")
}

// withAlerts adds the PR's open code-scanning alerts from the configured
// tools to anns.
func withAlerts(ctx context.Context, c *github.Client, repo string, pr int, tools []string, anns []gh.Annotation) ([]engine.LinterAnnotation, error) {
	alerts, err := gh.CodeScanningAnnotations(ctx, c, repo, pr, tools)
	if err != nil {
		return linterAnnotations(anns), err
	}
	return linterAnnotations(append(anns, alerts...)), nil
}

func linterAnnotations(in []gh.Annotation) []engine.LinterAnnotation {
	out := make([]engine.LinterAnnotation, 0, len(in))
	for _, a := range in {
		out = append(out, engine.LinterAnnotation{Check: a.Check, Path: a.Path, Start: a.Start, End: a.End, Title: a.Title, Message: a.Message})
	}
	return out
}

// failed returns err, first cleaning up after a review a newer push
// superseded (River cancelled its job remotely).
func (w *Worker) failed(ctx context.Context, poster *gh.Poster, target gh.PR, checkID int64, rec *pipeline.Record, out Outcome, err error) (Outcome, error) {
	if errors.Is(context.Cause(ctx), rivertype.ErrJobCancelledRemotely) {
		return w.supersede(ctx, poster, target, checkID, rec, out, context.Cause(ctx))
	}
	return out, err
}

// supersede records a superseded review and completes its check run, so
// the old head does not keep an in-progress check (spec §6.2). It runs on
// a fresh, short context: the job's own may already be cancelled. cause is
// returned as the job's error (nil when the review noticed the push itself).
func (w *Worker) supersede(ctx context.Context, poster *gh.Poster, target gh.PR, checkID int64, rec *pipeline.Record, out Outcome, cause error) (Outcome, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	out.Status, out.Reason = StatusSuperseded, "a newer push or a closed PR replaced this head"
	if w.cfg.Store != nil && rec.ReviewID != 0 {
		if err := w.cfg.Store.MarkReviewSuperseded(cctx, rec.ReviewID); err != nil {
			w.cfg.Log.Error("superseded review not recorded", "review_id", rec.ReviewID, "err", err)
		}
	}
	if checkID != 0 {
		if err := poster.CompleteCheck(cctx, target, checkID, gh.CheckResult{
			Conclusion: "neutral", Title: "Superseded",
			Summary: "A newer push replaced this commit (or the pull request closed); Exemplar reviews the new head instead. Nothing was posted for this one.",
		}); err != nil {
			w.cfg.Log.Warn("superseded check not completed", "review_id", rec.ReviewID, "err", err)
		}
	}
	return out, cause
}

// approve submits the APPROVE for a native_approve repo, after checking
// the head has not moved during the review (an approval must never land
// on a newer push than the one reviewed).
func (w *Worker) approve(ctx context.Context, reader *github.Client, poster *gh.Poster, target gh.PR, p *engine.Prepared, reviewID int64) (bool, error) {
	if mode := p.Effective.RepoConfig.ApprovalMode; mode != "" && mode != gates.ApprovalNative {
		w.cfg.Log.Warn("approval mode not native; not approving", "repo", target.Owner+"/"+target.Repo, "pr", target.Number, "mode", mode)
		return false, nil
	}
	now, err := gh.GetPullRequest(ctx, reader, p.PR.Repo, p.PR.Number)
	if err != nil {
		return false, err
	}
	if now.HeadSHA != target.HeadSHA || now.State != "open" {
		return false, nil
	}
	_, _, err = poster.Approve(ctx, target, reviewID, "Exemplar found nothing to flag, and every auto-approve gate passed.")
	return err == nil, err
}

// mergeSafety scores the PR (spec §6.13), or returns nil when it cannot:
// Stage 2 then fails closed rather than the review failing.
func (w *Worker) mergeSafety(ctx context.Context, reader *github.Client, p *engine.Prepared) *gates.MergeSafetyResult {
	if w.cfg.Store == nil {
		return nil
	}
	var configured map[string]float64
	if ms := p.Effective.Global.MergeSafety; ms != nil {
		configured = ms.Weights
	}
	weights, err := gates.Weights(configured)
	if err != nil {
		w.cfg.Log.Error("merge safety weights", "err", err)
		return nil
	}
	query := func(ctx context.Context, q string, vars map[string]any, out any) error {
		return gh.GraphQL(ctx, reader, q, vars, out)
	}
	c, err := mergesafety.New(w.cfg.Store, query, w.cfg.Now)
	if err != nil {
		w.cfg.Log.Error("merge safety", "err", err)
		return nil
	}
	res, err := c.Score(ctx, mergesafety.PR{Repo: p.PR.Repo, Number: p.PR.Number, Author: p.PR.Author, Paths: p.Touched()},
		p.Effective.RepoConfig.SensitivePaths, weights)
	if err != nil {
		w.cfg.Log.Warn("merge safety not measured", "repo", p.PR.Repo, "pr", p.PR.Number, "err", err)
		return nil
	}
	return &res
}

// lazyGate mints the gate client once, on first use: most reviews never
// reach a branch-rules or team lookup.
func (w *Worker) lazyGate(a store.PRReviewArgs) func(context.Context) (*github.Client, error) {
	return lazyGateFor(w.cfg.GitHub, a.InstallationID, a.Repo)
}

func lazyGateFor(g GitHub, installationID int64, repo string) func(context.Context) (*github.Client, error) {
	var (
		once sync.Once
		c    *github.Client
		err  error
	)
	return func(ctx context.Context) (*github.Client, error) {
		once.Do(func() { c, err = g.Gate(ctx, installationID, repo) })
		return c, err
	}
}

func (w *Worker) neutralCheck(ctx context.Context, poster *gh.Poster, target gh.PR, title, summary string) error {
	id, err := poster.StartCheck(ctx, target, "not-reviewed-"+target.HeadSHA)
	if err != nil {
		return err
	}
	return poster.CompleteCheck(ctx, target, id, gh.CheckResult{Conclusion: "neutral", Title: title, Summary: summary})
}

func ineligibleText(err error) string {
	return "Exemplar does not review this pull request: " + strings.TrimPrefix(err.Error(), engine.ErrIneligible.Error()+": ") + "."
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// reanchor handles open findings from heads this one does not descend
// from (a rebase or force-push, FR-C4): each is looked up by its anchor
// code at the new head and moved there, or outdated with "No longer
// present after rebase". It returns the findings still open. A head whose
// ancestry GitHub cannot tell is left alone; the normal closure rules
// still apply to its findings after the review.
func (w *Worker) reanchor(ctx context.Context, reader *github.Client, poster *gh.Poster, target gh.PR, p *engine.Prepared, open []pipeline.OpenFinding, out *Outcome) []pipeline.OpenFinding {
	rewritten := map[string]bool{}
	checked := map[string]bool{}
	for _, f := range open {
		if f.HeadSha == p.PR.HeadSHA || checked[f.HeadSha] || f.AnchorCode == nil {
			continue
		}
		checked[f.HeadSha] = true
		anc, err := gh.IsAncestor(ctx, reader, p.PR.Repo, f.HeadSha, p.PR.HeadSHA)
		if err != nil {
			w.cfg.Log.Warn("ancestry unknown; findings not re-anchored", "repo", p.PR.Repo, "pr", p.PR.Number, "head", f.HeadSha, "err", err)
			continue
		}
		rewritten[f.HeadSha] = !anc
	}
	var stale []pipeline.OpenFinding
	for _, f := range open {
		if rewritten[f.HeadSha] {
			stale = append(stale, f)
		}
	}
	if len(stale) == 0 {
		return open
	}
	plan := pipeline.Reanchor(stale, func(side, path string) ([]string, bool) {
		rev := p.Workspace.HeadSHA
		if side == "LEFT" {
			rev = p.Workspace.BaseSHA
		}
		data, err := p.Workspace.Show(ctx, rev, path)
		if err != nil {
			return nil, false
		}
		return strings.Split(string(data), "\n"), true
	})
	for _, m := range plan.Moves {
		if err := w.cfg.Store.MoveFinding(ctx, queries.MoveFindingParams{ID: m.Finding.ID, StartLine: int32(m.Start), EndLine: int32(m.End)}); err != nil {
			w.cfg.Log.Warn("finding not moved", "finding", m.Finding.ID, "err", err)
		}
	}
	out.Moved = len(plan.Moves)
	n, err := pipeline.CloseFindings(ctx, w.cfg.Store, poster, target, plan.Outdated)
	out.Closed += n
	if err != nil {
		w.cfg.Log.Warn("findings a rebase removed not all closed", "repo", p.PR.Repo, "pr", p.PR.Number, "err", err)
	}
	gone := map[int64]bool{}
	for _, c := range plan.Outdated {
		gone[c.Finding.ID] = true
	}
	moved := map[int64]pipeline.Move{}
	for _, m := range plan.Moves {
		moved[m.Finding.ID] = m
	}
	var still []pipeline.OpenFinding
	for _, f := range open {
		if gone[f.ID] {
			continue
		}
		if m, ok := moved[f.ID]; ok {
			f.StartLine, f.EndLine = int32(m.Start), int32(m.End)
		}
		still = append(still, f)
	}
	return still
}
