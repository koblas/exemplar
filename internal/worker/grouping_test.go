package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// TestReviewGroupsClass follows a rule that matches three places, over a
// class threshold of two: one grouped comment, three findings sharing it;
// a command on the thread applies to all three; the thread resolves only once the last is fixed.
func TestReviewGroupsClass(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap := exampleSnapshot(t)
	snap.Global.Posting = &schema.Posting{ClassThreshold: 2}
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	e.also.Store(&[]int32{10, 13})
	review := func() Outcome {
		t.Helper()
		out, err := e.w.Review(t.Context(), args(e))
		if err != nil || out.Status != StatusReviewed {
			t.Fatalf("review = %+v, %v", out, err)
		}
		return out
	}

	review()
	comments, _ := e.fake.reviews[0]["comments"].([]any)
	if len(comments) != 1 || !strings.Contains(fmt.Sprint(comments[0]), "Also in 2 other place(s)") {
		t.Fatalf("comments = %v", comments)
	}
	var groups, ids int
	if err := st.Pool.QueryRow(t.Context(), `SELECT count(DISTINCT group_key), count(DISTINCT gh_comment_id) FROM findings
		WHERE rule_id = 'propagate-context' AND state = 'posted'`).Scan(&groups, &ids); err != nil {
		t.Fatal(err)
	}
	if got := e.state(t); len(got) != 3 || groups != 1 || ids != 1 || got[2] != "posted:1000:" {
		t.Fatalf("findings = %v, groups %d, comment ids %d", got, groups, ids)
	}

	// A command on the thread records an event for every occurrence.
	cw, err := NewFeedbackCommandWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.Handle(t.Context(), store.FeedbackCommandArgs{
		InstallationID: inst, Repo: repo, PR: prNum, CommentID: 1, ThreadRoot: 1000,
		Actor: "alice", ActorType: "User", At: time.Now().Add(time.Minute), Command: "disagree", Reason: "intended",
	}); err != nil {
		t.Fatal(err)
	}
	var events, findings int
	if err := st.Pool.QueryRow(t.Context(), `SELECT count(*), count(DISTINCT finding_id) FROM feedback_events WHERE kind = 'fb_disagree'`).Scan(&events, &findings); err != nil {
		t.Fatal(err)
	}
	if events != 3 || findings != 3 {
		t.Errorf("thread command recorded %d events on %d findings", events, findings)
	}

	// One place is fixed: its finding closes, the thread stays open.
	e.push(t, map[string]string{"main.go": headMain + "\n// touched\n"})
	e.also.Store(&[]int32{10})
	if out := review(); out.Closed != 1 {
		t.Fatalf("partial fix = %+v", out)
	}
	if len(e.fake.replies) != 0 || e.fake.resolved["T1000"] {
		t.Errorf("thread closed early: replies %v, resolved %v", e.fake.replies, e.fake.resolved)
	}

	// The rest are fixed: one reply, and the thread resolves.
	e.push(t, map[string]string{"main.go": headMain + "\n// fixed\n"})
	e.report.Store(false)
	if out := review(); out.Closed != 2 {
		t.Fatalf("full fix = %+v", out)
	}
	if len(e.fake.replies) != 1 || !e.fake.resolved["T1000"] {
		t.Errorf("replies %v, resolved %v", e.fake.replies, e.fake.resolved)
	}
}
