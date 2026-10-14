package worker

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/store/storetest"
)

// ev is a feedback event, minutes after the finding was posted.
type ev struct {
	kind, actor, role string
	weight            float32
	at                int
}

func TestFeedbackOutcomes(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	posted := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	runs := map[string]int64{}
	run := func(caller string) int64 {
		if id, ok := runs[caller]; ok {
			return id
		}
		var rid, id int64
		if err := st.Pool.QueryRow(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at, finished_at, cost_usd)
			VALUES ('acme/app', 1, 'h', 'b', 'v', $1, 'done', $2, $2, 0.5) RETURNING id`, caller, posted).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		if err := st.Pool.QueryRow(ctx, `INSERT INTO rule_runs (review_id, rule_id, rule_version, lane, scoped_files, status)
			VALUES ($1, 'r', 'v', 'exemplar', '{a.go}', 'ok') RETURNING id`, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		runs[caller] = id
		return id
	}
	n := 0
	finding := func(caller, state, reason string, events ...ev) int64 {
		n++
		var id int64
		var sr *string
		if reason != "" {
			sr = &reason
		}
		if err := st.Pool.QueryRow(ctx, `INSERT INTO findings (rule_run_id, repo, pr_number, rule_id, lane, severity, cc_label, path, side,
			start_line, end_line, fingerprint, message, state, suppressed_reason, created_at)
			VALUES ($1, 'acme/app', 1, 'r', 'exemplar', 'important', 'issue', 'a.go', 'RIGHT', $2, $2, $3, 'm', $4, $5, $6) RETURNING id`,
			run(caller), n, string(rune('a'+n)), state, sr, posted).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if _, err := st.Pool.Exec(ctx, `INSERT INTO feedback_events (finding_id, kind, actor, role, weight, via, observed_at)
				VALUES ($1, $2, $3, $4, $5, 'slash', $6)`, id, e.kind, e.actor, e.role, e.weight, posted.Add(time.Duration(e.at)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	type want struct {
		seen, accepted, rejected, humanAgreed, overturned bool
		neg, pos                                          float32
		nonAuthorNeg                                      int64
	}
	tests := []struct {
		name string
		id   int64
		want *want // nil: not in finding_outcomes
	}{
		{"the author's latest signal wins", finding("production", "posted", "",
			ev{"fb_wrong", "alice", "author", 1, 1}, ev{"fb_fixed", "alice", "author", 1, 5}),
			&want{seen: true, accepted: true, pos: 1}},
		{"different actors both count", finding("production", "posted", "",
			ev{"fb_wrong", "rev", "reviewer", 2, 1}, ev{"fb_fixed", "alice", "author", 1, 2}),
			&want{seen: true, accepted: true, rejected: true, neg: 2, pos: 1, nonAuthorNeg: 1}},
		{"observers count for nothing", finding("production", "posted", "",
			ev{"fb_wrong", "zed", "observer", 0, 1}),
			&want{seen: true}},
		{"an uphold decides", finding("production", "posted", "",
			ev{"fb_wrong", "alice", "author", 1, 1}, ev{"adjudicated_uphold", "olga", "owner", 4, 9}),
			&want{seen: true, accepted: true, pos: 4}},
		{"the latest adjudication decides", finding("production", "posted", "",
			ev{"fb_fixed", "alice", "author", 1, 1}, ev{"adjudicated_uphold", "olga", "owner", 4, 2}, ev{"adjudicated_overturn", "olga", "owner", 4, 3}),
			&want{seen: true, rejected: true, overturned: true, neg: 4}},
		{"a reviewer's agreement on a withheld duplicate", finding("production", "dropped", "human_duplicate",
			ev{"human_agreed", "dana", "reviewer", 2, 0}),
			&want{humanAgreed: true, pos: 2}},
		{"an applied suggestion on an outdated finding", finding("production", "outdated", "",
			ev{"applied_suggestion", "alice", "author", 1, 30}),
			&want{seen: true, accepted: true, pos: 1}},
		{"thread resolution is kept beside a later judgment", finding("production", "resolved", "",
			ev{"resolved", "alice", "author", 1, 1}, ev{"fb_not_important", "alice", "author", 1, 2}),
			&want{seen: true, rejected: true, neg: 1}},
		{"a late linter duplicate is left out", finding("production", "minimized", "linter_duplicate", ev{"fb_wrong", "alice", "author", 1, 1}), nil},
		{"an eval run is left out", finding("eval", "posted", ""), nil},
		{"a shadow finding is left out", finding("production", "shadow", ""), nil},
	}
	for _, tt := range tests {
		o, err := st.FindingOutcome(ctx, tt.id)
		if tt.want == nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("%s: in finding_outcomes (%+v, %v)", tt.name, o, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		got := want{seen: o.Seen, accepted: o.Accepted, rejected: o.Rejected, humanAgreed: o.HumanAgreed, overturned: o.OwnerOverturned,
			neg: o.NegW, pos: o.PosW, nonAuthorNeg: o.NonAuthorNegatives}
		if got != *tt.want {
			t.Errorf("%s = %+v, want %+v", tt.name, got, *tt.want)
		}
	}

	sw, err := NewStatsWorker(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var posts, accepted, rejected, humanAgreed, overturns int64
	var acceptance, efp float64
	if err := st.Pool.QueryRow(ctx, `SELECT posted, accepted, rejected, human_agreed, owner_overturns, acceptance_rate::float8, effective_fp_weighted::float8
		FROM rule_weekly_stats WHERE rule_id = 'r'`).Scan(&posts, &accepted, &rejected, &humanAgreed, &overturns, &acceptance, &efp); err != nil {
		t.Fatal(err)
	}
	// Seen: 7 findings (not the withheld duplicate, 6); accepted 1, 2, 4,
	// 7; rejected 2, 5, 8. Acceptance is
	// accepted over those with any outcome (1, 2, 4, 5, 7, 8) = 4/6. eFP is
	// negative weight (2 + 4 + 1) over all weight (7 + 1+1+4+2+1) = 7/16.
	if posts != 7 || accepted != 4 || rejected != 3 || humanAgreed != 1 || overturns != 1 || acceptance != 0.667 || efp != 0.438 {
		t.Errorf("rule_weekly_stats = posted %d accepted %d rejected %d human %d overturns %d acceptance %v eFP %v",
			posts, accepted, rejected, humanAgreed, overturns, acceptance, efp)
	}
	var reviews int64
	if err := st.Pool.QueryRow(ctx, `SELECT sum(reviews) FROM review_daily_stats`).Scan(&reviews); err != nil || reviews != 2 {
		t.Errorf("review_daily_stats reviews = %d, %v", reviews, err)
	}
}
