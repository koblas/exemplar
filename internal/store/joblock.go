package store

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobLockKey is the advisory-lock key of a scheduled job: FNV-64a of
// "exemplar/job/<name>". The Python jobs derive the same key
// (exemplar_learning.db.job_lock_key).
func JobLockKey(name string) int64 {
	h := fnv.New64a()
	// A hash.Hash never fails to write; the key is the hash's bits, so the
	// conversion's wrap is intended.
	_, _ = h.Write([]byte("exemplar/job/" + name))
	return int64(h.Sum64())
}

// TryJobLock takes the session-level advisory lock of the scheduled job
// name on a connection of its own (spec §2.4.2 C4), so a manual run cannot
// overlap a scheduled one on any scheduler. It never waits: ok is false,
// with no error, when another run holds the lock. release unlocks and
// closes the connection; it must be called once when ok, and works after
// ctx ends. A process that dies loses the lock with its session.
func TryJobLock(ctx context.Context, dbURL, name string) (func(), bool, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, false, fmt.Errorf("job lock: connect: %w", err)
	}
	closeConn := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}
	key := JobLockKey(name)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		closeConn()
		return nil, false, fmt.Errorf("job lock %s: %w", name, err)
	}
	if !ok {
		closeConn()
		return nil, false, nil
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Closing the session releases the lock even if the unlock fails.
		_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", key)
		_ = conn.Close(uctx)
	}, true, nil
}
