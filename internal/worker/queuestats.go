package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/koblas/exemplar/internal/telemetry"
)

// waitingSQL is a River job waiting to run: not running or finished, and
// due within a minute. Snoozed jobs (state scheduled) are included, so a
// review held by a full runner quota or repo slot counts as waiting; a
// job scheduled for later does not. Shared with the stalled-queue alert
// (dashboards/provisioning/alerts.yaml).
const waitingSQL = `state IN ('available', 'scheduled', 'retryable', 'pending') AND scheduled_at <= now() + interval '1 minute'`

// QueueStats counts River jobs per queue and state, with each state's
// oldest waiting job (by enqueue time), for telemetry.ObserveQueues.
// Finished jobs are left out.
func QueueStats(ctx context.Context, pool *pgxpool.Pool) ([]telemetry.QueueStat, error) {
	rows, err := pool.Query(ctx, `SELECT queue, state::text, count(*),
			coalesce(extract(epoch FROM now() - min(created_at) FILTER (WHERE `+waitingSQL+`)), 0)::float8
		FROM river_job WHERE state IN ('available', 'scheduled', 'retryable', 'running', 'pending')
		GROUP BY queue, state ORDER BY queue, state`)
	if err != nil {
		return nil, fmt.Errorf("worker: queue stats: %w", err)
	}
	defer rows.Close()
	var out []telemetry.QueueStat
	for rows.Next() {
		var s telemetry.QueueStat
		var secs float64
		if err := rows.Scan(&s.Queue, &s.State, &s.Jobs, &secs); err != nil {
			return nil, fmt.Errorf("worker: queue stats: %w", err)
		}
		s.OldestWaiting = time.Duration(secs * float64(time.Second))
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("worker: queue stats: %w", err)
	}
	return out, nil
}
