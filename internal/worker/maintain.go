package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Webhook safety nets: GitHub does not retry failed deliveries.

// MaintenanceQueue runs the periodic reconciler and sweeper.
const MaintenanceQueue = "maintenance"

// Periodic job settings.
const (
	ReconcileEvery = 5 * time.Minute
	// ReconcileLookback is how far back deliveries are checked; GitHub
	// keeps three days of them.
	ReconcileLookback = 6 * time.Hour
	// MaxRedeliveries is the most attempts (first plus redeliveries) the
	// reconciler lets one event reach; past it the event is left alone.
	MaxRedeliveries = 3
	SweepEvery      = 10 * time.Minute
	// SweepLookback overlaps runs so a PR updated during the last sweep is
	// not missed; reviewed heads are skipped, so overlap is cheap.
	SweepLookback = time.Hour
	// maxSearchQuery is GitHub search's query length limit.
	maxSearchQuery = 256
)

// ReconcileArgs is the periodic redelivery job.
type ReconcileArgs struct{}

// Kind implements river.JobArgs.
func (ReconcileArgs) Kind() string { return "reconcile_deliveries" }

// InsertOpts runs it on the maintenance queue, once at a time.
func (ReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: MaintenanceQueue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: ReconcileEvery}}
}

// SweepArgs is the periodic missed-PR sweep.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "sweep_prs" }

// InsertOpts runs it on the maintenance queue, once at a time.
func (SweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: MaintenanceQueue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: SweepEvery}}
}

// PeriodicJobs are the reconciler and sweeper schedules. River runs them
// on its elected leader only, so one process acts per period.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(ReconcileEvery),
			func() (river.JobArgs, *river.InsertOpts) { return ReconcileArgs{}, nil },
			&river.PeriodicJobOpts{ID: "reconcile_deliveries", RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(SweepEvery),
			func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, nil },
			&river.PeriodicJobOpts{ID: "sweep_prs", RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(RetainEvery),
			func() (river.JobArgs, *river.InsertOpts) { return RetentionArgs{}, nil },
			&river.PeriodicJobOpts{ID: "retention", RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(StatsEvery),
			func() (river.JobArgs, *river.InsertOpts) { return StatsArgs{}, nil },
			&river.PeriodicJobOpts{ID: "refresh_stats", RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(ReactionsEvery),
			func() (river.JobArgs, *river.InsertOpts) { return ReactionsArgs{}, nil },
			&river.PeriodicJobOpts{ID: "poll_reactions"}),
	}
}

// PlanRedeliveries picks the attempts to redeliver: one per event (GUID)
// that no attempt delivered (2xx), that the receiver did not store anyway
// (it may have committed and then timed out), and that has had fewer than
// MaxRedeliveries attempts. It returns the latest failed attempt of each.
func PlanRedeliveries(ds []gh.Delivery, stored map[string]bool) []gh.Delivery {
	byGUID := map[string][]gh.Delivery{}
	var order []string
	for _, d := range ds {
		if d.GUID == "" {
			continue
		}
		if _, ok := byGUID[d.GUID]; !ok {
			order = append(order, d.GUID)
		}
		byGUID[d.GUID] = append(byGUID[d.GUID], d)
	}
	var out []gh.Delivery
	for _, guid := range order {
		attempts := byGUID[guid]
		if stored[strings.ToLower(guid)] || len(attempts) >= MaxRedeliveries ||
			slices.ContainsFunc(attempts, func(d gh.Delivery) bool { return d.StatusCode >= 200 && d.StatusCode < 300 }) {
			continue
		}
		latest := slices.MaxFunc(attempts, func(a, b gh.Delivery) int { return a.DeliveredAt.Compare(b.DeliveredAt) })
		out = append(out, latest)
	}
	return out
}

// Deliveries is the App's delivery log; *gh.App implements it.
type Deliveries interface {
	HookDeliveries(ctx context.Context, since time.Time) ([]gh.Delivery, error)
	Redeliver(ctx context.Context, deliveryID int64) error
}

// ReconcileWorker requests redelivery of failed webhook deliveries.
type ReconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]

	st  *store.Store
	app Deliveries
	now func() time.Time
	log *slog.Logger
}

