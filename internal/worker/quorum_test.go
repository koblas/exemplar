package worker

import (
	"context"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// A finding of a rule with verify: quorum is stored with its quorum: the
// verifiers asked and how many confirmed (FR-V7).
func TestReviewStoresQuorum(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap := exampleSnapshot(t)
	for i := range snap.Rules {
		if snap.Rules[i].ID == "propagate-context" {
			snap.Rules[i].Verify = "quorum"
		}
	}
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("review = %+v, %v", out, err)
	}
	var n, agreed int
	if err := st.Pool.QueryRow(t.Context(), `SELECT verify_quorum_n, verify_quorum_agreed FROM findings WHERE rule_id = 'propagate-context'`).Scan(&n, &agreed); err != nil {
		t.Fatal(err)
	}
	if n != 3 || agreed != 3 {
		t.Errorf("stored quorum = %d of %d", agreed, n)
	}
	var single *int
	if err := st.Pool.QueryRow(t.Context(), `SELECT verify_quorum_n FROM findings WHERE rule_id <> 'propagate-context' LIMIT 1`).Scan(&single); err == nil && single != nil {
		t.Errorf("single-verifier finding stored a quorum: %d", *single)
	}
}
