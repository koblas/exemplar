package worker

import (
	"context"
	"errors"
	"fmt"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Config snapshots (spec §2.4 C3): control stores each validated
// exemplar-config version, and a runner loads the version its job was
// dispatched with instead of reading the config repo.

// SnapshotRetentionDays is how long an unused config version is kept.
const SnapshotRetentionDays = 30

// PutSnapshot stores snap's tree under its version.
func PutSnapshot(ctx context.Context, st *store.Store, snap *config.Snapshot) error {
	tree, ok := snap.Files.(fstest.MapFS)
	if !ok {
		var err error
		if tree, err = config.ReadTree(snap.Files); err != nil {
			return fmt.Errorf("worker: config %s: %w", snap.Version, err)
		}
	}
	files, err := config.EncodeTree(tree)
	if err != nil {
		return fmt.Errorf("worker: config %s: %w", snap.Version, err)
	}
	if err := st.PutConfigSnapshot(ctx, queries.PutConfigSnapshotParams{Version: snap.Version, Files: files}); err != nil {
		return fmt.Errorf("worker: store config %s: %w", snap.Version, err)
	}
	return nil
}

// StoredSnapshot loads and validates a stored config version.
func StoredSnapshot(ctx context.Context, st *store.Store, version string) (*config.Snapshot, error) {
	files, err := st.GetConfigSnapshot(ctx, version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("worker: config %s %w", version, errNotStored)
	}
	if err != nil {
		return nil, fmt.Errorf("worker: get config %s: %w", version, err)
	}
	tree, err := config.DecodeTree(files)
	if err != nil {
		return nil, fmt.Errorf("worker: config %s: %w", version, err)
	}
	snap, err := config.LoadSnapshot(tree, version)
	if err != nil {
		return nil, fmt.Errorf("worker: load config %s: %w", version, err)
	}
	return snap, nil
}

// Snapshots returns the version loader for an in-process runner: the
// current snapshot when it is the version asked for, else the stored one.
func Snapshots(st *store.Store, current func(context.Context) (*config.Snapshot, error)) func(context.Context, string) (*config.Snapshot, error) {
	return func(ctx context.Context, version string) (*config.Snapshot, error) {
		if snap, err := current(ctx); err == nil && snap.Version == version {
			return snap, nil
		}
		if st == nil {
			return nil, fmt.Errorf("worker: config %s %w", version, errNotStored)
		}
		return StoredSnapshot(ctx, st, version)
	}
}
