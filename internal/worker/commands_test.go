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

func TestParseCommand(t *testing.T) {
	tests := []struct {
		body, cmd, reason string
		ok                bool
	}{
		{"/exemplar wrong", "wrong", "", true},
		{"  /Exemplar WRONG it's a test helper  ", "wrong", "it's a test helper", true},
		{"/exemplar not-important\r\nThis path is generated.", "not-important", "This path is generated.", true},
		{"/exemplar", "", "", true},
		{"thanks! /exemplar fixed", "", "", false},
		{"looks good", "", "", false},
	}
	for _, tt := range tests {
		cmd, reason, ok := ParseCommand(tt.body)
		if cmd != tt.cmd || reason != tt.reason || ok != tt.ok {
			t.Errorf("ParseCommand(%q) = %q, %q, %v", tt.body, cmd, reason, ok)
		}
	}
	if _, reason, _ := ParseCommand("/exemplar wrong " + strings.Repeat("x", 2000)); len(reason) != maxReasonChars {
		t.Errorf("reason not capped: %d", len(reason))
	}
}

func TestRouteFeedbackCommand(t *testing.T) {
	payload := func(action, body string, replyTo int, userType string) string {
		return fmt.Sprintf(`{"action": %q, "installation": {"id": 42}, "repository": {"full_name": "acme/app"}, "pull_request": {"number": 7},
			"comment": {"id": 55, "in_reply_to_id": %d, "body": %q, "created_at": "2026-10-14T10:00:00Z", "user": {"login": "dana", "type": %q}}}`,
			action, replyTo, body, userType)
	}
	r, err := RouteEvent("pull_request_review_comment", []byte(payload("created", "/exemplar wrong not a bug", 1000, "User")))
	want := store.FeedbackCommandArgs{InstallationID: 42, Repo: "acme/app", PR: 7, CommentID: 55, ThreadRoot: 1000, Actor: "dana", ActorType: "User",
		At: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC), Command: "wrong", Reason: "not a bug"}
	if err != nil || len(r.FeedbackCommands) != 1 || r.FeedbackCommands[0] != want {
		t.Fatalf("route = %+v, %v", r, err)
	}
	for name, p := range map[string]string{
		"edited":     payload("edited", "/exemplar wrong", 1000, "User"),
		"top-level":  payload("created", "/exemplar wrong", 0, "User"),
		"from a bot": payload("created", "/exemplar wrong", 1000, "Bot"),
	} {
		if r, err := RouteEvent("pull_request_review_comment", []byte(p)); err != nil || len(r.FeedbackCommands)+len(r.Signals) != 0 || r.Ignored == "" {
			t.Errorf("%s = %+v, %v", name, r, err)
		}
	}
}

func TestFeedbackCommands(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("review = %+v, %v", out, err)
	}
	root := int64(e.fake.commentIDs()[0])
	at := time.Now().Add(time.Minute)
	e.fake.teamMembers = map[string]bool{"olga": true}
	e.fake.issueComments = fmt.Sprintf(`[{"id": 7, "user": {"login": "rev", "type": "User"}, "created_at": %q, "body": "LGTM?"}]`, time.Now().Add(-time.Hour).Format(time.RFC3339))
	cw, err := NewFeedbackCommandWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cmd := func(id int64, actor, command, reason string) store.FeedbackCommandArgs {
		return store.FeedbackCommandArgs{InstallationID: inst, Repo: repo, PR: prNum, CommentID: id, ThreadRoot: root,
			Actor: actor, ActorType: "User", At: at, Command: command, Reason: reason}
	}
	tests := []struct {
		name             string
		args             store.FeedbackCommandArgs
		recorded, role   string
		replied, ignored bool
	}{
		{name: "the author", args: cmd(1, "alice", "wrong", "this is generated code"), recorded: "fb_wrong", role: "author"},
		{name: "a reviewer", args: cmd(2, "rev", "fixed", ""), recorded: "fb_fixed", role: "reviewer"},
		{name: "an owner", args: cmd(3, "olga", "disagree", "we allow this here"), recorded: "fb_disagree", role: "owner"},
		{name: "an owner adjudicates", args: cmd(4, "olga", "overturn", ""), recorded: "adjudicated_overturn", role: "owner"},
		{name: "someone else", args: cmd(5, "zed", "wrong", ""), replied: true},
		{name: "the author cannot adjudicate", args: cmd(6, "alice", "uphold", ""), replied: true},
		{name: "a reviewer cannot adjudicate", args: cmd(7, "rev", "uphold", ""), replied: true},
		{name: "an unknown command", args: cmd(8, "alice", "frobnicate", ""), replied: true},
		{name: "not an Exemplar thread", args: func() store.FeedbackCommandArgs { a := cmd(9, "alice", "wrong", ""); a.ThreadRoot = 999; return a }(), ignored: true},
		{name: "a repeat", args: cmd(10, "alice", "wrong", "again"), recorded: "fb_wrong", role: "author"},
	}
	for _, tt := range tests {
		replies := len(e.fake.replies)
		out, err := cw.Handle(t.Context(), tt.args)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if out.Recorded != tt.recorded || out.Role != tt.role || (out.Replied != "") != tt.replied || (out.Ignored != "") != tt.ignored {
			t.Errorf("%s = %+v", tt.name, out)
		}
		if got := len(e.fake.replies) - replies; got != map[bool]int{true: 1, false: 0}[tt.replied] {
			t.Errorf("%s: %d replies", tt.name, got)
		}
		if tt.recorded != "" && !slices.Contains(e.fake.reactions, fmt.Sprintf("%d:+1", tt.args.CommentID)) {
			t.Errorf("%s: no reaction, reactions %v", tt.name, e.fake.reactions)
		}
	}
	rows, err := st.Pool.Query(t.Context(), `SELECT kind, actor, role, weight, via, coalesce(reason_text, '') FROM feedback_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var kind, actor, role, via, reason string
		var weight float32
		if err := rows.Scan(&kind, &actor, &role, &weight, &via, &reason); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %s %v %s %q", kind, actor, role, weight, via, reason))
	}
	want := []string{
		`fb_wrong alice author 1 slash "this is generated code"`,
		`fb_fixed rev reviewer 2 slash ""`,
		`fb_disagree olga owner 4 slash "we allow this here"`,
		`adjudicated_overturn olga owner 4 slash ""`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Teach-the-bot classes the reasons of rejections only.
	var classes []string
	crows, err := st.Pool.Query(t.Context(), `SELECT kind || ':' || coalesce(teach_class, '-') FROM feedback_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for crows.Next() {
		var s string
		_ = crows.Scan(&s)
		classes = append(classes, s)
	}
	if want := []string{"fb_wrong:scope", "fb_fixed:-", "fb_disagree:noise", "adjudicated_overturn:-"}; !slices.Equal(classes, want) {
		t.Errorf("teach classes = %q, want %q", classes, want)
	}
	// The author's wrong became a verifier label; the owner's overturn
	// replaced it as a human review.
	var label, source string
	if err := st.Pool.QueryRow(t.Context(), `SELECT label, source FROM verifier_labels`).Scan(&label, &source); err != nil || label != "wrong" || source != "human_review" {
		t.Errorf("verifier label = %s %s, %v", label, source, err)
	}
}
