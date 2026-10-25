package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// The key is shared with the Python jobs (learning/tests/test_db.py).
func TestJobLockKey(t *testing.T) {
	t.Parallel()
	if got := store.JobLockKey("cluster"); got != jobLockKeyCluster {
		t.Errorf("JobLockKey(cluster) = %d, want %d", got, jobLockKeyCluster)
	}
	if store.JobLockKey("rank") == store.JobLockKey("cluster") {
		t.Error("two jobs share a key")
	}
}

const jobLockKeyCluster int64 = -6205359000760017648

// A job lock is exclusive per name, never waits, and is free again once
// released or once its session ends.
func TestTryJobLock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	url := storetest.EmptyURL(t)
	take := func(name string) (func(), bool) {
		t.Helper()
		release, ok, err := store.TryJobLock(ctx, url, name)
		if err != nil {
			t.Fatalf("lock %s: %v", name, err)
		}
		return release, ok
	}

	release, ok := take("demote")
	if !ok {
		t.Fatal("first lock not taken")
	}
	if _, again := take("demote"); again {
		t.Fatal("a second run took a held lock")
	}
	other, ok := take("digest")
	if !ok {
		t.Fatal("another job's lock was blocked")
	}
	other()
	release()
	release, ok = take("demote")
	if !ok {
		t.Fatal("lock not free after release")
	}
	release()

	// A holder whose session ends (a killed process) frees the lock.
	holder, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", store.JobLockKey("teach")); err != nil {
		t.Fatal(err)
	}
	if _, ok := take("teach"); ok {
		t.Fatal("took a lock another session holds")
	}
	if err := holder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	release, ok = take("teach")
	if !ok {
		t.Fatal("lock not free after its session ended")
	}
	release()
}
