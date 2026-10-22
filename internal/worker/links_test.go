package worker

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestFeedbackLinks(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	links, err := feedbacklink.New([]byte(strings.Repeat("l", 32)), "https://exemplar.example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	e.w.cfg.Links = links
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("review = %+v, %v", out, err)
	}
	var id int64
	var comment *int64
	if err := st.Pool.QueryRow(t.Context(), `SELECT id, gh_comment_id FROM findings WHERE rule_id = 'propagate-context'`).Scan(&id, &comment); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprint(e.fake.reviews[0]["comments"])
	if !strings.Contains(body, links.URL(id, "wrong")) || comment == nil {
		t.Fatalf("the comment does not carry the finding's signed links, or was not mapped (%v):\n%s", comment, body)
	}
	if _, err := st.Pool.Exec(t.Context(), `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, inst, repo); err != nil {
		t.Fatal(err)
	}
	e.fake.issueComments = fmt.Sprintf(`[{"id": 7, "user": {"login": "rev", "type": "User"}, "created_at": %q, "body": "hm"}]`, time.Now().Add(-time.Hour).Format(time.RFC3339))
	lw, err := NewFeedbackLinkWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	click := func(actor, reason, text string) SignalOutcome {
		t.Helper()
		out, err := lw.Handle(t.Context(), store.FeedbackLinkArgs{FindingID: id, Reason: reason, Actor: actor, At: time.Now(), Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if o := click("alice", "not_important", "this is generated code"); o.Role != "author" || o.Kinds[0] != "fb_not_important" {
		t.Errorf("author = %+v", o)
	}
	if o := click("rev", "wrong", ""); o.Role != "reviewer" {
		t.Errorf("participant = %+v", o)
	}
	// A click alone never makes someone a reviewer.
	if o := click("zed", "fixed", ""); o.Role != "observer" {
		t.Errorf("stranger = %+v", o)
	}
	var class string
	if err := st.Pool.QueryRow(t.Context(), `SELECT teach_class FROM feedback_events WHERE actor = 'alice'`).Scan(&class); err != nil || class != "scope" {
		t.Errorf("teach class = %q, %v", class, err)
	}
	var via string
	if err := st.Pool.QueryRow(t.Context(), `SELECT string_agg(DISTINCT via, ',') FROM feedback_events`).Scan(&via); err != nil || via != "link" {
		t.Errorf("via = %q, %v", via, err)
	}
	// A finding retention removed: nothing to record, no retry.
	if out, err := lw.Handle(t.Context(), store.FeedbackLinkArgs{FindingID: id + 1000, Reason: "wrong", Actor: "alice", At: time.Now()}); err != nil || out.Ignored == "" {
		t.Errorf("gone = %+v, %v", out, err)
	}
}
