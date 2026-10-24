package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
)

// Dispatcher is the River worker for a job kind a runner does: it takes
// the per-repo slot (pr_review only), chooses the config version, hands
// the job to a Launcher and maps the runner's exit back to River. River
// keeps timeouts, retries, uniqueness and supersede.
type Dispatcher[T river.JobArgs] struct {
	river.WorkerDefaults[T]

	launch   Launcher
	snapshot func(context.Context) (*config.Snapshot, error)
	timeout  time.Duration
	// slot, when set, names the repo whose per-repo slot the job holds.
	slot  func(T) string
	slots *Slots
	log   *slog.Logger
}

// DispatchConfig wires the dispatchers.
type DispatchConfig struct {
	Launcher Launcher
	// Snapshot returns the current config; its version goes to the runner.
	Snapshot func(context.Context) (*config.Snapshot, error)
	// Store holds the per-repo slots; nil only in tests, which then take
	// no slot.
	Store *store.Store
	Log   *slog.Logger
}

func newDispatcher[T river.JobArgs](cfg DispatchConfig, timeout time.Duration) (*Dispatcher[T], error) {
	if cfg.Launcher == nil || cfg.Snapshot == nil {
		return nil, fmt.Errorf("%w: a dispatcher needs a launcher and the config", errMissingDependency)
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Dispatcher[T]{launch: cfg.Launcher, snapshot: cfg.Snapshot, timeout: timeout, log: log}, nil
}

// Job timeouts, as the workers set them.
const (
	ReviewTimeout   = 30 * time.Minute
	EvaluateTimeout = time.Hour
)

// NewReviewDispatcher dispatches pr_review jobs, one per-repo slot each.
func NewReviewDispatcher(cfg DispatchConfig) (*Dispatcher[store.PRReviewArgs], error) {
	d, err := newDispatcher[store.PRReviewArgs](cfg, ReviewTimeout)
	if err != nil {
		return nil, err
	}
	if cfg.Store != nil {
		d.slots = NewSlots(cfg.Store.Pool)
		d.slot = func(a store.PRReviewArgs) string { return a.Repo }
	}
	return d, nil
}

// NewEvaluateDispatcher dispatches `exemplar review` evaluations.
func NewEvaluateDispatcher(cfg DispatchConfig) (*Dispatcher[store.EvaluateArgs], error) {
	return newDispatcher[store.EvaluateArgs](cfg, EvaluateTimeout)
}

// NewPlaygroundDispatcher dispatches web UI playground runs, with River's
// default timeout as before the split, stated so a runner Job's deadline
// matches it.
func NewPlaygroundDispatcher(cfg DispatchConfig) (*Dispatcher[store.PlaygroundArgs], error) {
	return newDispatcher[store.PlaygroundArgs](cfg, river.JobTimeoutDefault)
}

// Timeout implements river.Worker.
func (d *Dispatcher[T]) Timeout(*river.Job[T]) time.Duration { return d.timeout }

// Work implements river.Worker.
func (d *Dispatcher[T]) Work(ctx context.Context, job *river.Job[T]) error {
	snap, err := d.snapshot(ctx)
	if err != nil {
		return err
	}
	if d.slot != nil {
		// Per-repo fairness: wait for one of the repo's slots.
		repo := d.slot(job.Args)
		release, ok, err := d.slots.Acquire(ctx, repo, RepoLimit(snap, repo))
		if err != nil {
			return err
		}
		if !ok {
			d.log.Debug("repo at its review limit; waiting", "repo", repo, "job", job.ID)
			return river.JobSnooze(SlotWait)
		}
		defer release()
	}
	exit, err := d.launch.Launch(ctx, Task{
		JobID: job.ID, Kind: job.Kind, Attempt: job.Attempt, ConfigVersion: snap.Version, Timeout: d.timeout,
	})
	if errors.Is(err, ErrNoCapacity) {
		d.log.Info("no runner capacity; waiting", "job", job.ID, "kind", job.Kind, "err", err)
		return river.JobSnooze(SlotWait)
	}
	if err != nil {
		return fmt.Errorf("worker: launch %s job %d: %w", job.Kind, job.ID, err)
	}
	switch exit {
	case ExitDone:
		return nil
	case ExitCancel:
		return river.JobCancel(fmt.Errorf("%w: runner cancelled %s job %d", errBadJob, job.Kind, job.ID))
	case ExitSuperseded:
		return rivertype.ErrJobCancelledRemotely
	case ExitError:
	}
	return fmt.Errorf("%w: %s job %d exited %d", errRunnerFailed, job.Kind, job.ID, exit)
}

var errRunnerFailed = errors.New("worker: runner failed")
