package worker

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// A push lands while the review runs: nothing is posted, and the check
// on the old head is completed as superseded rather than left running.
func TestReviewSupersededBeforePosting(t *testing.T) {
	e := setup(t, repo)
	e.fake.headAfterCheck = strings.Repeat("c", 40)
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Status != StatusSuperseded {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	f := e.fake
	if len(f.reviews) != 0 || len(f.checkUpdates) != 1 || fmt.Sprint(f.checkUpdates[0]["output"]) == "" ||
		!strings.Contains(fmt.Sprint(f.checkUpdates[0]["output"]), "Superseded") {
		t.Errorf("reviews = %+v, check updates = %+v", f.reviews, f.checkUpdates)
	}
}

// blockingModel holds every generator call until its request is cancelled,
// so a test can supersede a review in flight.
type blockingModel struct {
	next    http.RoundTripper
	once    sync.Once
	started chan struct{}
}

func (b *blockingModel) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get(llm.HeaderStage) == llm.StageGenerator {
		b.once.Do(func() { close(b.started) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return b.next.RoundTrip(r)
}

// TestRunningReviewCancelled supersedes a running review the way routing
// does (JobCancelTx in the push's transaction): River cancels its context
// on the pod holding it, and the review records itself superseded and
// completes its check.
func TestRunningReviewCancelled(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	model := &blockingModel{started: make(chan struct{})}
	e := setupWith(t, repo, func(next http.RoundTripper) http.RoundTripper { model.next = next; return model })
	e.w.cfg.Store = st
	workers := river.NewWorkers()
	river.AddWorker(workers, e.w)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{store.ReviewQueue: {MaxWorkers: 1}}, Workers: workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(ctx) }()
	if _, err := client.Insert(ctx, args(e), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-time.After(30 * time.Second):
		t.Fatal("review never reached the model")
	}

	// A push to a new head: cancel every other head's review.
	err = st.InTx(ctx, func(tx pgx.Tx, _ *queries.Queries) error {
		ids, err := store.ReviewJobsToSupersede(ctx, tx, strings.ToUpper(repo), prNum, strings.Repeat("d", 40))
		if err != nil || len(ids) != 1 {
			return fmt.Errorf("jobs to supersede = %v, %w", ids, err)
		}
		_, err = client.JobCancelTx(ctx, tx, ids[0])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Kind != river.EventKindJobCancelled {
			t.Fatalf("job %s: %v", ev.Kind, ev.Job.Errors)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("review was not cancelled")
	}
	var status string
	if err := st.Pool.QueryRow(ctx, `SELECT status FROM reviews WHERE repo = $1 AND pr_number = $2`, repo, prNum).Scan(&status); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	if status != "superseded" || len(e.fake.reviews) != 0 || len(e.fake.checkUpdates) != 1 ||
		!strings.Contains(fmt.Sprint(e.fake.checkUpdates[0]["output"]), "Superseded") {
		t.Errorf("status %s, reviews %v, check updates %v", status, e.fake.reviews, e.fake.checkUpdates)
	}

	// The keep head's own job is never a candidate.
	_ = st.InTx(ctx, func(tx pgx.Tx, _ *queries.Queries) error {
		if ids, _ := store.ReviewJobsToSupersede(ctx, tx, repo, prNum, e.head); len(ids) != 0 {
			t.Errorf("finished job offered for supersede: %v", ids)
		}
		return nil
	})
}
