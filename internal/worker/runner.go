package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/store"
)

// The control/runner split (spec §2.4 C1). Control is River: it routes
// events, runs the cheap queues and the periodic jobs, and dispatches each
// pr_review, evaluate and playground_run job to a runner through a
// Launcher. A runner does that one job, with the config version it was
// dispatched with, and exits. The in-process launcher runs it in the
// control process (the quickstart, as before the split); the Kubernetes
// launcher runs `exemplar-worker run-job` as a Job per review.

// Exit is how a runner ended, as the dispatcher maps it back to River.
// It is the run-job process's exit code.
type Exit int

// Runner exits. Any other code (or none, when the runner was killed) is an
// error, which River retries.
const (
	ExitDone       Exit = 0  // the job is done
	ExitError      Exit = 1  // failed; River retries it
	ExitCancel     Exit = 10 // a bad or vanished job: cancelled, not retried
	ExitSuperseded Exit = 11 // cancelled remotely (a newer head superseded it)
)

// Task is one dispatched job.
type Task struct {
	JobID   int64
	Kind    string
	Attempt int
	// ConfigVersion is the exemplar-config version the dispatcher chose
	// (config_snapshots.version).
	ConfigVersion string
	// Timeout is the job's River timeout.
	Timeout time.Duration
	// Nonce redeems the job's runner claim, once.
	Nonce string
}

// Launcher runs a Task to its end. It returns an error only when it could
// not tell how the runner ended; ErrNoCapacity asks for a snooze.
type Launcher interface {
	Launch(ctx context.Context, t Task) (Exit, error)
}

// ErrNoCapacity is returned by a launcher that cannot start the task now.
var ErrNoCapacity = errors.New("worker: no runner capacity")

// RunnerJobs reads River job rows (river.Client.JobGet).
type RunnerJobs interface {
	JobGet(ctx context.Context, id int64) (*rivertype.JobRow, error)
}

// Runner runs one dispatched job.
type Runner struct {
	// Config wires the job's worker; its Snapshot is replaced by the
	// dispatched version.
	Config Config
	Jobs   RunnerJobs
	// Snapshot loads a config version.
	Snapshot func(ctx context.Context, version string) (*config.Snapshot, error)
	// CancelPoll is how often the runner reads its job row for a remote
	// cancel; zero leaves cancellation to the caller's context (in-process,
	// where River cancels it).
	CancelPoll time.Duration
	// Claims redeems the job's tokens; the runner's GitHub is then the
	// claim's (ClaimClients). Nil only in tests, which keep Config.GitHub.
	Claims *Claims
	// BaseURL is GitHub's API base for claim clients (GHES).
	BaseURL string
}

// Run runs job id with config version, redeeming its claim with nonce, and
// reports how it ended.
func (r *Runner) Run(ctx context.Context, id int64, version, nonce string) Exit {
	log := r.Config.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	exit, err := r.run(ctx, id, version, nonce)
	switch exit {
	case ExitDone, ExitSuperseded:
	case ExitCancel:
		log.Warn("job cancelled", "job", id, "err", err)
	case ExitError:
		log.Error("job failed", "job", id, "err", err)
	}
	return exit
}

func (r *Runner) run(ctx context.Context, id int64, version, nonce string) (Exit, error) {
	row, err := r.Jobs.JobGet(ctx, id)
	if errors.Is(err, river.ErrNotFound) {
		return ExitCancel, fmt.Errorf("worker: job %d %w", id, errGone)
	}
	if err != nil {
		return ExitError, fmt.Errorf("worker: get job %d: %w", id, err)
	}
	if row.State != rivertype.JobStateRunning {
		// A runner left over from an attempt River has already settled.
		return ExitCancel, fmt.Errorf("%w: job %d is %s", errBadJob, id, row.State)
	}
	snap, err := r.Snapshot(ctx, version)
	if err != nil {
		return ExitError, fmt.Errorf("worker: config %s: %w", version, err)
	}
	cfg := r.Config
	cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	if r.Claims != nil {
		grant, claimed, err := r.Claims.Redeem(ctx, id, nonce)
		if errors.Is(err, errClaimRefused) {
			return ExitCancel, err
		}
		if err != nil {
			return ExitError, err
		}
		defer func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = r.Claims.Release(rctx, id)
		}()
		if claimed != version {
			return ExitCancel, fmt.Errorf("%w: claim is for config %s, not %s", errClaimRefused, claimed, version)
		}
		cfg.GitHub = ClaimClients{Grant: grant, BaseURL: r.BaseURL}
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if r.CancelPoll > 0 {
		go r.watchCancel(ctx, cancel, id)
	}
	err = r.work(ctx, cfg, snap, row)
	return exitOf(ctx, err), err
}

