package worker

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/telemetry"
)

// The runner quota spans replicas, and its slots are apart from repo
// slots. Seeded random takes and releases from two replicas never hold
// more than the quota, and every take succeeds while one is free.
func TestRunnerQuotaSlots(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := t.Context()
	replicas := []*Slots{NewSlots(st.Pool), NewSlots(otherReplica(t, st))}
	const quota = 3
	r := rand.New(rand.NewPCG(5, 8))
	var held []func()
	for step := range 200 {
		if len(held) > 0 && r.IntN(2) == 0 {
			i := r.IntN(len(held))
			held[i]()
			held = append(held[:i], held[i+1:]...)
			continue
		}
		release, ok, err := replicas[r.IntN(2)].AcquireQuota(ctx, quota)
		if err != nil {
			t.Fatal(err)
		}
		if ok != (len(held) < quota) {
			t.Fatalf("step %d: took %v with %d of %d held", step, ok, len(held), quota)
		}
		if ok {
			held = append(held, release)
		}
	}
	for _, release := range held {
		release()
	}
	// A repo's slots are not the quota's.
	for range quota {
		release, ok, err := replicas[0].AcquireQuota(ctx, quota)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		defer release()
	}
	release, ok, err := replicas[1].Acquire(ctx, "exemplar/runner-quota", quota)
	if err != nil || !ok {
		t.Fatalf("a repo slot with the quota full = %v, %v", ok, err)
	}
	release()
}

// With the quota full, a dispatcher of any kind snoozes without launching
// a runner, and launches once a slot is free.
func TestDispatchWaitsForQuota(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	snap := exampleSnapshot(t)
	other := NewSlots(otherReplica(t, st))
	hold, ok, err := other.AcquireQuota(t.Context(), 1)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	launched := 0
	d, err := NewEvaluateDispatcher(DispatchConfig{
		Launcher: launchFunc(func(context.Context, Task) (Exit, error) { launched++; return ExitDone, nil }),
		Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil }, Store: st, RunnerQuota: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	job := &river.Job[store.EvaluateArgs]{JobRow: &rivertype.JobRow{ID: 1, Kind: "evaluate"}}
	err = d.Work(t.Context(), job)
	var snooze *rivertype.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != SlotWait || launched != 0 {
		t.Fatalf("work at the quota = %v, launched %d", err, launched)
	}
	hold()
	if err := d.Work(t.Context(), job); err != nil || launched != 1 {
		t.Fatalf("work under the quota = %v, launched %d", err, launched)
	}
	// The launch released its slot.
	again, ok, err := other.AcquireQuota(t.Context(), 1)
	if err != nil || !ok {
		t.Fatalf("slot after the launch = %v, %v", ok, err)
	}
	again()

	if _, err := NewEvaluateDispatcher(DispatchConfig{
		Launcher: launchFunc(func(context.Context, Task) (Exit, error) { return ExitDone, nil }),
		Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil }, RunnerQuota: 1,
	}); !errors.Is(err, errMissingDependency) {
		t.Errorf("a quota without the store = %v", err)
	}
}

// QueueStats counts unfinished River jobs per queue and state; a snoozed
// job that is due is waiting, one scheduled for later is not.
func TestQueueStats(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	if _, err := st.Pool.Exec(t.Context(), `INSERT INTO river_job (kind, args, queue, state, max_attempts, created_at, scheduled_at, finalized_at) VALUES
		('pr_review', '{}', 'reviews', 'available', 25, now() - interval '10 minutes', now() - interval '10 minutes', NULL),
		('pr_review', '{}', 'reviews', 'available', 25, now() - interval '2 minutes', now() - interval '2 minutes', NULL),
		('pr_review', '{}', 'reviews', 'scheduled', 25, now() - interval '30 minutes', now() + interval '10 seconds', NULL),
		('pr_review', '{}', 'reviews', 'scheduled', 25, now() - interval '50 minutes', now() + interval '1 hour', NULL),
		('pr_review', '{}', 'reviews', 'running', 25, now(), now() - interval '1 second', NULL),
		('pr_review', '{}', 'reviews', 'completed', 25, now() - interval '1 hour', now() - interval '1 hour', now())`); err != nil {
		t.Fatal(err)
	}
	stats, err := QueueStats(t.Context(), st.Pool)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]telemetry.QueueStat{}
	for _, s := range stats {
		by[s.State] = s
	}
	if len(stats) != 3 || by["available"].Jobs != 2 || by["available"].OldestWaiting < 9*time.Minute ||
		by["scheduled"].Jobs != 2 || by["scheduled"].OldestWaiting < 29*time.Minute || by["scheduled"].OldestWaiting > 31*time.Minute ||
		by["running"].Jobs != 1 || by["running"].OldestWaiting != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

// A runner's checkout volume: twice the repo's largest recent checkout
// plus 1 GiB, in GiB, within the bounds; the template's for a new repo.
func TestDiskSize(t *testing.T) {
	t.Parallel()
	s := DiskSizing{Min: 2 * gib, Max: 50 * gib}
	for _, tc := range []struct{ known, want int64 }{
		{0, 0}, {100 << 20, 2 * gib}, {3 * gib, 7 * gib}, {3*gib + 1, 8 * gib}, {40 * gib, 50 * gib},
	} {
		if got := s.size(tc.known); got != tc.want {
			t.Errorf("size(%d) = %d, want %d", tc.known, got, tc.want)
		}
	}
}

// A review records its checkout's size, the repo's recent largest sizes
// its next runner, and the dispatcher passes it on.
func TestCheckoutBytesSizeTheRunner(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Status != StatusReviewed {
		t.Fatalf("review = %+v, %v", out, err)
	}
	known, err := st.RepoCheckoutBytes(t.Context(), repo)
	if err != nil || known <= 0 {
		t.Fatalf("recorded checkout = %d, %v", known, err)
	}
	if n, err := st.RepoCheckoutBytes(t.Context(), "acme/unseen"); err != nil || n != 0 {
		t.Errorf("an unseen repo = %d, %v", n, err)
	}

	snap := exampleSnapshot(t)
	var got Task
	d, err := NewReviewDispatcher(DispatchConfig{
		Launcher: launchFunc(func(_ context.Context, task Task) (Exit, error) { got = task; return ExitDone, nil }),
		Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil },
		Disk: &DiskSizing{Min: 2 * gib, Max: 50 * gib, Known: func(ctx context.Context, r string) int64 {
			n, _ := st.RepoCheckoutBytes(ctx, r)
			return n
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Work(t.Context(), &river.Job[store.PRReviewArgs]{JobRow: &rivertype.JobRow{ID: 9}, Args: store.PRReviewArgs{Repo: repo, PR: 1}}); err != nil {
		t.Fatal(err)
	}
	if want := (DiskSizing{Min: 2 * gib, Max: 50 * gib}).size(known); got.WorkDisk != want || want != 2*gib {
		t.Errorf("task disk = %d, want %d", got.WorkDisk, want)
	}
}
