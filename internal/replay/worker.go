package replay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
)

// worker runs replay jobs from the River queue.
type worker struct {
	river.WorkerDefaults[store.ReviewArgs]

	runner   *Runner
	fixtures map[string]schema.Fixture

	mu       sync.Mutex
	outcomes map[string]*Outcome
	errs     map[string]error
}

// errUnknownFixture reports a job for a fixture this run does not have.
var errUnknownFixture = errors.New("unknown fixture")

func (w *worker) Work(ctx context.Context, job *river.Job[store.ReviewArgs]) error {
	fx, ok := w.fixtures[job.Args.FixtureID]
	if !ok {
		return river.JobCancel(fmt.Errorf("%w %s", errUnknownFixture, job.Args.FixtureID))
	}
	out, err := w.runner.Run(ctx, fx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.errs[fx.ID] = err
		return err // River retries; writes are idempotent on natural keys
	}
	delete(w.errs, fx.ID)
	w.outcomes[fx.ID] = out
	return nil
}

// RunAll replays every fixture. With a store, each fixture is a River job
// (so retries exercise idempotency); without one, fixtures run in order.
func (r *Runner) RunAll(ctx context.Context, fixtures []schema.Fixture) ([]*Outcome, map[string]error, error) {
	if r.Store == nil {
		var outs []*Outcome
		errs := map[string]error{}
		for _, fx := range fixtures {
			out, err := r.Run(ctx, fx)
			if err != nil {
				errs[fx.ID] = err
				continue
			}
			outs = append(outs, out)
		}
		if err := ctx.Err(); err != nil {
			return outs, errs, fmt.Errorf("replay: %w", err)
		}
		return outs, errs, nil
	}

	w := &worker{runner: r, fixtures: map[string]schema.Fixture{}, outcomes: map[string]*Outcome{}, errs: map[string]error{}}
	for _, fx := range fixtures {
		w.fixtures[fx.ID] = fx
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	client, err := river.NewClient(riverpgxv5.New(r.Store.Pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 2}},
		Workers: workers,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create river client: %w", err)
	}
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("start river client: %w", err)
	}
	defer func() { _ = client.Stop(context.WithoutCancel(ctx)) }()

	var b [8]byte
	_, _ = rand.Read(b[:])
	runID := hex.EncodeToString(b[:])
	for _, fx := range fixtures {
		if _, err := client.Insert(ctx, store.ReviewArgs{FixtureID: fx.ID, RunID: runID}, nil); err != nil {
			return nil, nil, fmt.Errorf("enqueue fixture %s: %w", fx.ID, err)
		}
	}
	done := 0
	for done < len(fixtures) {
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("replay: %w", ctx.Err())
		case ev := <-events:
			if ev == nil {
				continue
			}
			// A failed attempt that will retry is not done yet.
			if ev.Kind == river.EventKindJobFailed && ev.Job.Attempt < ev.Job.MaxAttempts {
				continue
			}
			done++
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var outs []*Outcome
	for _, fx := range fixtures {
		if o, ok := w.outcomes[fx.ID]; ok {
			outs = append(outs, o)
		}
	}
	return outs, w.errs, nil
}
