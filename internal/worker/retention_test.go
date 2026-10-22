package worker

import (
	"context"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestRetention(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	days := func(n int) time.Time { return now.AddDate(0, 0, -n) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// gh_events: three expired payloads (more than one batch of 2), one fresh.
	for i, age := range []int{40, 35, 31, 5} {
		exec(`INSERT INTO gh_events (delivery_id, event, installation_id, payload, received_at)
			VALUES (gen_random_uuid(), 'pull_request', 1, $1, $2)`, `{"n": `+string(rune('0'+i))+`}`, days(age))
	}
	// model_calls: 100 days and 10 days old.
	for _, age := range []int{100, 10} {
		exec(`INSERT INTO model_calls (at, stage, provider, model, data_classes) VALUES ($1, 'generator', 'claude', 'm', '{}')`, days(age))
	}
	// A review eight years old (past both the 2-year findings and 7-year
	// decisions periods) with a decision and two findings, one labeled; and
	// a fresh review with the same.
	for i, age := range []int{8 * 365, 10} {
		var rid, run int64
		if err := st.Pool.QueryRow(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at, finished_at)
			VALUES ('acme/app', $1, 'h', 'b', 'v', 'production', 'done', $2, $2) RETURNING id`, i+1, days(age)).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		if err := st.Pool.QueryRow(ctx, `INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status)
			VALUES ($1, 'r', 'v', 'exemplar', '{a.go}', 'ok') RETURNING id`, rid).Scan(&run); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO approval_decisions (review_id, stage1, decision) VALUES ($1, '{}', 'ineligible')`, rid)
		for j := range 2 {
			var fid int64
			if err := st.Pool.QueryRow(ctx, `INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side,
				start_line, end_line, fingerprint, message, state, created_at)
				VALUES ($1, 'acme/app', $2, 'r', 'exemplar', 'important', 'issue', 'a.go', 'RIGHT', 1, 1, $3, 'm', 'posted', $4) RETURNING id`,
				run, i+1, string(rune('a'+j)), days(age)).Scan(&fid); err != nil {
				t.Fatal(err)
			}
			if j == 0 {
				exec(`INSERT INTO verifier_labels (finding_id, label, source) VALUES ($1, 'wrong', 'author_signal')`, fid)
				exec(`INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via) VALUES ($1, 'fb_wrong', 'dana', 'reviewer', 2, 'link')`, fid)
			}
		}
	}
	// evaluations: one past the findings period, one whose job never ran
	// for three days, and a fresh one waiting.
	for _, age := range []int{8 * 365, 3, 0} {
		exec(`INSERT INTO evaluations (repo, base_sha, requested_by, diff, created_at) VALUES ('acme/app', 'b', 'alice', 'diff --git ', $1)`, days(age))
	}

	snap := exampleSnapshot(t)
	w, err := NewRetentionWorker(st, func(context.Context) (*config.Snapshot, error) { return snap, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.now, w.batch = func() time.Time { return now }, 2
	res, err := w.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := RetentionResult{"gh_events.payload": 3, "model_calls": 1, "findings": 2, "approval_decisions": 1, "evaluations": 1, "evaluations.diff": 1}
	for k, v := range want {
		if res[k] != v {
			t.Errorf("%s: %d, want %d (all: %v)", k, res[k], v, res)
		}
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := st.Pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The event rows stay (idempotency); only payloads are gone.
	if n := count(`SELECT count(*) FROM gh_events`); n != 4 {
		t.Errorf("gh_events rows = %d", n)
	}
	if n := count(`SELECT count(*) FROM gh_events WHERE payload IS NOT NULL`); n != 1 {
		t.Errorf("payloads left = %d", n)
	}
	if n := count(`SELECT count(*) FROM verifier_labels`); n != 1 {
		t.Errorf("labels left = %d", n)
	}
	if n := count(`SELECT count(*) FROM feedback_events`); n != 1 {
		t.Errorf("feedback events left = %d", n)
	}
	if n := count(`SELECT count(*) FROM findings`) + count(`SELECT count(*) FROM approval_decisions`) + count(`SELECT count(*) FROM model_calls`); n != 2+1+1 {
		t.Errorf("rows left = %d", n)
	}
	if n := count(`SELECT count(*) FROM evaluations WHERE diff IS NOT NULL`); n != 1 {
		t.Errorf("evaluation diffs left = %d", n)
	}
	if n := count(`SELECT count(*) FROM evaluations WHERE status = 'failed'`); n != 1 {
		t.Errorf("stale evaluations marked failed = %d", n)
	}
	// Running again finds nothing.
	if res, err := w.Run(ctx); err != nil || res["findings"]+res["model_calls"]+res["gh_events.payload"]+res["approval_decisions"] != 0 {
		t.Errorf("second run = %v, %v", res, err)
	}
	// A missing period is never read as "delete everything".
	snap.Retention.FindingsDays = 0
	if _, err := w.Run(ctx); err == nil {
		t.Error("a zero period was accepted")
	}
	if n := count(`SELECT count(*) FROM findings`); n != 2 {
		t.Errorf("findings after a zero period = %d", n)
	}
}
