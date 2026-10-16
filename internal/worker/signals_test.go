package worker

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestRouteSignals(t *testing.T) {
	merged := `{"action": "closed", "number": 7, "installation": {"id": 42}, "repository": {"full_name": "acme/app"},
		"pull_request": {"state": "closed", "merged": true, "merged_at": "2026-10-14T09:00:00Z", "user": {"login": "alice"}, "head": {"sha": "abc"}}}`
	r, err := RouteEvent("pull_request", []byte(merged))
	if err != nil || r.Supersede == nil || len(r.Signals) != 1 || r.Signals[0].Signal != store.SignalMerged || r.Signals[0].Actor != "alice" ||
		r.Signals[0].HeadSHA != "abc" || !r.Signals[0].At.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("merged = %+v, %v", r, err)
	}
	if r, _ := RouteEvent("pull_request", []byte(strings.Replace(merged, `"merged": true`, `"merged": false`, 1))); len(r.Signals) != 0 {
		t.Errorf("closed unmerged = %+v", r.Signals)
	}
	thread := func(action, sender string) string {
		return fmt.Sprintf(`{"action": %q, "installation": {"id": 42}, "repository": {"full_name": "acme/app"}, "pull_request": {"number": 7},
			"thread": {"comments": [{"id": 1000}, {"id": 1001, "in_reply_to_id": 1000}]}, "sender": {"login": "rev", "type": %q}}`, action, sender)
	}
	if r, err := RouteEvent("pull_request_review_thread", []byte(thread("resolved", "User"))); err != nil || len(r.Signals) != 1 ||
		r.Signals[0] != (store.FeedbackSignalArgs{InstallationID: 42, Repo: "acme/app", PR: 7, Signal: store.SignalResolved, ThreadRoot: 1000, Actor: "rev"}) {
		t.Errorf("resolved = %+v, %v", r, err)
	}
	for name, p := range map[string]string{"unresolved": thread("unresolved", "User"), "by Exemplar": thread("resolved", "Bot")} {
		if r, _ := RouteEvent("pull_request_review_thread", []byte(p)); len(r.Signals) != 0 || r.Ignored == "" {
			t.Errorf("%s = %+v", name, r)
		}
	}
	comment := func(reply int, body string) string {
		return fmt.Sprintf(`{"action": "created", "installation": {"id": 42}, "repository": {"full_name": "acme/app"}, "pull_request": {"number": 7},
			"comment": {"id": 55, "in_reply_to_id": %d, "body": %q, "created_at": "2026-10-14T10:00:00Z", "author_association": "MEMBER",
			"path": "a.go", "side": "RIGHT", "start_line": 3, "line": 5, "user": {"login": "dana", "type": "User"}}}`, reply, body)
	}
	if r, _ := RouteEvent("pull_request_review_comment", []byte(comment(1000, "will fix"))); len(r.Signals) != 1 || r.Signals[0].Signal != store.SignalReply ||
		r.Signals[0].ThreadRoot != 1000 || r.Signals[0].Body != "will fix" {
		t.Errorf("reply = %+v", r.Signals)
	}
	if r, _ := RouteEvent("pull_request_review_comment", []byte(comment(0, "this leaks"))); len(r.Signals) != 1 || r.Signals[0].Signal != store.SignalComment ||
		r.Signals[0].Path != "a.go" || r.Signals[0].Start != 3 || r.Signals[0].End != 5 || r.Signals[0].Association != "MEMBER" {
		t.Errorf("comment = %+v", r.Signals)
	}
}

func TestFeedbackSignals(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("review = %+v, %v", out, err)
	}
	root := int64(e.fake.commentIDs()[0])
	at := time.Now().Add(time.Minute)
	e.fake.issueComments = fmt.Sprintf(`[{"id": 7, "user": {"login": "rev", "type": "User"}, "created_at": %q, "body": "LGTM?"}]`, time.Now().Add(-time.Hour).Format(time.RFC3339))
	sw, err := NewFeedbackSignalWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	sig := func(signal, actor, assoc, body string, id int64) store.FeedbackSignalArgs {
		a := store.FeedbackSignalArgs{InstallationID: inst, Repo: repo, PR: prNum, Signal: signal, CommentID: id, Actor: actor, Association: assoc, At: at, Body: body}
		if signal == store.SignalComment {
			a.Path, a.Side, a.Start, a.End = "main.go", "RIGHT", 8, 10
		} else {
			a.ThreadRoot = root
		}
		return a
	}
	tests := []struct {
		name    string
		args    store.FeedbackSignalArgs
		kinds   []string
		role    string
		ignored bool
	}{
		{name: "the author agrees", args: sig(store.SignalReply, "alice", "", "Good catch, will fix.", 1), kinds: []string{"reply_agree"}, role: "author"},
		{name: "a reviewer asks", args: sig(store.SignalReply, "rev", "", "Which context?", 2), kinds: []string{"reply_question"}, role: "reviewer"},
		{name: "a stranger disagrees", args: sig(store.SignalReply, "zed", "", "This is wrong.", 3), kinds: []string{"reply_disagree"}, role: "observer"},
		{name: "an unrelated reply", args: sig(store.SignalReply, "alice", "", "cc @rev", 4), ignored: true},
		{name: "a reviewer resolves", args: sig(store.SignalResolved, "rev", "", "", 0), kinds: []string{"resolved"}, role: "reviewer"},
		{name: "a member raises it again", args: sig(store.SignalComment, "dana", "MEMBER", "same-concern: pass ctx here", 5), kinds: []string{"human_agreed"}, role: "reviewer"},
		{name: "a member raises something else", args: sig(store.SignalComment, "dana", "MEMBER", "rename this", 6), ignored: true},
		{name: "an outside contributor", args: sig(store.SignalComment, "zed", "CONTRIBUTOR", "same-concern", 7), ignored: true},
		{name: "the author raises it", args: sig(store.SignalComment, "alice", "OWNER", "same-concern", 8), ignored: true},
		{name: "another thread", args: func() store.FeedbackSignalArgs {
			a := sig(store.SignalResolved, "rev", "", "", 0)
			a.ThreadRoot = 9
			return a
		}(), ignored: true},
		// The finding has feedback, so merging records no ignored.
		{name: "merged after feedback", args: store.FeedbackSignalArgs{InstallationID: inst, Repo: repo, PR: prNum, Signal: store.SignalMerged, Actor: "alice"}, ignored: true},
	}
	for _, tt := range tests {
		out, err := sw.Handle(t.Context(), tt.args)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if !slices.Equal(out.Kinds, tt.kinds) || (tt.role != "" && out.Role != tt.role) || (out.Ignored != "") != tt.ignored {
			t.Errorf("%s = %+v", tt.name, out)
		}
	}
	var weight float32
	if err := st.Pool.QueryRow(t.Context(), `SELECT weight FROM feedback_events WHERE actor = 'zed'`).Scan(&weight); err != nil || weight != 0 {
		t.Errorf("observer weight = %v, %v", weight, err)
	}
}

