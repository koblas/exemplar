package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestLateLinterDuplicate(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap := exampleSnapshot(t)
	rc := snap.Repos[repo]
	rc.Linters = []string{"golangci-lint"}
	snap.Repos[repo] = rc
	noWait := 0
	snap.Global.LinterWait = &noWait
	for i := range snap.Rules {
		if snap.Rules[i].ID == "propagate-context" {
			snap.Rules[i].LinterMap = map[string][]string{"golangci-lint": {"noctx"}}
		}
	}
	snapshot := func(context.Context) (*config.Snapshot, error) { return snap, nil }
	e.w.cfg.Snapshot = snapshot
	// The linter is still running when the review posts.
	e.fake.linterRunning = 1
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Posted != 1 || out.LinterDuplicates != 0 {
		t.Fatalf("review = %+v, %v", out, err)
	}

	lw, err := NewLinterCheckWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	check := store.LinterCheckArgs{InstallationID: inst, Repo: repo, PR: prNum, HeadSHA: e.head, Check: "GolangCI-Lint"}

	// A review of the head still queued: wait for what it posts.
	inserter, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inserter.Insert(t.Context(), args(e), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := lw.Check(t.Context(), check); !errors.Is(err, errReviewRunning) {
		t.Fatalf("with a review queued: %v", err)
	}
	if _, err := st.Pool.Exec(t.Context(), `DELETE FROM river_job`); err != nil {
		t.Fatal(err)
	}

	got, err := lw.Check(t.Context(), check)
	if err != nil || got.Compared != 1 || got.Minimized != 1 {
		t.Fatalf("check = %+v, %v", got, err)
	}
	ids := e.fake.commentIDs()
	if len(e.fake.minimized) != 1 || e.fake.minimized[0] != fmt.Sprintf("PRRC_%d", ids[0]) {
		t.Errorf("minimized = %v, comments %v", e.fake.minimized, ids)
	}
	if len(e.fake.replies) != 1 || e.fake.replies[0]["in_reply_to"] != float64(ids[0]) ||
		!strings.Contains(fmt.Sprint(e.fake.replies[0]["body"]), "`golangci-lint` reports this too") {
		t.Errorf("replies = %v", e.fake.replies)
	}
	var state, reason string
	if err := st.Pool.QueryRow(t.Context(), `SELECT state, coalesce(suppressed_reason, '') FROM findings WHERE rule_id = 'propagate-context'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "minimized" || reason != "linter_duplicate" {
		t.Errorf("finding = %s %s", state, reason)
	}

	// A redelivered event finds nothing posted: no second reply.
	if got, err := lw.Check(t.Context(), check); err != nil || got.Ignored == "" || len(e.fake.replies) != 1 || len(e.fake.minimized) != 1 {
		t.Errorf("second check = %+v, %v (replies %d)", got, err, len(e.fake.replies))
	}

	for name, mod := range map[string]func(*store.LinterCheckArgs){
		"not a configured linter": func(a *store.LinterCheckArgs) { a.Check = "unit-tests" },
		"repo not configured":     func(a *store.LinterCheckArgs) { a.Repo = "acme/elsewhere" },
	} {
		a := check
		mod(&a)
		if got, err := lw.Check(t.Context(), a); err != nil || got.Ignored == "" {
			t.Errorf("%s = %+v, %v", name, got, err)
		}
	}
	rc.Shadow = true
	snap.Repos[repo] = rc
	if got, err := lw.Check(t.Context(), check); err != nil || !strings.Contains(got.Ignored, "shadow") {
		t.Errorf("shadow = %+v, %v", got, err)
	}
}
