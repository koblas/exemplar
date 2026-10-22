package calibrate

import (
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

func TestMain(m *testing.M) {
	os.Exit(synth.RunCached(func() int { return storetest.Run(m) }))
}

func TestStoredLabels(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO reviews (id, repo, pr_number, head_sha, base_sha, config_version, caller, status)
	      VALUES (1, 'acme/app', 7, 'h1', 'b1', 'v', 'production', 'done')`)
	exec(`INSERT INTO rule_runs (id, review_id, rule_id, rule_version, lane, scoped_files, status)
	      VALUES (1, 1, 'propagate-context', 'x', 'exemplar', '{handler.go}', 'ok')`)
	for i, label := range []string{"wrong", "true", "unimportant"} {
		exec(`INSERT INTO findings (id, rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side, start_line, end_line, fingerprint, message, state)
		      VALUES ($1, 1, 'acme/app', 7, 'propagate-context', 'exemplar', 'important', 'issue (reliability)', 'handler.go', 'RIGHT', 9, 9, $2, 'ctx ignored', 'posted')`,
			i+1, "fp"+label)
		exec(`INSERT INTO verifier_labels (finding_id, label, source) VALUES ($1, $2, 'author_signal')`, i+1, label)
	}
	labels, err := StoredLabels(ctx, st, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 || labels[0].Label != "wrong" || labels[1].Label != "true" {
		t.Fatalf("labels = %+v", labels)
	}
	l := labels[0]
	if l.Fixture.Repo != "acme/app" || l.Fixture.BaseSHA != "b1" || l.Fixture.HeadSHA != "h1" || l.Candidate.StartLine != 9 || l.Candidate.Claim != "ctx ignored" || l.Source != "verifier_labels" {
		t.Errorf("label = %+v", l)
	}
}