// Findings untouched at merge were ignored.
func TestIgnoredAtMerge(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("review = %+v, %v", out, err)
	}
	sw, err := NewFeedbackSignalWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	merge := store.FeedbackSignalArgs{
		InstallationID: inst, Repo: repo, PR: prNum, Signal: store.SignalMerged, Actor: "alice",
		HeadSHA: e.head, At: time.Now().Add(-time.Hour),
	}
	if out, err := sw.Handle(t.Context(), merge); err != nil || !slices.Equal(out.Kinds, []string{"ignored"}) {
		t.Fatalf("merged = %+v, %v", out, err)
	}
	// The merge is recorded for counter-metrics (spec §12.5), and a later
	// revert naming the PR marks it failed.
	var reviewed, auto, reverted bool
	var posted int
	q := `SELECT reviewed, auto_approved, findings_posted, reverted FROM merge_outcomes WHERE repo = $1 AND pr_number = $2`
	if err := st.Pool.QueryRow(t.Context(), q, repo, prNum).Scan(&reviewed, &auto, &posted, &reverted); err != nil ||
		!reviewed || auto || posted != 1 || reverted {
		t.Errorf("merge outcome = %v %v %d %v, %v", reviewed, auto, posted, reverted, err)
	}
	if _, err := st.Pool.Exec(t.Context(), `INSERT INTO pr_links (repo, pr_number, kind, ref, at) VALUES ($1, $2, 'revert', '#90', now())`, repo, prNum); err != nil {
		t.Fatal(err)
	}
	cm, err := st.CounterMetrics(t.Context(), 30)
	if err != nil || len(cm) != 1 || cm[0].AutoApproved || cm[0].Merged != 1 || cm[0].Reverted != 1 || cm[0].Failed != 1 || cm[0].Incident != 0 {
		t.Errorf("counter metrics = %+v, %v", cm, err)
	}
	var ignored bool
	if err := st.Pool.QueryRow(t.Context(), `SELECT ignored FROM finding_outcomes`).Scan(&ignored); err != nil || !ignored {
		t.Errorf("outcome ignored = %v, %v", ignored, err)
	}
	// A redelivery records nothing more.
	if out, err := sw.Handle(t.Context(), merge); err != nil || out.Ignored == "" {
		t.Errorf("again = %+v, %v", out, err)
	}
}

func TestReactions(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("review = %+v, %v", out, err)
	}
	// The poller finds the installation from the repo's deliveries.
	if _, err := st.Pool.Exec(t.Context(), `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, inst, repo); err != nil {
		t.Fatal(err)
	}
	e.fake.issueComments = fmt.Sprintf(`[{"id": 7, "user": {"login": "rev", "type": "User"}, "created_at": %q, "body": "LGTM?"}]`, time.Now().Add(-time.Hour).Format(time.RFC3339))
	e.fake.reactionsOn = []string{"alice:User:THUMBS_DOWN", "exemplar-app:Bot:THUMBS_UP", "rev:User:THUMBS_UP", "zed:User:THUMBS_DOWN", "alice:User:HEART"}
	rw, err := NewReactionsWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	prs, events, err := rw.Run(t.Context())
	if err != nil || prs != 1 || events != 3 {
		t.Fatalf("run = %d PRs, %d events, %v", prs, events, err)
	}
	rows, err := st.Pool.Query(t.Context(), `SELECT kind || ' ' || actor || ' ' || role || ' ' || weight || ' ' || via FROM feedback_events ORDER BY actor`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	want := []string{"react_down alice author 0.5 reaction", "react_up rev reviewer 0.5 reaction", "react_down zed observer 0 reaction"}
	if !slices.Equal(got, want) {
		t.Errorf("events = %q", got)
	}
	// A 👎 rejects for acceptance but stays out of eFP.
	var rejected bool
	var neg float32
	if err := st.Pool.QueryRow(t.Context(), `SELECT rejected, neg_w FROM finding_outcomes`).Scan(&rejected, &neg); err != nil || !rejected || neg != 0 {
		t.Errorf("outcome rejected %v, neg_w %v, %v", rejected, neg, err)
	}
	if _, events, err := rw.Run(t.Context()); err != nil || events != 0 {
		t.Errorf("second run = %d events, %v", events, err)
	}
}