// NewReconcileWorker returns a ReconcileWorker.
func NewReconcileWorker(st *store.Store, app Deliveries, log *slog.Logger) (*ReconcileWorker, error) {
	if st == nil || app == nil {
		return nil, errors.New("worker: the reconciler needs a store and the App")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ReconcileWorker{st: st, app: app, now: time.Now, log: log}, nil
}

// Work implements river.Worker.
func (w *ReconcileWorker) Work(ctx context.Context, _ *river.Job[ReconcileArgs]) error {
	ds, err := w.app.HookDeliveries(ctx, w.now().Add(-ReconcileLookback))
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for _, d := range ds {
		if id, err := uuid.Parse(d.GUID); err == nil {
			ids = append(ids, id)
		}
	}
	rows, err := w.st.StoredDeliveries(ctx, ids)
	if err != nil {
		return err
	}
	stored := map[string]bool{}
	for _, id := range rows {
		stored[id.String()] = true
	}
	plan := PlanRedeliveries(ds, stored)
	failed := 0
	for _, d := range plan {
		if err := w.app.Redeliver(ctx, d.ID); err != nil {
			failed++
			w.log.Warn("redelivery failed", "delivery", d.GUID, "event", d.Event, "err", err)
		}
	}
	w.log.Info("deliveries reconciled", "checked", len(ds), "redelivered", len(plan)-failed, "failed", failed)
	return nil
}

// SweepRepos are the repos the sweeper searches: opted in, enabled, not
// denylisted. Nothing when the service is disabled globally.
func SweepRepos(snap *config.Snapshot) []string {
	var out []string
	for repo := range snap.Repos {
		if snap.CheckEligible(repo) == nil {
			out = append(out, repo)
		}
	}
	slices.Sort(out)
	return out
}

// searchBatches groups one owner's repos into search queries that fit
// GitHub's query length limit.
func searchBatches(repos []string, since time.Time) map[string][][]string {
	base := len("is:pr is:open updated:>=" + since.UTC().Format("2006-01-02T15:04:05Z"))
	out := map[string][][]string{}
	for _, r := range repos {
		owner, _, _ := strings.Cut(r, "/")
		owner = strings.ToLower(owner)
		batches := out[owner]
		n := len(batches)
		if n == 0 || base+queryLen(batches[n-1])+len(" repo:"+r) > maxSearchQuery {
			batches = append(batches, nil)
			n++
		}
		batches[n-1] = append(batches[n-1], r)
		out[owner] = batches
	}
	return out
}

func queryLen(repos []string) int {
	n := 0
	for _, r := range repos {
		n += len(" repo:" + r)
	}
	return n
}

// PlanSweep picks the open PRs to enqueue: in a swept repo, not a draft
// the repo will not review, not a fork it will not review, and with a head
// that has no production review yet.
func PlanSweep(snap *config.Snapshot, prs []gh.OpenPR, reviewed func(gh.OpenPR) (bool, error)) ([]gh.OpenPR, error) {
	var out []gh.OpenPR
	for _, pr := range prs {
		rc, ok := repoConfig(snap, pr.Repo)
		if !ok || snap.CheckEligible(canonical(snap, pr.Repo)) != nil || pr.HeadSHA == "" {
			continue
		}
		if pr.Draft && !rc.ReviewDrafts {
			continue
		}
		fork := pr.HeadRepo == "" || !strings.EqualFold(pr.HeadRepo, pr.Repo)
		if fork && (rc.Forks == nil || !rc.Forks.Review) {
			continue
		}
		done, err := reviewed(pr)
		if err != nil {
			return out, err
		}
		if !done {
			out = append(out, pr)
		}
	}
	return out, nil
}

func repoConfig(snap *config.Snapshot, repo string) (schema.RepoConfig, bool) {
	rc, ok := snap.Repos[canonical(snap, repo)]
	return rc, ok
}

// canonical is repo as the config spells it.
func canonical(snap *config.Snapshot, repo string) string {
	for name := range snap.Repos {
		if strings.EqualFold(name, repo) {
			return name
		}
	}
	return repo
}

// PRSearch finds a repo's installation and its open PRs; *gh.App
// implements it.
type PRSearch interface {
	RepoInstallation(ctx context.Context, repo string) (int64, error)
	SearchOpenPRs(ctx context.Context, installationID int64, repos []string, since time.Time) ([]gh.OpenPR, error)
}

// SweepWorker enqueues reviews for open PR heads that were never reviewed,
// whatever happened to their webhooks.
type SweepWorker struct {
	river.WorkerDefaults[SweepArgs]

	st       *store.Store
	search   PRSearch
	snapshot func(context.Context) (*config.Snapshot, error)
	now      func() time.Time
	log      *slog.Logger
}

// NewSweepWorker returns a SweepWorker.
func NewSweepWorker(st *store.Store, search PRSearch, snapshot func(context.Context) (*config.Snapshot, error), log *slog.Logger) (*SweepWorker, error) {
	if st == nil || search == nil || snapshot == nil {
		return nil, errors.New("worker: the sweeper needs a store, the App and the config")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &SweepWorker{st: st, search: search, snapshot: snapshot, now: time.Now, log: log}, nil
}

// Work implements river.Worker. One owner's failure does not stop the
// others; the next sweep retries it.
func (w *SweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	snap, err := w.snapshot(ctx)
	if err != nil {
		return err
	}
	since := w.now().Add(-SweepLookback)
	client := river.ClientFromContext[pgx.Tx](ctx)
	enqueued, failed := 0, 0
	for owner, batches := range searchBatches(SweepRepos(snap), since) {
		inst, err := w.search.RepoInstallation(ctx, batches[0][0])
		if err != nil {
			failed++
			w.log.Warn("sweep: no installation", "owner", owner, "err", err)
			continue
		}
		for _, repos := range batches {
			prs, err := w.search.SearchOpenPRs(ctx, inst, repos, since)
			if err != nil {
				failed++
				w.log.Warn("sweep: search failed", "owner", owner, "err", err)
				continue
			}
			plan, err := PlanSweep(snap, prs, func(pr gh.OpenPR) (bool, error) {
				return w.st.ReviewedHead(ctx, queries.ReviewedHeadParams{Repo: pr.Repo, PrNumber: int32(pr.Number), HeadSha: pr.HeadSHA})
			})
			if err != nil {
				return err
			}
			for _, pr := range plan {
				// River's uniqueness skips heads already queued or running.
				res, err := client.Insert(ctx, store.PRReviewArgs{InstallationID: inst, Repo: pr.Repo, PR: pr.Number, HeadSHA: pr.HeadSHA}, nil)
				if err != nil {
					return fmt.Errorf("sweep: enqueue: %w", err)
				}
				if !res.UniqueSkippedAsDuplicate {
					enqueued++
				}
			}
		}
	}
	w.log.Info("sweep done", "enqueued", enqueued, "failed", failed)
	return nil
}
