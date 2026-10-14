package worker

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func attempt(id int64, guid string, code, minsAgo int) gh.Delivery {
	return gh.Delivery{ID: id, GUID: guid, StatusCode: code, DeliveredAt: t0.Add(-time.Duration(minsAgo) * time.Minute)}
}

func TestPlanRedeliveries(t *testing.T) {
	ds := []gh.Delivery{
		attempt(1, "a", 502, 30),                           // failed once: redeliver
		attempt(2, "b", 502, 30), attempt(3, "b", 200, 20), // a redelivery succeeded
		attempt(4, "c", 0, 30),                                                       // timed out, but the receiver stored it
		attempt(5, "d", 500, 50), attempt(6, "d", 500, 40), attempt(7, "d", 503, 30), // three tries: give up
		attempt(8, "e", 500, 50), attempt(9, "e", 500, 10), // redeliver the latest attempt
		{ID: 10, StatusCode: 500}, // no GUID
	}
	got := PlanRedeliveries(ds, map[string]bool{"c": true})
	var ids []int64
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	if !slices.Equal(ids, []int64{1, 9}) {
		t.Errorf("redeliver = %v", ids)
	}
}

func TestSearchBatches(t *testing.T) {
	var repos []string
	for i := range 40 {
		repos = append(repos, "acme/service-number-"+strings.Repeat("x", i%5))
	}
	repos = append(repos, "Other/app")
	b := searchBatches(repos, t0)
	if len(b["other"]) != 1 || len(b["acme"]) < 2 {
		t.Fatalf("batches = %v", b)
	}
	total := 0
	for _, batches := range b {
		for _, batch := range batches {
			total += len(batch)
			if n := len("is:pr is:open updated:>=2026-10-13T12:00:00Z") + queryLen(batch); n > maxSearchQuery {
				t.Errorf("query of %d characters", n)
			}
		}
	}
	if total != len(repos) {
		t.Errorf("%d repos batched, want %d", total, len(repos))
	}
}

func exampleSnapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "cfg-test")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestPlanSweep(t *testing.T) {
	snap := exampleSnapshot(t)
	if got := SweepRepos(snap); !slices.Equal(got, []string{repo}) {
		t.Fatalf("sweep repos = %v", got)
	}
	// The example repo reviews drafts but not forks.
	prs := []gh.OpenPR{
		{Repo: repo, Number: 1, HeadSHA: "a", HeadRepo: repo},
		{Repo: repo, Number: 2, HeadSHA: "b", HeadRepo: repo}, // reviewed already
		{Repo: repo, Number: 3, HeadSHA: "c", HeadRepo: repo, Draft: true},
		{Repo: repo, Number: 4, HeadSHA: "d", HeadRepo: "someone/fork"},
		{Repo: repo, Number: 5, HeadSHA: "e"}, // deleted fork
		{Repo: "acme/not-opted-in", Number: 6, HeadSHA: "f", HeadRepo: "acme/not-opted-in"},
	}
	got, err := PlanSweep(snap, prs, func(pr gh.OpenPR) (bool, error) { return pr.Number == 2, nil })
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, pr := range got {
		nums = append(nums, pr.Number)
	}
	if !slices.Equal(nums, []int{1, 3}) {
		t.Errorf("enqueue = %v", nums)
	}
	// Without review_drafts, the draft is skipped too.
	rc := snap.Repos[repo]
	rc.ReviewDrafts = false
	rc.Forks = &schema.Forks{Review: true}
	snap.Repos[repo] = rc
	got, _ = PlanSweep(snap, prs, func(gh.OpenPR) (bool, error) { return false, nil })
	nums = nil
	for _, pr := range got {
		nums = append(nums, pr.Number)
	}
	if !slices.Equal(nums, []int{1, 2, 4, 5}) {
		t.Errorf("enqueue with forks, no drafts = %v", nums)
	}
	// Disabled globally: nothing is swept.
	snap.Global.Enabled = false
	if len(SweepRepos(snap)) != 0 {
		t.Error("swept while disabled globally")
	}
}

type fakeDeliveries struct {
	ds          []gh.Delivery
	redelivered []int64
}

func (f *fakeDeliveries) HookDeliveries(context.Context, time.Time) ([]gh.Delivery, error) {
	return f.ds, nil
}
func (f *fakeDeliveries) Redeliver(_ context.Context, id int64) error {
	f.redelivered = append(f.redelivered, id)
	return nil
}

func TestReconcileWorker(t *testing.T) {
	st := storetest.New(t)
	stored, missed := uuid.New(), uuid.New()
	if _, err := st.InsertGHEvent(t.Context(), queries.InsertGHEventParams{DeliveryID: stored, Event: "pull_request", InstallationID: 1}); err != nil {
		t.Fatal(err)
	}
	f := &fakeDeliveries{ds: []gh.Delivery{attempt(1, stored.String(), 504, 5), attempt(2, missed.String(), 502, 5)}}
	w, err := NewReconcileWorker(st, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Work(t.Context(), &river.Job[ReconcileArgs]{}); err != nil {
		t.Fatal(err)
	}
	// The receiver stored the first despite GitHub seeing a timeout.
	if !slices.Equal(f.redelivered, []int64{2}) {
		t.Errorf("redelivered = %v", f.redelivered)
	}
}

type fakeSearch struct{ prs []gh.OpenPR }

func (f *fakeSearch) RepoInstallation(context.Context, string) (int64, error) { return 42, nil }
func (f *fakeSearch) SearchOpenPRs(context.Context, int64, []string, time.Time) ([]gh.OpenPR, error) {
	return f.prs, nil
}

func TestSweepWorker(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	snap := exampleSnapshot(t)
	if _, err := st.Pool.Exec(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, attempt, status)
		VALUES ($1, 2, 'b', 'x', 'v', 'production', 1, 'done')`, repo); err != nil {
		t.Fatal(err)
	}
	search := &fakeSearch{prs: []gh.OpenPR{
		{Repo: repo, Number: 1, HeadSHA: "a", HeadRepo: repo},
		{Repo: repo, Number: 2, HeadSHA: "b", HeadRepo: repo},
	}}
	sw, err := NewSweepWorker(st, search, func(context.Context) (*config.Snapshot, error) { return snap, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, sw)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{MaintenanceQueue: {MaxWorkers: 1}}, Workers: workers, SkipUnknownJobCheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(ctx) }()
	for range 2 { // a second sweep finds the same head queued and adds nothing
		// The sweep is unique per period; drop the finished one so it runs again.
		if _, err := st.Pool.Exec(ctx, `DELETE FROM river_job WHERE kind = 'sweep_prs'`); err != nil {
			t.Fatal(err)
		}
		if res, err := client.Insert(ctx, SweepArgs{}, nil); err != nil || res.UniqueSkippedAsDuplicate {
			t.Fatalf("insert sweep = %+v, %v", res, err)
		}
		select {
		case ev := <-events:
			if ev.Kind != river.EventKindJobCompleted {
				t.Fatalf("sweep %s: %v", ev.Kind, ev.Job.Errors)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("sweep did not finish")
		}
	}
	res, err := client.JobList(ctx, river.NewJobListParams().Kinds("pr_review").States(rivertype.JobStateAvailable))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Jobs) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(res.Jobs))
	}
	var got store.PRReviewArgs
	if err := json.Unmarshal(res.Jobs[0].EncodedArgs, &got); err != nil {
		t.Fatal(err)
	}
	if got != (store.PRReviewArgs{InstallationID: 42, Repo: repo, PR: 1, HeadSHA: "a"}) {
		t.Errorf("queued = %+v", got)
	}
}
