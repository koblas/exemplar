package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/store"
)

// StatsEvery is how often the feedback roll-ups are refreshed (spec §12.6).
const StatsEvery = 15 * time.Minute

// StatsArgs is the periodic job that refreshes the materialized views the
// dashboards, the owner digest and auto-demotion read (rule_weekly_stats,
// review_daily_stats).
type StatsArgs struct{}

// Kind implements river.JobArgs.
func (StatsArgs) Kind() string { return "refresh_stats" }

// InsertOpts runs it on the maintenance queue, once a period.
func (StatsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: MaintenanceQueue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: StatsEvery}}
}

// StatsWorker refreshes the roll-ups.
type StatsWorker struct {
	river.WorkerDefaults[StatsArgs]
	st  *store.Store
	log *slog.Logger
}

// NewStatsWorker returns a StatsWorker.
func NewStatsWorker(st *store.Store, log *slog.Logger) (*StatsWorker, error) {
	if st == nil {
		return nil, errors.New("worker: stats need a store")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &StatsWorker{st: st, log: log}, nil
}

// Work implements river.Worker.
func (w *StatsWorker) Work(ctx context.Context, _ *river.Job[StatsArgs]) error {
	err := w.Run(ctx)
	w.log.Info("stats refreshed", "err", err)
	return err
}

// Run refreshes both views, concurrently so readers are never blocked.
func (w *StatsWorker) Run(ctx context.Context) error {
	if err := w.st.RefreshRuleWeeklyStats(ctx); err != nil {
		return fmt.Errorf("worker: refresh rule_weekly_stats: %w", err)
	}
	if err := w.st.RefreshReviewDailyStats(ctx); err != nil {
		return fmt.Errorf("worker: refresh review_daily_stats: %w", err)
	}
	return nil
}
