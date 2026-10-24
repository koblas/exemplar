package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Retention: a daily job enforces the periods in
// exemplar-config/platform/retention.yaml (owned by Security).
//   - gh_events.payload is nulled; the row stays as the idempotency key.
//   - model_calls and findings (with their calibration labels) are deleted.
//   - approval_decisions are deleted by their review's age.
//   - evaluations (`exemplar review` results) and playground runs keep
//     findings' period; an evaluation's
//     diff whose job never finished is cleared after a day.
//
// Transcripts are not stored yet and feedback_events does not exist, so
// their periods have nothing to apply to.

// staleDiffDays is how long an evaluation's diff may wait for its job.
const staleDiffDays = 1

// RetainEvery is how often retention runs.
const RetainEvery = 24 * time.Hour

const (
	retentionBatch = 5000
	// maxRetentionBatches bounds one run per table; a larger backlog
	// continues the next day.
	maxRetentionBatches = 200
)

// RetentionArgs is the daily retention job.
type RetentionArgs struct{}

// Kind implements river.JobArgs.
func (RetentionArgs) Kind() string { return "retention" }

// InsertOpts runs it on the maintenance queue, once a period.
func (RetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: MaintenanceQueue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: RetainEvery}}
}

// RetentionWorker applies the retention periods.
type RetentionWorker struct {
	river.WorkerDefaults[RetentionArgs]

	st       *store.Store
	snapshot func(context.Context) (*config.Snapshot, error)
	now      func() time.Time
	log      *slog.Logger
	batch    int32
}

// NewRetentionWorker returns a RetentionWorker.
func NewRetentionWorker(st *store.Store, snapshot func(context.Context) (*config.Snapshot, error), log *slog.Logger) (*RetentionWorker, error) {
	if st == nil || snapshot == nil {
		return nil, fmt.Errorf("%w: retention needs a store and the config", errMissingDependency)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &RetentionWorker{st: st, snapshot: snapshot, now: time.Now, log: log, batch: retentionBatch}, nil
}

// Timeout bounds one run.
func (w *RetentionWorker) Timeout(*river.Job[RetentionArgs]) time.Duration { return time.Hour }

// RetentionResult counts what one run removed, per table.
type RetentionResult map[string]int64

// Work implements river.Worker.
func (w *RetentionWorker) Work(ctx context.Context, _ *river.Job[RetentionArgs]) error {
	res, err := w.Run(ctx)
	w.log.Info("retention applied", "result", res, "err", err)
	return err
}

// Run applies every period once.
func (w *RetentionWorker) Run(ctx context.Context) (RetentionResult, error) {
	snap, err := w.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	r := snap.Retention
	type step func(context.Context, pgtype.Timestamptz, int32) (int64, error)
	policies := []struct {
		name string
		days int
		step step
	}{
		{"gh_events.payload", r.GHEventsPayloadDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.NullExpiredPayloads(ctx, queries.NullExpiredPayloadsParams{Cutoff: c, Batch: n})
		}},
		{"model_calls", r.ModelCallsDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.DeleteExpiredModelCalls(ctx, queries.DeleteExpiredModelCallsParams{Cutoff: c, Batch: n})
		}},
		{"findings", r.FindingsDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.DeleteExpiredFindings(ctx, queries.DeleteExpiredFindingsParams{Cutoff: c, Batch: n})
		}},
		{"approval_decisions", r.ApprovalDecisionsDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.DeleteExpiredApprovalDecisions(ctx, queries.DeleteExpiredApprovalDecisionsParams{Cutoff: c, Batch: n})
		}},
		{"evaluations", r.FindingsDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.DeleteExpiredEvaluations(ctx, queries.DeleteExpiredEvaluationsParams{Cutoff: c, Batch: n})
		}},
		{"playground_runs", r.FindingsDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.DeleteExpiredPlaygroundRuns(ctx, queries.DeleteExpiredPlaygroundRunsParams{Cutoff: c, Batch: n})
		}},
		{"config_snapshots", SnapshotRetentionDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.DeleteExpiredConfigSnapshots(ctx, queries.DeleteExpiredConfigSnapshotsParams{Cutoff: c, Keep: snap.Version, Batch: n})
		}},
		{"runner_claims", claimRetentionDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.ClearExpiredRunnerClaims(ctx, queries.ClearExpiredRunnerClaimsParams{Cutoff: c, Batch: n})
		}},
		{"evaluations.diff", staleDiffDays, func(ctx context.Context, c pgtype.Timestamptz, n int32) (int64, error) {
			return w.st.NullStaleEvaluationDiffs(ctx, queries.NullStaleEvaluationDiffsParams{Cutoff: c, Batch: n})
		}},
	}
	out := RetentionResult{}
	var errs []error
	for _, p := range policies {
		if p.days < 1 {
			// The schema requires at least 1; never read 0 as "delete all".
			errs = append(errs, fmt.Errorf("retention: %s %w; skipped", p.name, errNoPeriod))
			continue
		}
		cutoff := pgtype.Timestamptz{Time: w.now().AddDate(0, 0, -p.days), Valid: true}
		for range maxRetentionBatches {
			n, err := p.step(ctx, cutoff, w.batch)
			out[p.name] += n
			if err != nil {
				errs = append(errs, fmt.Errorf("retention: %s: %w", p.name, err))
				break
			}
			if n < int64(w.batch) {
				break
			}
		}
	}
	return out, errors.Join(errs...)
}
