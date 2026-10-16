package mergesafety

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// fakeGitHub answers the force-push and author-tenure queries.
type fakeGitHub struct {
	forcePushes int
	veterans    map[string]bool // logins with a PR merged before the cutoff
	searches    []string
	fail        bool
}

func (f *fakeGitHub) query(_ context.Context, q string, vars map[string]any, out any) error {
	if f.fail {
		return errors.New("github down")
	}
	var data any
	switch q {
	case forcePushQuery:
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"timelineItems": map[string]any{"totalCount": f.forcePushes}}}}
	case authorQuery:
		s, ok := vars["q"].(string)
		if !ok {
			return fmt.Errorf("q = %v, want a string", vars["q"])
		}
		f.searches = append(f.searches, s)
		n := 0
		for login := range f.veterans {
			if strings.Contains(s, "author:"+login+" ") {
				n = 3
			}
		}
		data = map[string]any{"search": map[string]any{"issueCount": n}}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func TestCollect(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	exec(`INSERT INTO path_events (repo, path, kind, ref, at) VALUES
		('acme/app', 'svc/pay.go', 'incident', 'INC-1', $1),
		('acme/app', 'svc/cart.go', 'revert', '#9', $2),
		('acme/app', 'svc/old.go', 'incident', 'INC-0', $3),
		('acme/other', 'svc/cart.go', 'incident', 'INC-2', $1)`,
		now.Add(-10*day), now.Add(-100*day), now.Add(-200*day))
	// svc/cart.go: 11 other merged PRs in 30 days (churn), plus this PR and an old one.
	for i := 1; i <= 11; i++ {
		exec(`INSERT INTO mined_prs (repo, pr_number, head_sha, base_sha, merged_at, approved_by_codeowner, files)
			VALUES ('acme/app', $1, 'a', 'b', $2, false, ARRAY['svc/cart.go'])`, i, now.Add(-time.Duration(i)*day))
	}
	exec(`INSERT INTO mined_prs VALUES ('acme/app', 50, 'a', 'b', $1, false, ARRAY['svc/cart.go'])`, now.Add(-day))
	exec(`INSERT INTO mined_prs VALUES ('acme/app', 60, 'a', 'b', $1, false, ARRAY['svc/cart.go'])`, now.Add(-40*day))

	f := &fakeGitHub{forcePushes: 2, veterans: map[string]bool{"alice": true}}
	c, err := New(st, f.query, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	pr := PR{Repo: "acme/app", Number: 50, Author: "bob", Paths: []string{"svc/pay.go", "svc/cart.go", "svc/old.go", "db/migrations/7.sql"}}
	in, err := c.Collect(ctx, pr, []string{"db/migrations/**"})
	if err != nil {
		t.Fatal(err)
	}
	want := []gates.PathFacts{
		{Path: "svc/pay.go", Incident: true, Recent: true}, // 10 days: inside the bug lane's 90
		{Path: "svc/cart.go", Revert: true, MergedPRs: 11}, // revert at 100 days; this PR and the 40-day-old one do not count
		{Path: "svc/old.go"}, // incident outside 180 days
		{Path: "db/migrations/7.sql", Sensitive: true},
	}
	for i, p := range want {
		if in.Paths[i] != p {
			t.Errorf("path %d = %+v, want %+v", i, in.Paths[i], p)
		}
	}
	if !in.NoTests || !in.NewAuthor || !in.ForcePush {
		t.Errorf("PR facts = %+v", in)
	}
	if len(f.searches) != 1 || !strings.Contains(f.searches[0], "repo:acme/app is:pr is:merged author:bob merged:<2026-09-13") {
		t.Errorf("searches = %v", f.searches)
	}

	w, _ := gates.Weights(nil)
	res, err := c.Score(ctx, PR{Repo: "acme/app", Number: 50, Author: "alice", Paths: pr.Paths}, []string{"db/migrations/**"}, w)
	if err != nil {
		t.Fatal(err)
	}
	// Worst path svc/cart.go (0.20 + 0.10) ties pay.go (0.30); first in diff order wins.
	// Plus no_tests 0.15 and force_push 0.05; alice is not new.
	if res.Score != 0.5 || res.WorstPath != "svc/pay.go" {
		t.Errorf("score = %+v", res)
	}
}

func TestCollectFailsClosed(t *testing.T) {
	st := storetest.New(t)
	c, _ := New(st, (&fakeGitHub{fail: true}).query, func() time.Time { return now })
	if _, err := c.Collect(t.Context(), PR{Repo: "acme/app", Number: 1, Author: "bob", Paths: []string{"a.go"}}, nil); err == nil {
		t.Fatal("Collect returned facts it could not measure")
	}
}

func TestOddLoginCountsAsNew(t *testing.T) {
	st := storetest.New(t)
	f := &fakeGitHub{veterans: map[string]bool{"alice": true}}
	c, _ := New(st, f.query, func() time.Time { return now })
	in, err := c.Collect(t.Context(), PR{Repo: "acme/app", Number: 1, Author: "alice repo:other/x", Paths: []string{"a.go"}}, nil)
	if err != nil || !in.NewAuthor || len(f.searches) != 0 {
		t.Fatalf("in = %+v, err = %v, searches = %v", in, err, f.searches)
	}
}
