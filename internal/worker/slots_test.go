package worker

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

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

// A repo at its limit: the review job snoozes without touching GitHub.
func TestWorkWaitsForSlot(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	slots := NewSlots(otherReplica(t, st))
	snap := exampleSnapshot(t)
	for range RepoLimit(snap, repo) {
		release, ok, err := slots.Acquire(t.Context(), repo, RepoLimit(snap, repo))
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		defer release()
	}
	err := e.w.Work(t.Context(), &river.Job[store.PRReviewArgs]{Args: args(e)})
	var snooze *rivertype.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != SlotWait {
		t.Fatalf("work = %v", err)
	}
	if len(e.fake.reviews)+len(e.fake.checks) != 0 {
		t.Error("a waiting review touched GitHub")
	}
}
