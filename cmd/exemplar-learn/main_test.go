package main

import (
	"errors"
	"testing"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// A subcommand runs only while it holds its job lock; a run that finds
// it held does nothing and succeeds.
func TestRunLocked(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	url := storetest.EmptyURL(t)
	errJob := errors.New("job failed")
	ran := 0
	job := func() error { ran++; return errJob }

	if err := runLocked(ctx, "demote", url, job); !errors.Is(err, errJob) || ran != 1 {
		t.Fatalf("free lock: err %v, ran %d", err, ran)
	}
	release, ok, err := store.TryJobLock(ctx, url, "demote")
	if err != nil || !ok {
		t.Fatalf("hold the lock: %v, %v", ok, err)
	}
	if err := runLocked(ctx, "demote", url, job); err != nil || ran != 1 {
		t.Errorf("held lock: err %v, ran %d", err, ran)
	}
	if err := runLocked(ctx, "digest", url, job); !errors.Is(err, errJob) || ran != 2 {
		t.Errorf("another job: err %v, ran %d", err, ran)
	}
	release()
	// The first run released its lock when it returned.
	if err := runLocked(ctx, "demote", url, job); !errors.Is(err, errJob) || ran != 3 {
		t.Errorf("after release: err %v, ran %d", err, ran)
	}
	// No database: nothing to lock, the subcommand reports the setting.
	if err := runLocked(ctx, "demote", "", job); !errors.Is(err, errJob) || ran != 4 {
		t.Errorf("no database: err %v, ran %d", err, ran)
	}
}