// work runs row with the worker for its kind, as River would.
func (r *Runner) work(ctx context.Context, cfg Config, snap *config.Snapshot, row *rivertype.JobRow) error {
	switch row.Kind {
	case store.PRReviewArgs{}.Kind():
		w, err := New(cfg)
		if err != nil {
			return err
		}
		return runJob(ctx, w, row)
	case store.EvaluateArgs{}.Kind():
		w, err := NewEvaluateWorker(cfg)
		if err != nil {
			return err
		}
		return runJob(ctx, w, row)
	case store.PlaygroundArgs{}.Kind():
		w, err := NewPlaygroundWorker(cfg, func(ruleYAML []byte) (*config.Snapshot, string, error) {
			return playground.RuleFileSnapshot(snap.Files, ruleYAML, snap.Version)
		})
		if err != nil {
			return err
		}
		return runJob(ctx, w, row)
	}
	return fmt.Errorf("%w: kind %q is not run by a runner", errBadJob, row.Kind)
}

// runJob decodes row's args and calls w as River would, with w's timeout.
func runJob[T river.JobArgs](ctx context.Context, w river.Worker[T], row *rivertype.JobRow) error {
	job := &river.Job[T]{JobRow: row}
	if err := json.Unmarshal(row.EncodedArgs, &job.Args); err != nil {
		return fmt.Errorf("%w: args: %w", errBadJob, err)
	}
	if d := w.Timeout(job); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	return w.Work(ctx, job) //nolint:wrapcheck // River's cancel errors must reach exitOf as they are
}

// watchCancel cancels the run when River marks its job cancelled while
// running (JobCancel sets cancel_attempted_at in the job's metadata).
func (r *Runner) watchCancel(ctx context.Context, cancel context.CancelCauseFunc, id int64) {
	t := time.NewTicker(r.CancelPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if CancelRequested(ctx, r.Jobs, id) {
			cancel(rivertype.ErrJobCancelledRemotely)
			return
		}
	}
}

// CancelRequested reports whether River was asked to cancel running job id.
// A read error counts as no.
func CancelRequested(ctx context.Context, jobs RunnerJobs, id int64) bool {
	row, err := jobs.JobGet(ctx, id)
	if err != nil {
		return false
	}
	var meta struct {
		CancelAttemptedAt *time.Time `json:"cancel_attempted_at"`
	}
	return json.Unmarshal(row.Metadata, &meta) == nil && meta.CancelAttemptedAt != nil
}

// exitOf maps a worker's result to an Exit.
func exitOf(ctx context.Context, err error) Exit {
	var cancelled *rivertype.JobCancelError
	switch {
	case err == nil:
		return ExitDone
	case Superseded(ctx):
		return ExitSuperseded
	case errors.As(err, &cancelled), errors.Is(err, errBadJob), errors.Is(err, errGone), errors.Is(err, errClaimRefused):
		return ExitCancel
	}
	return ExitError
}

// JobRunner runs a dispatched job to its end (Runner).
type JobRunner interface {
	Run(ctx context.Context, id int64, version, nonce string) Exit
}

// Superseded reports whether ctx was cancelled by River's remote cancel.
// It compares the cause itself: every river.JobCancel error matches
// rivertype.ErrJobCancelledRemotely under errors.Is.
func Superseded(ctx context.Context) bool {
	return context.Cause(ctx) == rivertype.ErrJobCancelledRemotely //nolint:errorlint,err113 // see above
}

// InProcess is the launcher that runs a task in this process.
type InProcess struct{ Runner JobRunner }

// Launch implements Launcher.
func (l InProcess) Launch(ctx context.Context, t Task) (Exit, error) {
	return l.Runner.Run(ctx, t.JobID, t.ConfigVersion, t.Nonce), nil
}
