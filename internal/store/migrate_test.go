package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// migrateLockKey mirrors the store's advisory lock key.
const migrateLockKey int64 = 0x6578656d706c6172

func openEmpty(ctx context.Context, t *testing.T, url string) *store.Store {
	t.Helper()
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestMigrateWaitsForLock: Migrate does nothing while another session holds
// the migration lock, and runs once it is released.
func TestMigrateWaitsForLock(t *testing.T) {
	ctx := t.Context()
	url := storetest.EmptyURL(t)
	s := openEmpty(ctx, t, url)

	holder, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect holder: %v", err)
	}
	defer func() { _ = holder.Close(context.Background()) }()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		t.Fatalf("take lock: %v", err)
	}

	// wallclock: Postgres's advisory lock cannot block inside a synctest
	// bubble; the deadline is how long Migrate must keep waiting.
	wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
	err = s.Migrate(wctx)
	wcancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Migrate under a held lock = %v, want deadline exceeded", err)
	}
	var applied bool
	if err := holder.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&applied); err != nil {
		t.Fatalf("check goose table: %v", err)
	}
	if applied {
		t.Fatal("Migrate ran migrations without the lock")
	}

	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrateLockKey); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after release: %v", err)
	}
}

// TestMigrateConcurrent: pods starting together all migrate an empty
// database without errors. It guards the lock's wait: sessions blocked in
// pg_advisory_lock would deadlock the holder's CREATE INDEX CONCURRENTLY.
func TestMigrateConcurrent(t *testing.T) {
	ctx := t.Context()
	url := storetest.EmptyURL(t)
	const pods = 8
	stores := make([]*store.Store, pods)
	for i := range stores {
		stores[i] = openEmpty(ctx, t, url)
	}
	var wg sync.WaitGroup
	errs := make([]error, pods)
	for i, s := range stores {
		wg.Go(func() { errs[i] = s.Migrate(ctx) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("pod %d: Migrate: %v", i, err)
		}
	}
}
