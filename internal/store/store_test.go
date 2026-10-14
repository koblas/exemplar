package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func newReview(t *testing.T, q *queries.Queries, caller string) queries.Review {
	t.Helper()
	r, err := q.CreateReview(t.Context(), queries.CreateReviewParams{
		Repo: "acme/app", PrNumber: 7, HeadSha: "head1", BaseSha: "base1",
		ConfigVersion: "cfg1", Caller: caller, Attempt: 1,
	})
	if err != nil {
		t.Fatalf("CreateReview: %v", err)
	}
	return r
}

func TestReviewAndRunIdempotency(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()

	r1 := newReview(t, s.Queries, "production")
	r2 := newReview(t, s.Queries, "production")
	if r1.ID != r2.ID {
		t.Fatalf("CreateReview not idempotent: %d vs %d", r1.ID, r2.ID)
	}
	if other := newReview(t, s.Queries, "playground"); other.ID == r1.ID {
		t.Fatal("playground review collided with production review")
	}

	runArgs := queries.UpsertRuleRunParams{ReviewID: r1.ID, RuleID: "r", RuleVersion: "v", Lane: "exemplar", ScopedFiles: []string{"a.go"}}
	run1, err := s.UpsertRuleRun(ctx, runArgs)
	if err != nil {
		t.Fatal(err)
	}
	cov := float32(1)
	if err := s.FinishRuleRun(ctx, queries.FinishRuleRunParams{ID: run1.ID, Status: "ok", Coverage: &cov}); err != nil {
		t.Fatal(err)
	}
	run2, err := s.UpsertRuleRun(ctx, runArgs)
	if err != nil {
		t.Fatal(err)
	}
	if run2.ID != run1.ID || run2.Status != "ok" {
		t.Fatalf("retried rule run = %+v, want the finished run %d", run2, run1.ID)
	}
}

func insertFinding(t *testing.T, q *queries.Queries, runID int64, fp, state string) int64 {
	t.Helper()
	id, err := q.InsertFinding(t.Context(), queries.InsertFindingParams{
		RuleRunID: runID, Repo: "acme/app", PrNumber: 7, RuleID: "r", Lane: "exemplar",
		Severity: "important", CcLabel: "issue", Path: "a.go", Side: "RIGHT",
		StartLine: 1, EndLine: 1, Fingerprint: fp, Message: "m", State: state,
	})
	if err != nil {
		t.Fatalf("InsertFinding: %v", err)
	}
	return id
}

func TestFindingIdentity(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()

	prod := newReview(t, s.Queries, "production")
	play := newReview(t, s.Queries, "playground")
	prodRun, _ := s.UpsertRuleRun(ctx, queries.UpsertRuleRunParams{ReviewID: prod.ID, RuleID: "r", RuleVersion: "v", Lane: "exemplar", ScopedFiles: []string{}})
	playRun, _ := s.UpsertRuleRun(ctx, queries.UpsertRuleRunParams{ReviewID: play.ID, RuleID: "r", RuleVersion: "v", Lane: "exemplar", ScopedFiles: []string{}})

	a := insertFinding(t, s.Queries, prodRun.ID, "fp1", "posted")
	if again := insertFinding(t, s.Queries, prodRun.ID, "fp1", "posted"); again != a {
		t.Fatalf("same fingerprint in one rule run produced two findings: %d, %d", a, again)
	}
	insertFinding(t, s.Queries, playRun.ID, "fp1", "posted")

	open, err := s.OpenProductionFindingsByFingerprint(ctx, queries.OpenProductionFindingsByFingerprintParams{Repo: "acme/app", PrNumber: 7, Fingerprint: "fp1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != a {
		t.Fatalf("open production findings = %v, want only %d", open, a)
	}
}

func TestTransactionalUniqueEnqueue(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	client, err := river.NewClient(riverpgxv5.New(s.Pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}

	// A failed transaction leaves neither the review nor the job behind.
	errRollback := errors.New("rollback")
	err = s.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
		r := newReview(t, q, "eval")
		if _, err := client.InsertTx(ctx, tx, store.ReviewArgs{ReviewID: r.ID}, nil); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("InTx = %v", err)
	}
	if n := countJobs(ctx, t, s); n != 0 {
		t.Fatalf("%d jobs after rollback, want 0", n)
	}

	for range 2 {
		if _, err := client.Insert(ctx, store.ReviewArgs{ReviewID: 42}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := countJobs(ctx, t, s); n != 1 {
		t.Fatalf("%d jobs after duplicate enqueue, want 1", n)
	}
}

func countJobs(ctx context.Context, t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM river_job").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestModelCallSpend(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	r := newReview(t, s.Queries, "eval")
	for _, c := range []float64{0.25, 0.5} {
		cost := c
		if err := s.InsertModelCall(ctx, queries.InsertModelCallParams{
			ReviewID: &r.ID, Stage: "generator", Provider: "claude", Model: "m",
			DataClasses: []string{"diff"}, CostUsd: &cost,
		}); err != nil {
			t.Fatal(err)
		}
	}
	spend, err := s.ReviewSpend(ctx, &r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if spend != 0.75 {
		t.Fatalf("ReviewSpend = %v, want 0.75", spend)
	}
}

func TestBatchStore(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	b := store.BatchStore{Store: s}
	rec := llm.BatchRecord{ID: "msgbatch_1", Provider: "claude", Model: "claude-haiku-5-5", Stage: "light", Caller: "learning", DataClasses: []string{"review_comments"}, Requests: 3}
	if err := b.SaveBatch(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveBatch(ctx, rec); err != nil { // idempotent
		t.Fatal(err)
	}
	got, ok, err := b.LookupBatch(ctx, "msgbatch_1")
	if err != nil || !ok || got.Provider != "claude" || got.Requests != 3 || got.Repo != "" {
		t.Fatalf("lookup = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := b.LookupBatch(ctx, "msgbatch_missing"); ok || err != nil {
		t.Fatalf("missing batch: ok=%v err=%v", ok, err)
	}
	first, err := b.ClaimAccounting(ctx, "msgbatch_1")
	if err != nil || !first {
		t.Fatalf("first claim = %v, %v", first, err)
	}
	if again, _ := b.ClaimAccounting(ctx, "msgbatch_1"); again {
		t.Fatal("batch accounted twice")
	}
	id := "msgbatch_1"
	if err := (store.CallRecorder{Store: s}).RecordCall(ctx, llm.Call{BatchID: &id, Stage: "light", Provider: "claude", Model: "claude-haiku-5-5", DataClasses: []string{"review_comments"}}); err != nil {
		t.Fatalf("record batch call: %v", err)
	}
}
