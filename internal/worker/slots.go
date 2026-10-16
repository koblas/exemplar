package worker

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/koblas/exemplar/internal/config"
)

// Per-repo concurrency (spec §6.15 backpressure, FR-D6): at most
// queues.per_repo_concurrency reviews of one repo run at once across every
// worker replica, 32 for queues.large_repos. River's queue MaxWorkers is
// per process, so the limit is a set of Postgres advisory-lock slots: a
// review holds one for its whole run on a dedicated connection. A pod that
// dies drops its connection, and Postgres frees the slot.

// LargeRepoConcurrency is the per-repo limit for queues.large_repos.
const LargeRepoConcurrency = 32

// SlotWait is how long a review that found no free slot waits before it
// is tried again (it is snoozed, which costs no attempt).
const SlotWait = 20 * time.Second

// RepoLimit is the number of reviews of repo that may run at once.
func RepoLimit(snap *config.Snapshot, repo string) int {
	q := snap.Global.Queues
	if slices.ContainsFunc(q.LargeRepos, func(r string) bool { return strings.EqualFold(r, repo) }) {
		return max(LargeRepoConcurrency, q.PerRepoConcurrency)
	}
	return max(1, q.PerRepoConcurrency)
}

// Slots hands out per-repo review slots.
type Slots struct{ pool *pgxpool.Pool }

// NewSlots returns Slots on pool.
func NewSlots(pool *pgxpool.Pool) *Slots { return &Slots{pool: pool} }

// slotKey is the advisory-lock key of one slot of one repo.
func slotKey(repo string, slot int) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "exemplar/review-slot/%s/%d", strings.ToLower(repo), slot)
	return int64(h.Sum64())
}

// Acquire takes a free slot of repo's limit. It returns ok=false, with no
// error, when every slot is taken. release frees the slot and returns the
// connection; it must be called exactly once, and works after ctx ends.
func (s *Slots) Acquire(ctx context.Context, repo string, limit int) (func(), bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("worker: slot connection: %w", err)
	}
	for slot := range limit {
		key := slotKey(repo, slot)
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
			conn.Release()
			return nil, false, fmt.Errorf("worker: take slot: %w", err)
		}
		if got {
			return func() {
				uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
					// The lock may still be held: close the connection so
					// Postgres drops it, rather than pooling it locked.
					_ = conn.Conn().Close(uctx)
				}
				conn.Release()
			}, true, nil
		}
	}
	conn.Release()
	return nil, false, nil
}
