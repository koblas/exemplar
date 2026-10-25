package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestRepoLimit(t *testing.T) {
	t.Parallel()
	snap := exampleSnapshot(t)
	snap.Global.Queues.PerRepoConcurrency = 4
	snap.Global.Queues.LargeRepos = []string{"acme/monorepo"}
	for repo, want := range map[string]int{"acme/app": 4, "acme/monorepo": 32, "ACME/Monorepo": 32} {
		if got := RepoLimit(snap, repo); got != want {
			t.Errorf("%s: %d, want %d", repo, got, want)
		}
	}
	snap.Global.Queues.PerRepoConcurrency = 40
	if got := RepoLimit(snap, "acme/monorepo"); got != 40 {
		t.Errorf("a larger configured limit = %d", got)
	}
}

// otherReplica is a second pool on the same database: another worker pod.
func otherReplica(t *testing.T, st *store.Store) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSlotsAcrossReplicas(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := t.Context()
	a, b := NewSlots(st.Pool), NewSlots(otherReplica(t, st))
	take := func(s *Slots, repo string) func() {
		t.Helper()
		release, ok, err := s.Acquire(ctx, repo, 2)
		if err != nil || !ok {
			t.Fatalf("acquire %s = %v, %v", repo, ok, err)
		}
		return release
	}
	r1 := take(a, "acme/app")
	r2 := take(b, "ACME/App") // case does not matter
	// Both of acme/app's slots are held, one per replica.
	if _, ok, err := a.Acquire(ctx, "acme/app", 2); ok || err != nil {
		t.Fatalf("third slot = %v, %v", ok, err)
	}
	if _, ok, _ := b.Acquire(ctx, "acme/app", 2); ok {
		t.Fatal("third slot on the other replica")
	}
	// Another repo is not affected.
	take(a, "acme/web")()
	// Releasing one frees it.
	r1()
	r3 := take(b, "acme/app")
	r2()
	r3()
	// Everything is free again, and no connection is left holding a lock.
	// pg_locks spans the server, where parallel tests hold locks in their
	// own databases, so count only this test's.
	var locks int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&locks); err != nil || locks != 0 {
		t.Errorf("advisory locks left = %d, %v", locks, err)
	}
}

// A PR head's lock is held by one review at a time, across replicas;
// another head of the PR, or another PR, is not affected.
func TestHeadLock(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := t.Context()
	a, b := NewSlots(st.Pool), NewSlots(otherReplica(t, st))
	release, ok, err := a.AcquireHead(ctx, "acme/app", 7, "abc")
	if err != nil || !ok {
		t.Fatalf("acquire = %v, %v", ok, err)
	}
	if _, ok, err := b.AcquireHead(ctx, "ACME/App", 7, "abc"); ok || err != nil {
		t.Fatalf("second review of the head = %v, %v", ok, err)
	}
	for _, other := range []struct {
		pr   int
		head string
	}{{7, "def"}, {8, "abc"}} {
		r, ok, err := b.AcquireHead(ctx, "acme/app", other.pr, other.head)
		if err != nil || !ok {
			t.Fatalf("PR %d head %s = %v, %v", other.pr, other.head, ok, err)
		}
		r()
	}
	release()
	r, ok, err := b.AcquireHead(ctx, "acme/app", 7, "abc")
	if err != nil || !ok {
		t.Fatalf("after release = %v, %v", ok, err)
	}
	r()
}

// A second review of a head that is being reviewed waits (snoozes)
// without launching a runner, whatever its attempt.
func TestDispatchWaitsForHead(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	snap := exampleSnapshot(t)
	release, ok, err := NewSlots(otherReplica(t, st)).AcquireHead(t.Context(), repo, 1, "abc")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer release()
	launched := 0
	d, err := NewReviewDispatcher(DispatchConfig{
		Launcher: launchFunc(func(context.Context, Task) (Exit, error) { launched++; return ExitDone, nil }),
		Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil }, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	work := func(head string) error {
		return d.Work(t.Context(), &river.Job[store.PRReviewArgs]{JobRow: &rivertype.JobRow{ID: 1}, Args: store.PRReviewArgs{Repo: repo, PR: 1, HeadSHA: head, Attempt: 2}})
	}
	var snooze *rivertype.JobSnoozeError
	if err := work("abc"); !errors.As(err, &snooze) || snooze.Duration != SlotWait || launched != 0 {
		t.Fatalf("work = %v, launched %d", err, launched)
	}
	if err := work("def"); err != nil || launched != 1 {
		t.Fatalf("another head = %v, launched %d", err, launched)
	}
}

// A repo at its limit: the dispatcher snoozes the review without
// launching a runner.
func TestDispatchWaitsForSlot(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	slots := NewSlots(otherReplica(t, st))
	snap := exampleSnapshot(t)
	for range RepoLimit(snap, repo) {
		release, ok, err := slots.Acquire(t.Context(), repo, RepoLimit(snap, repo))
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		defer release()
	}
	launched := 0
	d, err := NewReviewDispatcher(DispatchConfig{
		Launcher: launchFunc(func(context.Context, Task) (Exit, error) { launched++; return ExitDone, nil }),
		Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil }, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = d.Work(t.Context(), &river.Job[store.PRReviewArgs]{JobRow: &rivertype.JobRow{ID: 1}, Args: store.PRReviewArgs{Repo: repo, PR: 1}})
	var snooze *rivertype.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != SlotWait {
		t.Fatalf("work = %v", err)
	}
	if launched != 0 {
		t.Error("a waiting review launched a runner")
	}
}
