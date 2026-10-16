package store

import (
	"context"
	"fmt"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/store/queries"
)

// CallRecorder writes gateway calls to model_calls (cost ledger and the
// provider data-egress log).
type CallRecorder struct {
	Store *Store
}

// RecordCall implements llm.Recorder.
func (r CallRecorder) RecordCall(ctx context.Context, c llm.Call) error {
	in, cached, out := int32(c.Usage.InputTokens), int32(c.Usage.CacheReadTokens), int32(c.Usage.OutputTokens)
	latency := int32(c.Latency.Milliseconds())
	cost := c.CostUSD
	repo, caller := c.Repo, c.Caller
	if err := r.Store.InsertModelCall(ctx, queries.InsertModelCallParams{
		ReviewID:     c.ReviewID,
		RuleRunID:    c.RuleRunID,
		BatchID:      c.BatchID,
		Repo:         &repo,
		Caller:       &caller,
		Stage:        c.Stage,
		Provider:     c.Provider,
		Model:        c.Model,
		Failover:     c.Failover,
		DataClasses:  c.DataClasses,
		InputTokens:  &in,
		CachedTokens: &cached,
		OutputTokens: &out,
		CostUsd:      &cost,
		LatencyMs:    &latency,
	}); err != nil {
		return fmt.Errorf("store: insert model call: %w", err)
	}
	return nil
}
