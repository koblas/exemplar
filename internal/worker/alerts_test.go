package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestRouteAlertDismissal(t *testing.T) {
	payload := func(action, tool, ref string) string {
		return fmt.Sprintf(`{"action": %q, "installation": {"id": 42}, "repository": {"full_name": "acme/app"},
			"alert": {"number": 3, "dismissed_reason": "false positive", "dismissed_comment": "test fixture", "dismissed_by": {"login": "sec"},
			"rule": {"id": "possible-secret-in-diff"}, "tool": {"name": %q},
			"most_recent_instance": {"ref": %q, "location": {"path": "config.go", "start_line": 3}}}}`, action, tool, ref)
	}
	r, err := RouteEvent("code_scanning_alert", []byte(payload("closed_by_user", "Exemplar", "refs/pull/7/head")))
	want := store.AlertDismissalArgs{InstallationID: 42, Repo: "acme/app", PR: 7, Alert: 3, RuleID: "possible-secret-in-diff", Path: "config.go",
		StartLine: 3, Reason: "false positive", Comment: "test fixture", Actor: "sec"}
	if err != nil || len(r.AlertDismissals) != 1 || r.AlertDismissals[0] != want {
		t.Fatalf("route = %+v, %v", r, err)
	}
	for name, p := range map[string]string{
		"reopened":       payload("reopened_by_user", "Exemplar", "refs/pull/7/head"),
		"another tool":   payload("closed_by_user", "CodeQL", "refs/pull/7/head"),
		"default branch": payload("closed_by_user", "Exemplar", "refs/heads/main"),
	} {
		if r, err := RouteEvent("code_scanning_alert", []byte(p)); err != nil || len(r.AlertDismissals) != 0 || r.Ignored == "" {
			t.Errorf("%s = %+v, %v", name, r, err)
		}
	}
}

func fakeSecret() string {
	sum := sha256.Sum256([]byte("worker test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

func TestSARIFAndDismissal(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	secret := fakeSecret()
	e.push(t, map[string]string{"config.go": "package main\n\nconst apiToken = \"" + secret + "\"\n"})
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || !out.SARIF || len(e.fake.sarifs) != 1 {
		t.Fatalf("review = %+v, %v (sarifs %d)", out, err, len(e.fake.sarifs))
	}
	log := e.fake.sarifs[0]
	if !strings.Contains(log, `"ruleId":"possible-secret-in-diff"`) || !strings.Contains(log, `"uri":"config.go"`) || strings.Contains(log, secret) ||
		strings.Contains(log, "propagate-context") {
		t.Errorf("sarif = %s", log)
	}

	dw, err := NewAlertDismissalWorker(e.w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dismiss := func(actor, reason string, line int) (CommandOutcome, error) {
		return dw.Handle(t.Context(), store.AlertDismissalArgs{InstallationID: inst, Repo: repo, PR: prNum, Alert: 3,
			RuleID: "possible-secret-in-diff", Path: "config.go", StartLine: line, Reason: reason, Comment: "a fixture", Actor: actor})
	}
	if o, err := dismiss("alice", "false positive", 3); err != nil || o.Recorded != "fb_wrong" || o.Role != "author" {
		t.Errorf("author = %+v, %v", o, err)
	}
	// Moved since posting: the only candidate still matches.
	if o, err := dismiss("sec", "used in tests", 9); err != nil || o.Recorded != "fb_not_important" || o.Role != "reviewer" {
		t.Errorf("security team = %+v, %v", o, err)
	}
	if o, err := dismiss("sec", "something new", 3); err != nil || o.Ignored == "" {
		t.Errorf("unknown reason = %+v, %v", o, err)
	}
	var alert int
	var events int
	if err := st.Pool.QueryRow(t.Context(), `SELECT sarif_alert_number FROM findings WHERE rule_id = 'possible-secret-in-diff' AND gh_comment_id IS NOT NULL`).Scan(&alert); err != nil || alert != 3 {
		t.Errorf("alert number = %d, %v", alert, err)
	}
	if err := st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM feedback_events WHERE via = 'code_scanning'`).Scan(&events); err != nil || events != 2 {
		t.Errorf("events = %d, %v", events, err)
	}

	// The secret is removed: an empty analysis closes the alert.
	e.push(t, map[string]string{"config.go": "package main\n"})
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || !out.SARIF || len(e.fake.sarifs) != 2 || !strings.Contains(e.fake.sarifs[1], `"results":[]`) {
		t.Errorf("after removal = %+v, %v (sarifs %v)", out, err, e.fake.sarifs)
	}
}
