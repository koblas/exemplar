package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func mergePolicySnapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	snap := exampleSnapshot(t)
	rc := snap.Repos[repo]
	rc.ApprovalMode = gates.ApprovalMergePolicy
	snap.Repos[repo] = rc
	return snap
}

// policyChecks returns the exemplar/merge-policy runs created.
func policyChecks(f *fakeGitHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.checks {
		if c["name"] == gh.MergePolicyCheckName {
			out = append(out, c)
		}
	}
	return out
}

func checkTitle(c map[string]any) string {
	out, _ := c["output"].(map[string]any)
	return fmt.Sprint(out["title"])
}

func TestMergePolicyCheckName(t *testing.T) {
	if gh.MergePolicyCheckName != gates.MergePolicyCheck {
		t.Fatalf("%q != %q", gh.MergePolicyCheckName, gates.MergePolicyCheck)
	}
}

// A merge_policy_check repo's review never approves; it reports
// exemplar/merge-policy, waiting for people while the gates do not pass.
func TestReviewMergePolicy(t *testing.T) {
	e := setup(t, repo)
	snap := mergePolicySnapshot(t)
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Status != StatusReviewed || out.Approved {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	for _, r := range e.fake.reviews {
		if r["event"] == "APPROVE" {
			t.Fatal("approved in merge_policy_check mode")
		}
	}
	cs := policyChecks(e.fake)
	if len(cs) != 1 || cs[0]["status"] != "in_progress" || cs[0]["head_sha"] != e.head || checkTitle(cs[0]) != "Waiting for a code-owner approval" {
		t.Fatalf("merge policy checks = %+v", cs)
	}

	// A person's approval of the head, from the reviews themselves.
	e.fake.approvals = []string{"bob:APPROVED:" + e.head}
	e.fake.teamMembers = map[string]bool{"bob": true}
	e.fake.codeowners = "* @acme/eng\n"
	if _, err := e.w.Review(t.Context(), args(e)); err != nil {
		t.Fatal(err)
	}
	if cs := policyChecks(e.fake); len(cs) != 2 || cs[1]["conclusion"] != "success" || checkTitle(cs[1]) != "Approved" {
		t.Fatalf("after approval = %+v", cs)
	}
}

func TestMergePolicyWorker(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap := mergePolicySnapshot(t)
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	mw := &MergePolicyWorker{cfg: e.w.cfg}
	a := store.MergePolicyArgs{InstallationID: inst, Repo: repo, PR: prNum, Review: 9, Action: "submitted"}

	e.fake.codeowners = "* @acme/eng\n"
	e.fake.teamMembers = map[string]bool{"bob": true}
	for _, c := range []struct {
		name      string
		approvals []string
		pass      bool
		title     string
	}{
		{"an owner approves the head", []string{"bob:APPROVED:" + e.head}, true, "Approved"},
		{"an approval of an older commit", []string{"bob:APPROVED:0ld"}, false, "Waiting for a code-owner approval"},
		{"a non-owner approves", []string{"carol:APPROVED:" + e.head}, false, "Waiting for a code-owner approval"},
		{"the owner's approval was dismissed", []string{"bob:APPROVED:" + e.head, "bob:DISMISSED:" + e.head}, false, "Waiting for a code-owner approval"},
	} {
		e.fake.approvals = c.approvals
		before := len(policyChecks(e.fake))
		pass, ignored, err := mw.Handle(t.Context(), a)
		cs := policyChecks(e.fake)
		if err != nil || ignored != "" || pass != c.pass || len(cs) != before+1 || checkTitle(cs[len(cs)-1]) != c.title {
			t.Errorf("%s: pass %v, ignored %q, %v, checks %+v", c.name, pass, ignored, err, cs)
		}
	}

	// Gates passed on this head: success whatever people did.
	rid := insertReview(t, st, e.head)
	if _, err := st.Pool.Exec(t.Context(), `INSERT INTO approval_decisions (review_id, stage1, stage2, decision) VALUES ($1, '{}', '{}', 'approved')`, rid); err != nil {
		t.Fatal(err)
	}
	e.fake.approvals = nil
	if pass, _, err := mw.Handle(t.Context(), a); err != nil || !pass {
		t.Errorf("gates passed: %v, %v", pass, err)
	}
	if cs := policyChecks(e.fake); checkTitle(cs[len(cs)-1]) != "Auto-approve gates passed" {
		t.Errorf("last check = %+v", cs[len(cs)-1])
	}

	// A native_approve repo: nothing.
	snap = exampleSnapshot(t)
	before := len(policyChecks(e.fake))
	if _, ignored, err := mw.Handle(t.Context(), a); err != nil || ignored != "repo not in merge_policy_check mode" || len(policyChecks(e.fake)) != before {
		t.Errorf("native repo: %q, %v", ignored, err)
	}
}

func insertReview(t *testing.T, st *store.Store, head string) int64 {
	t.Helper()
	var id int64
	if err := st.Pool.QueryRow(t.Context(), `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, started_at)
		VALUES ($1, $2, $3, 'b', 'v', 'production', 'done', now()) RETURNING id`, repo, prNum, head).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRouteReviewEvents(t *testing.T) {
	payload := func(action, userType, state string) []byte {
		return fmt.Appendf(nil, `{"action": %q, "installation": {"id": 42}, "repository": {"full_name": "acme/app"},
			"pull_request": {"number": 7, "state": %q}, "review": {"id": 99, "user": {"type": %q}}}`, action, state, userType)
	}
	for _, c := range []struct {
		name                    string
		action, userType, state string
		routed                  bool
	}{
		{"a person approves", "submitted", "User", "open", true},
		{"a person edits", "edited", "User", "open", true},
		{"a dismissal, even of a bot's review", "dismissed", "Bot", "open", true},
		{"a bot's review", "submitted", "Bot", "open", false},
		{"a closed PR", "submitted", "User", "closed", false},
	} {
		r, err := RouteEvent("pull_request_review", payload(c.action, c.userType, c.state))
		if err != nil || (len(r.MergePolicy) == 1) != c.routed {
			t.Errorf("%s: %+v, %v", c.name, r, err)
			continue
		}
		if c.routed && (r.MergePolicy[0] != store.MergePolicyArgs{InstallationID: 42, Repo: "acme/app", PR: 7, Review: 99, Action: c.action}) {
			t.Errorf("%s: args %+v", c.name, r.MergePolicy[0])
		}
	}
}
