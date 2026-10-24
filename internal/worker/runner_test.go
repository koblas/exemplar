package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

type launchFunc func(context.Context, Task) (Exit, error)

func (f launchFunc) Launch(ctx context.Context, t Task) (Exit, error) { return f(ctx, t) }

// fakeJobs is a River job table of one process.
type fakeJobs struct {
	mu   sync.Mutex
	rows map[int64]*rivertype.JobRow
}

func (f *fakeJobs) JobGet(_ context.Context, id int64) (*rivertype.JobRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return nil, river.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeJobs) set(id int64, kind string, state rivertype.JobState, args, meta string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[int64]*rivertype.JobRow{}
	}
	f.rows[id] = &rivertype.JobRow{ID: id, Kind: kind, State: state, EncodedArgs: []byte(args), Metadata: []byte(meta), MaxAttempts: 3, Attempt: 1}
}

func TestDispatchMapsExits(t *testing.T) {
	t.Parallel()
	snap := exampleSnapshot(t)
	errBoom := errors.New("api server down")
	for name, tc := range map[string]struct {
		exit Exit
		err  error
		want func(error) bool
	}{
		"done": {ExitDone, nil, func(err error) bool { return err == nil }},
		"cancel": {ExitCancel, nil, func(err error) bool {
			var c *rivertype.JobCancelError
			return errors.As(err, &c) && errors.Is(err, errBadJob)
		}},
		"superseded": {ExitSuperseded, nil, func(err error) bool { return errors.Is(err, rivertype.ErrJobCancelledRemotely) }},
		"error":      {ExitError, nil, func(err error) bool { return errors.Is(err, errRunnerFailed) }},
		"killed":     {Exit(137), nil, func(err error) bool { return errors.Is(err, errRunnerFailed) }},
		"capacity": {ExitError, fmt.Errorf("busy: %w", ErrNoCapacity), func(err error) bool {
			var s *rivertype.JobSnoozeError
			return errors.As(err, &s) && s.Duration == SlotWait
		}},
		"launch failed": {ExitError, errBoom, func(err error) bool { return errors.Is(err, errBoom) }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got Task
			d, err := NewEvaluateDispatcher(DispatchConfig{
				Launcher: launchFunc(func(_ context.Context, task Task) (Exit, error) { got = task; return tc.exit, tc.err }),
				Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			err = d.Work(t.Context(), &river.Job[store.EvaluateArgs]{JobRow: &rivertype.JobRow{ID: 7, Kind: "evaluate", Attempt: 2}})
			if !tc.want(err) {
				t.Errorf("work = %v", err)
			}
			if got != (Task{JobID: 7, Kind: "evaluate", Attempt: 2, ConfigVersion: snap.Version, Timeout: EvaluateTimeout}) {
				t.Errorf("task = %+v", got)
			}
		})
	}
}

func TestExitOf(t *testing.T) {
	t.Parallel()
	superseded, cancel := context.WithCancelCause(t.Context())
	cancel(rivertype.ErrJobCancelledRemotely)
	for name, tc := range map[string]struct {
		ctx  context.Context //nolint:containedctx // table input
		err  error
		want Exit
	}{
		"done":            {t.Context(), nil, ExitDone},
		"bad job":         {t.Context(), fmt.Errorf("x: %w", errBadJob), ExitCancel},
		"job cancel":      {t.Context(), river.JobCancel(errors.New("gone")), ExitCancel},
		"remote cancel":   {superseded, fmt.Errorf("review: %w", rivertype.ErrJobCancelledRemotely), ExitSuperseded},
		"cancelled ctx":   {superseded, context.Canceled, ExitSuperseded},
		"timed out":       {t.Context(), context.DeadlineExceeded, ExitError},
		"retryable error": {t.Context(), errors.New("github 502"), ExitError},
	} {
		if got := exitOf(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}

func TestRunnerRefuses(t *testing.T) {
	t.Parallel()
	e := setup(t, repo)
	snap := exampleSnapshot(t)
	jobs := &fakeJobs{}
	jobs.set(2, "pr_review", rivertype.JobStateCompleted, `{}`, `{}`)
	jobs.set(3, "owner_action", rivertype.JobStateRunning, `{"id": 1}`, `{}`)
	jobs.set(4, "pr_review", rivertype.JobStateRunning, `{"repo": "nope"}`, `{}`)
	jobs.set(5, "pr_review", rivertype.JobStateRunning, `not json`, `{}`)
	r := &Runner{Config: e.w.cfg, Jobs: jobs, Snapshot: func(_ context.Context, v string) (*config.Snapshot, error) {
		if v != snap.Version {
			return nil, fmt.Errorf("config %s %w", v, errNotStored)
		}
		return snap, nil
	}}
	for name, tc := range map[string]struct {
		id      int64
		version string
		want    Exit
	}{
		"vanished job":      {1, snap.Version, ExitCancel},
		"settled job":       {2, snap.Version, ExitCancel},
		"not a runner kind": {3, snap.Version, ExitCancel},
		"bad args":          {4, snap.Version, ExitCancel},
		"undecodable args":  {5, snap.Version, ExitCancel},
		"unknown config":    {4, "nope", ExitError},
	} {
		if got := r.Run(t.Context(), tc.id, tc.version); got != tc.want {
			t.Errorf("%s: exit %d, want %d", name, got, tc.want)
		}
	}
	if len(e.fake.reviews)+len(e.fake.checks) != 0 {
		t.Error("a refused job touched GitHub")
	}
}

func TestRunnerWatchesCancel(t *testing.T) {
	t.Parallel()
	jobs := &fakeJobs{}
	jobs.set(1, "pr_review", rivertype.JobStateRunning, `{}`, `{}`)
	r := &Runner{Jobs: jobs, CancelPoll: time.Millisecond}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	go r.watchCancel(ctx, cancel, 1)
	if CancelRequested(t.Context(), jobs, 1) {
		t.Fatal("cancel before River asked")
	}
	jobs.set(1, "pr_review", rivertype.JobStateRunning, `{}`, `{"cancel_attempted_at": "2026-10-24T12:00:00Z"}`)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("runner never saw the cancel")
	}
	if !errors.Is(context.Cause(ctx), rivertype.ErrJobCancelledRemotely) {
		t.Errorf("cause = %v", context.Cause(ctx))
	}
	if CancelRequested(t.Context(), jobs, 99) {
		t.Error("a missing job reads as cancelled")
	}
}

func TestStoredSnapshot(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	tree, err := config.ReadTree(os.DirFS("../../examples/exemplar-config"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := config.LoadSnapshot(tree, config.TreeHash(tree))
	if err != nil {
		t.Fatal(err)
	}
	if err := PutSnapshot(t.Context(), st, snap); err != nil {
		t.Fatal(err)
	}
	// Storing the version again only marks it current.
	if err := PutSnapshot(t.Context(), st, snap); err != nil {
		t.Fatal(err)
	}
	got, err := StoredSnapshot(t.Context(), st, snap.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != snap.Version || len(got.Rules) != len(snap.Rules) || len(got.Repos) != len(snap.Repos) || len(got.Evals) != len(snap.Evals) {
		t.Errorf("stored snapshot = %s %d rules %d repos, want %s %d %d", got.Version, len(got.Rules), len(got.Repos), snap.Version, len(snap.Rules), len(snap.Repos))
	}
	if _, err := StoredSnapshot(t.Context(), st, "nope"); !errors.Is(err, errNotStored) {
		t.Errorf("missing version = %v", err)
	}
	// An in-process runner uses the live snapshot for its own version.
	load := Snapshots(st, func(context.Context) (*config.Snapshot, error) { return snap, nil })
	if live, err := load(t.Context(), snap.Version); err != nil || live != snap {
		t.Errorf("current version = %p, %v", live, err)
	}
}

// TestRiverDispatchInProcess runs one review through River, the dispatcher
// and the in-process launcher into the store.
func TestRiverDispatchInProcess(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	runner := &Runner{Config: e.w.cfg, Snapshot: Snapshots(st, e.w.cfg.Snapshot)}
	d, err := NewReviewDispatcher(DispatchConfig{Launcher: InProcess{Runner: runner}, Snapshot: e.w.cfg.Snapshot, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, d)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{store.ReviewQueue: {MaxWorkers: 1}}, Workers: workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.Jobs = client
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(context.WithoutCancel(t.Context())) }()
	if _, err := client.Insert(t.Context(), args(e), nil); err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Kind != river.EventKindJobCompleted {
		t.Fatalf("job = %s", ev.Kind)
	}
	var version string
	if err := st.Pool.QueryRow(t.Context(), `SELECT config_version FROM reviews WHERE repo = $1 AND pr_number = $2`, repo, prNum).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "cfg-test" || len(e.fake.reviews) != 1 {
		t.Errorf("review config %s, %d posted", version, len(e.fake.reviews))
	}
}
