package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/store/queries"
)

// BatchStore keeps gateway batch records in model_batches, so every
// gateway replica can serve any batch and usage is booked once.
type BatchStore struct {
	Store *Store
}

var _ llm.BatchStore = BatchStore{}

// SaveBatch implements llm.BatchStore.
func (b BatchStore) SaveBatch(ctx context.Context, r llm.BatchRecord) error {
	var repo *string
	if r.Repo != "" {
		repo = &r.Repo
	}
	if err := b.Store.InsertModelBatch(ctx, queries.InsertModelBatchParams{
		BatchID: r.ID, Provider: r.Provider, Model: r.Model, Stage: r.Stage, Caller: r.Caller,
		Repo: repo, DataClasses: r.DataClasses, RequestCount: int32(r.Requests),
	}); err != nil {
		return fmt.Errorf("store: insert model batch: %w", err)
	}
	return nil
}

// LookupBatch implements llm.BatchStore.
func (b BatchStore) LookupBatch(ctx context.Context, id string) (llm.BatchRecord, bool, error) {
	row, err := b.Store.GetModelBatch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return llm.BatchRecord{}, false, nil
	}
	if err != nil {
		return llm.BatchRecord{}, false, fmt.Errorf("store: get model batch: %w", err)
	}
	rec := llm.BatchRecord{
		ID: row.BatchID, Provider: row.Provider, Model: row.Model, Stage: row.Stage, Caller: row.Caller,
		DataClasses: row.DataClasses, Requests: int(row.RequestCount),
	}
	if row.Repo != nil {
		rec.Repo = *row.Repo
	}
	return rec, true, nil
}

// ClaimAccounting implements llm.BatchStore.
func (b BatchStore) ClaimAccounting(ctx context.Context, id string) (bool, error) {
	n, err := b.Store.ClaimBatchAccounting(ctx, id)
	if err != nil {
		return false, fmt.Errorf("store: claim batch accounting: %w", err)
	}
	return n == 1, nil
}
