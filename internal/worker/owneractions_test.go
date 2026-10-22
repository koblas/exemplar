package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/owneract"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// A GitHub failure is retried, and the last attempt records the action as
// failed so the UI shows it.
func TestOwnerActionWorker(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := t.Context()
	snap := &config.Snapshot{Rules: []config.Rule{{ID: "r", Status: schema.StatusActive, Owner: "@acme/sec", Scope: "global", Path: "rules/r.yaml"}}}
	w, err := NewOwnerActionWorker(owneract.Runner{
		Store: st, PlatformTeam: "@platform/devex",
		Repo:   func(context.Context) (owneract.Repo, error) { return nil, errors.New("no token") },
		Member: func(context.Context, string, string) (bool, error) { return true, nil },
	}, func(context.Context) (*config.Snapshot, error) { return snap, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateOwnerAction(ctx, queries.CreateOwnerActionParams{Action: owneract.ShadowRule, Target: "r", Actor: "dana"})
	if err != nil {
		t.Fatal(err)
	}
	job := func(attempt int) *river.Job[store.OwnerActionArgs] {
		return &river.Job[store.OwnerActionArgs]{JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: 3}, Args: store.OwnerActionArgs{ID: id}}
	}
	if err := w.Work(ctx, job(1)); err == nil {
		t.Fatal("first attempt swallowed the error")
	}
	if a, _ := st.GetOwnerAction(ctx, id); a.Status != "queued" {
		t.Errorf("after a retryable failure = %s", a.Status)
	}
	if err := w.Work(ctx, job(3)); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetOwnerAction(ctx, id); a.Status != owneract.StatusFailed || a.Result == nil || !strings.HasSuffix(*a.Result, "no token") {
		t.Errorf("after the last attempt = %+v", a)
	}
}
