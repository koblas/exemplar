package worker

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// TestSampledForBaseline: the draw is fixed per head, rate 0 and 1 are
// exact, and over many heads the share sampled is near the rate in every
// repo (the stratum the spec names besides size).
func TestSampledForBaseline(t *testing.T) {
	t.Parallel()
	rnd := rand.New(rand.NewPCG(1, 2))
	const heads = 20000
	for _, repo := range []string{"acme/app", "acme/monorepo", "other/tool"} {
		n := 0
		for range heads {
			pr, head := rnd.IntN(5000)+1, fmt.Sprintf("%040x", rnd.Uint64())
			s := sampledForBaseline(0.10, repo, pr, head)
			if s != sampledForBaseline(0.10, repo, pr, head) {
				t.Fatalf("%s#%d@%s drawn two ways", repo, pr, head)
			}
			if !sampledForBaseline(1, repo, pr, head) || sampledForBaseline(0, repo, pr, head) {
				t.Fatalf("%s#%d@%s: rate 1 or 0 not exact", repo, pr, head)
			}
			if s {
				n++
			}
		}
		if share := float64(n) / heads; share < 0.09 || share > 0.11 {
			t.Errorf("%s: sampled %.3f of heads at rate 0.10", repo, share)
		}
	}
}

// TestReviewRunsSampledBaseline: a sampled head gets one caller=baseline
// review after its production review, writes nothing more to GitHub, and
// is not run again when the head is reviewed again; an unsampled head
// gets none.
func TestReviewRunsSampledBaseline(t *testing.T) {
	t.Parallel()
	for _, rate := range []float64{1, 0} {
		t.Run(fmt.Sprintf("rate=%v", rate), func(t *testing.T) {
			t.Parallel()
			st := storetest.New(t)
			e := setup(t, repo)
			e.w.cfg.Store = st
			snap := exampleSnapshot(t)
			snap.Global.Baseline.SampleRate = rate
			e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }

			out, err := e.w.Review(t.Context(), args(e))
			if err != nil || out.Status != StatusReviewed || out.Posted != 1 {
				t.Fatalf("outcome = %+v, %v", out, err)
			}
			if got := len(e.fake.reviews); got != 1 {
				t.Errorf("%d GitHub reviews, want the production one only", got)
			}
			if got := len(e.fake.checks); got != 1 {
				t.Errorf("%d check runs, want the production one only", got)
			}
			count := func() (int, string) {
				t.Helper()
				var n int
				var status string
				if err := st.Pool.QueryRow(t.Context(), `SELECT count(*), coalesce(max(status), '') FROM reviews
					WHERE caller = 'baseline' AND repo = $1 AND pr_number = $2 AND head_sha = $3`, repo, prNum, e.head).Scan(&n, &status); err != nil {
					t.Fatal(err)
				}
				return n, status
			}
			n, status := count()
			want := map[float64]int{1: 1, 0: 0}[rate]
			if n != want || (want == 1 && status != "done") {
				t.Fatalf("rate %v: %d baseline reviews (%q), want %d done", rate, n, status, want)
			}

			// Reviewed again (a retry, a Re-run): the baseline is not repeated.
			if _, err := e.w.Review(t.Context(), args(e)); err != nil {
				t.Fatal(err)
			}
			if n, _ := count(); n != want {
				t.Errorf("after a second review: %d baseline reviews, want %d", n, want)
			}
		})
	}
}
