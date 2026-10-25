package worker

import (
	"fmt"
	"net/http"
	"slices"
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
	t.Parallel()
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
	t.Parallel()
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

// A review that stored its finding but died at its post does not keep
// the next head from posting it, unless the post had landed (its comment
// mapping lost with the runner): then the finding is on the PR already.
func TestUnpostedFindingPostsOnNextHead(t *testing.T) {
	t.Parallel()
	// Where a runner dies: the review's post, before or after GitHub took
	// it, or the comment listing that maps a landed post.
	post := func(r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reviews")
	}
	list := func(r *http.Request) bool {
		return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/reviews/") && strings.HasSuffix(r.URL.Path, "/comments")
	}
	type death struct {
		at    func(*http.Request) bool
		apply bool
	}
	for _, tc := range []struct {
		name   string
		deaths []death  // one run of the first head each
		posted int      // the next head's comments
		states []string // the finding's rows, oldest first
	}{
		{"never posted", []death{{post, false}}, 1, []string{"dropped:-:not_posted", "posted:1000:"}},
		{"posted, mapping lost", []death{{post, true}}, 0, []string{"posted:-:", "dropped:-:already_posted"}},
		// The retry posts what the first run did not, then loses the mapping.
		{"retried, mapping lost", []death{{post, false}, {list, false}}, 0, []string{"posted:-:", "dropped:-:already_posted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setup(t, repo)
			e.w.cfg.Store = storetest.New(t)
			e.fake.listPosted = true
			for _, d := range tc.deaths {
				// A dead runner's later calls (a retried read) never arrive.
				dead := false
				e.fake.mu.Lock()
				e.fake.intercept = func(r *http.Request) (bool, bool) {
					if !dead && d.at(r) {
						dead = true
						return true, d.apply
					}
					return dead, false
				}
				e.fake.mu.Unlock()
				if out, err := e.w.Review(t.Context(), args(e)); err == nil {
					t.Fatalf("review survived its death: %+v", out)
				}
			}
			e.fake.mu.Lock()
			e.fake.intercept = nil
			e.fake.mu.Unlock()
			e.push(t, map[string]string{"main.go": headMain, "README.md": "docs\n"})
			out, err := e.w.Review(t.Context(), args(e))
			if err != nil || out.Status != StatusReviewed || out.Posted != tc.posted {
				t.Fatalf("next head = %+v, %v", out, err)
			}
			e.fake.mu.Lock()
			ids := e.fake.commentIDs()
			e.fake.mu.Unlock()
			if got := e.state(t); len(ids) != 1 || !slices.Equal(got, tc.states) {
				t.Errorf("%d comments on the PR, findings %v; want 1, %v", len(ids), got, tc.states)
			}
		})
	}
}
