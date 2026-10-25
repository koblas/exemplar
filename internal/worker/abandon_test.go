package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// The review dispatcher abandons a job only when River will not retry
// it: a failure on its last attempt, not a cancel, a supersede or a wait.
func TestDispatchAbandonsLastAttempt(t *testing.T) {
	t.Parallel()
	snap := exampleSnapshot(t)
	for name, tc := range map[string]struct {
		exit    Exit
		err     error
		attempt int
		want    bool
	}{
		"failed, retried":   {ExitError, nil, 2, false},
		"failed, last":      {ExitError, nil, 3, true},
		"killed, last":      {Exit(137), nil, 3, true},
		"launch failed":     {ExitError, errors.New("api server down"), 3, true},
		"done, last":        {ExitDone, nil, 3, false},
		"cancelled, last":   {ExitCancel, nil, 3, false},
		"superseded, last":  {ExitSuperseded, nil, 3, false},
		"no capacity, last": {ExitError, fmt.Errorf("busy: %w", ErrNoCapacity), 3, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var abandoned []error
			d, err := NewReviewDispatcher(DispatchConfig{
				Launcher: launchFunc(func(context.Context, Task) (Exit, error) { return tc.exit, tc.err }),
				Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil },
				Abandon:  func(_ context.Context, _ store.PRReviewArgs, err error) { abandoned = append(abandoned, err) },
			})
			if err != nil {
				t.Fatal(err)
			}
			err = d.Work(t.Context(), &river.Job[store.PRReviewArgs]{JobRow: &rivertype.JobRow{ID: 7, Kind: "pr_review", Attempt: tc.attempt, MaxAttempts: 3}})
			if got := len(abandoned) == 1; got != tc.want || len(abandoned) > 1 || tc.want && !errors.Is(abandoned[0], err) {
				t.Errorf("abandoned = %v after %v; want %v", abandoned, err, tc.want)
			}
		})
	}
}

// An abandoned review is recorded incomplete (which Re-run re-runs) and
// its check run completed with the Re-run action. A head without a
// review, or with a superseded one, is left alone.
func TestAbandonReview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		die    string // where the review's runner dies
		status string // the review row before Abandon
	}{
		{"killed before its post", "/reviews", "done"},
		{"killed after its post", "/check-runs/", "done"},
		{"superseded", "", "superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := setup(t, repo)
			st := storetest.New(t)
			e.w.cfg.Store = st
			e.fake.listPosted = true
			ab := Abandoner{Store: st, GitHub: e.fake}
			ctx := t.Context()
			// Nothing to close before the head has a review.
			ab.Abandon(ctx, args(e), errRunnerFailed)
			if n := len(e.fake.checkUpdates); n != 0 {
				t.Fatalf("abandoned a head with no review: %d check updates", n)
			}

			if tc.die == "" {
				e.fake.headAfterCheck = strings.Repeat("c", 40)
			}
			dead := false
			e.fake.mu.Lock()
			e.fake.intercept = func(r *http.Request) (bool, bool) {
				if !dead && tc.die != "" && r.Method != http.MethodGet && strings.Contains(r.URL.Path, tc.die) &&
					(tc.die != "/check-runs/" || r.Method == http.MethodPatch) {
					dead = true
					return true, false // dies before GitHub takes the call
				}
				return dead, false
			}
			e.fake.mu.Unlock()
			out, err := e.w.Review(ctx, args(e))
			e.fake.mu.Lock()
			e.fake.intercept = nil
			e.fake.mu.Unlock()
			if (tc.die != "") != (err != nil) {
				t.Fatalf("review = %+v, %v", out, err)
			}
			var status string
			row := `SELECT status FROM reviews WHERE head_sha = $1`
			if err := st.Pool.QueryRow(ctx, row, e.head).Scan(&status); err != nil || status != tc.status {
				t.Fatalf("review before = %s, %v; want %s", status, err, tc.status)
			}
			updates := len(e.fake.checkUpdates)

			ab.Abandon(ctx, args(e), errRunnerFailed)
			if err := st.Pool.QueryRow(ctx, row, e.head).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if tc.status == "superseded" {
				if status != "superseded" || len(e.fake.checkUpdates) != updates {
					t.Errorf("superseded review abandoned: %s, %d check updates", status, len(e.fake.checkUpdates)-updates)
				}
				return
			}
			if status != reviewIncomplete || len(e.fake.checkUpdates) != updates+1 || !e.fake.checksDone[1] {
				t.Fatalf("after abandon: review %s, %d check updates, check done %v", status, len(e.fake.checkUpdates)-updates, e.fake.checksDone[1])
			}
			u := e.fake.checkUpdates[updates]
			if out, _ := u["output"].(map[string]any); out["title"] != "Review did not complete" || !strings.Contains(fmt.Sprint(u["actions"]), "rerun") {
				t.Errorf("check update = %v", u)
			}
			// Abandoning again finds no check run left open.
			ab.Abandon(ctx, args(e), errRunnerFailed)
			if len(e.fake.checkUpdates) != updates+1 {
				t.Errorf("completed the check run twice")
			}
		})
	}
}
