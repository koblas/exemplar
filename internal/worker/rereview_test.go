package worker

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// push moves the PR to a new head with these files over the same base.
// push commits files over the current head on the PR branch, as a
// developer's push would; earlier heads stay fetchable (tagged), as they
// do on GitHub. The base does not move.
func (e *env) push(t *testing.T, files map[string]string) {
	t.Helper()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", *e.dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.invalid", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("checkout", "-q", e.head)
	git("tag", "-f", "head-"+e.head[:12])
	for name, body := range files {
		p := filepath.Join(*e.dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "push")
	head := git("rev-parse", "HEAD")
	git("branch", "-f", "pr", head)
	e.head = head
	e.fake.mu.Lock()
	e.fake.head = head
	e.fake.mu.Unlock()
	if body, ok := files["main.go"]; ok {
		e.main.Store(&body)
	}
}

// rewrite force-pushes: a new head built on the base, not on the old
// head, with files over the base's tree. The old head stays fetchable,
// and the fake's compare API reports it as diverged.
func (e *env) rewrite(t *testing.T, files map[string]string) {
	t.Helper()
	old := e.head
	base := strings.TrimSpace(gitOut(t, *e.dir, "merge-base", "pr", "main"))
	gitOut(t, *e.dir, "tag", "-f", "head-"+old[:12], old)
	gitOut(t, *e.dir, "checkout", "-q", base)
	e.head = base // push commits on top of e.head
	e.push(t, files)
	e.fake.mu.Lock()
	if e.fake.rewritten == nil {
		e.fake.rewritten = map[string]bool{}
	}
	e.fake.rewritten[old] = true
	e.fake.mu.Unlock()
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.invalid", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e *env) state(t *testing.T) (states []string) {
	t.Helper()
	rows, err := e.w.cfg.Store.Pool.Query(t.Context(), `SELECT state || ':' || coalesce(gh_comment_id::text, '-') || ':' || coalesce(suppressed_reason, '')
		FROM findings WHERE rule_id = 'propagate-context' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		states = append(states, s)
	}
	return states
}

// TestReReviewAcrossPushes follows one finding over three pushes: posted
// once, found again but not reposted, then fixed and resolved with a reply.
func TestReReviewAcrossPushes(t *testing.T) {
	e := setup(t, repo)
	e.w.cfg.Store = storetest.New(t)
	review := func() Outcome {
		t.Helper()
		out, err := e.w.Review(t.Context(), args(e))
		if err != nil || out.Status != StatusReviewed {
			t.Fatalf("review = %+v, %v", out, err)
		}
		return out
	}

	// Push 1: the finding is posted and its comment id recorded.
	if out := review(); out.Posted != 1 || out.Closed != 0 {
		t.Fatalf("push 1 = %+v", out)
	}
	if got := e.state(t); len(got) != 1 || got[0] != "posted:1000:" {
		t.Fatalf("push 1 findings = %v", got)
	}

	// Push 2: an unrelated file changes; the finding still holds and is not
	// posted again.
	e.push(t, map[string]string{"main.go": headMain, "README.md": "docs\n"})
	if out := review(); out.Posted != 0 || out.Closed != 0 {
		t.Fatalf("push 2 = %+v", out)
	}
	if got := e.state(t); len(got) != 2 || got[0] != "posted:1000:" || got[1] != "dropped:-:already_posted" {
		t.Fatalf("push 2 findings = %v", got)
	}
	if n := len(e.fake.reviews); n != 2 || e.fake.reviews[1]["comments"] != nil && len(e.fake.reviews[1]["comments"].([]any)) != 0 {
		t.Fatalf("push 2 reposted: %+v", e.fake.reviews[n-1])
	}
	if s := fmt.Sprint(e.fake.reviews[1]["body"]); !strings.Contains(s, "1 finding(s) from earlier pushes still apply") {
		t.Errorf("push 2 summary = %q", s)
	}

	// Push 3: fixed. The rule ran on main.go and no longer finds it: reply
	// "Addressed in <sha7>", resolve the thread, and record it resolved.
	fixed := strings.Replace(headMain, `http.Get("https://inventory.internal/items")`, `http.DefaultClient.Do(req)`, 1)
	e.push(t, map[string]string{"main.go": fixed})
	e.report.Store(false)
	if out := review(); out.Posted != 0 || out.Closed != 1 {
		t.Fatalf("push 3 = %+v", out)
	}
	if got := e.state(t); got[0] != "resolved:1000:" {
		t.Fatalf("push 3 findings = %v", got)
	}
	f := e.fake
	if len(f.replies) != 1 || f.replies[0]["body"] != "Addressed in "+e.head[:7]+"." || fmt.Sprint(f.replies[0]["in_reply_to"]) != "1000" ||
		!f.resolved["T1000"] {
		t.Errorf("replies = %+v, resolved = %v", f.replies, f.resolved)
	}

	// Push 4: nothing open, nothing to close.
	e.push(t, map[string]string{"main.go": fixed, "README.md": "more\n"})
	if out := review(); out.Closed != 0 || len(f.replies) != 1 {
		t.Errorf("push 4 = %+v, replies %d", out, len(f.replies))
	}
}

// countingModel counts generator calls.
type countingModel struct {
	next http.RoundTripper
	n    atomic.Int32
}

func (c *countingModel) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get(llm.HeaderStage) == llm.StageGenerator {
		c.n.Add(1)
	}
	return c.next.RoundTrip(r)
}

// TestInterdiffCarries reviews four pushes: rules whose files did not
// change carry forward with no model call, linked to the run that did the
// work; a change to their files re-runs them.
func TestInterdiffCarries(t *testing.T) {
	model := &countingModel{}
	e := setupWith(t, repo, func(next http.RoundTripper) http.RoundTripper { model.next = next; return model })
	e.w.cfg.Store = storetest.New(t)
	e.report.Store(false) // no findings, so no open finding pins main.go
	review := func() (calls int32) {
		t.Helper()
		before := model.n.Load()
		if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Status != StatusReviewed {
			t.Fatalf("review = %+v, %v", out, err)
		}
		return model.n.Load() - before
	}
	// rule -> carried_from of the latest review's run ("" when it ran).
	latest := func() map[string]string {
		t.Helper()
		rows, err := e.w.cfg.Store.Pool.Query(t.Context(), `SELECT rr.rule_id, coalesce(rr.carried_from::text, '') FROM rule_runs rr
			WHERE rr.review_id = (SELECT max(id) FROM reviews) AND rr.lane = 'exemplar'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, from string
			_ = rows.Scan(&id, &from)
			out[id] = from
		}
		return out
	}
	runID := func(review int, rule string) string {
		var id string
		_ = e.w.cfg.Store.Pool.QueryRow(t.Context(), `SELECT rr.id::text FROM rule_runs rr JOIN reviews r ON r.id = rr.review_id
			WHERE rr.rule_id = $1 ORDER BY r.id LIMIT 1 OFFSET $2`, rule, review-1).Scan(&id)
		return id
	}

	if calls := review(); calls == 0 {
		t.Fatal("push 1 made no model calls")
	}
	if got := latest(); got["propagate-context"] != "" || got["wrap-errors-with-cause"] != "" {
		t.Fatalf("push 1 carried: %v", got)
	}

	// Push 2 touches only a README: both Go rules carry from push 1.
	e.push(t, map[string]string{"main.go": headMain, "README.md": "docs\n"})
	if calls := review(); calls != 0 {
		t.Fatalf("push 2 made %d model calls", calls)
	}
	got := latest()
	if got["propagate-context"] != runID(1, "propagate-context") || got["wrap-errors-with-cause"] != runID(1, "wrap-errors-with-cause") {
		t.Fatalf("push 2 carried_from = %v", got)
	}
	if s := fmt.Sprint(e.fake.checkUpdates[len(e.fake.checkUpdates)-1]["output"]); !strings.Contains(s, "2 rule(s) carried forward") {
		t.Errorf("check text = %s", s)
	}

	// Push 3 changes main.go: they re-run.
	e.push(t, map[string]string{"main.go": headMain + "\n// changed\n"})
	if calls := review(); calls == 0 {
		t.Fatal("push 3 carried rules over a changed file")
	}
	if got := latest(); got["propagate-context"] != "" {
		t.Fatalf("push 3 carried: %v", got)
	}

	// Push 4: carried again, from push 3's runs (the original, not a copy).
	e.push(t, map[string]string{"main.go": headMain + "\n// changed\n", "README.md": "more\n"})
	if calls := review(); calls != 0 {
		t.Fatalf("push 4 made %d model calls", calls)
	}
	if got := latest(); got["propagate-context"] != runID(3, "propagate-context") {
		t.Errorf("push 4 carried_from = %v, want push 3's run %s", got, runID(3, "propagate-context"))
	}
}

// TestRebaseReanchors: after a force-push, an open finding whose code
// moved is re-anchored (not reposted); one whose code is gone is outdated
// with "No longer present after rebase".
func TestRebaseReanchors(t *testing.T) {
	e := setup(t, repo)
	st := storetest.New(t)
	e.w.cfg.Store = st
	review := func() Outcome {
		t.Helper()
		out, err := e.w.Review(t.Context(), args(e))
		if err != nil || out.Status != StatusReviewed {
			t.Fatalf("review = %+v, %v", out, err)
		}
		return out
	}
	finding := func() (state string, start int, code string) {
		t.Helper()
		var c *string
		if err := st.Pool.QueryRow(t.Context(), `SELECT state, start_line, anchor_code FROM findings
			WHERE rule_id = 'propagate-context' ORDER BY id LIMIT 1`).Scan(&state, &start, &c); err != nil {
			t.Fatal(err)
		}
		if c != nil {
			code = *c
		}
		return state, start, code
	}

	if out := review(); out.Posted != 1 {
		t.Fatalf("push 1 = %+v", out)
	}
	if state, start, code := finding(); state != "posted" || start != 9 || !strings.Contains(code, `http.Get("https://inventory.internal/items")`) {
		t.Fatalf("push 1 finding = %s %d %q", state, start, code)
	}

	// Force-push: two lines added near the top, outside the anchor's
	// context, so the same code (and fingerprint) moves to line 11. Lines
	// added inside the ±2 context would change its identity instead.
	shifted := strings.Replace(headMain, "package main\n", "package main\n\n// Package main fetches items.\n", 1)
	e.rewrite(t, map[string]string{"main.go": shifted})
	e.line.Store(11)
	out := review()
	if out.Moved != 1 || out.Posted != 0 || out.Closed != 0 {
		t.Fatalf("shifted rebase = %+v", out)
	}
	if state, start, _ := finding(); state != "posted" || start != 11 {
		t.Fatalf("after the shift: %s at %d", state, start)
	}
	if len(e.fake.replies) != 0 {
		t.Errorf("replied to a moved finding: %+v", e.fake.replies)
	}

	// Force-push that drops the call: outdated, replied to and resolved.
	fixed := strings.Replace(shifted, `http.Get("https://inventory.internal/items")`, `http.DefaultClient.Do(req)`, 1)
	e.rewrite(t, map[string]string{"main.go": fixed})
	e.report.Store(false)
	if out := review(); out.Closed != 1 || out.Moved != 0 {
		t.Fatalf("removing rebase = %+v", out)
	}
	if state, _, _ := finding(); state != "outdated" {
		t.Fatalf("after removal: %s", state)
	}
	if len(e.fake.replies) != 1 || e.fake.replies[0]["body"] != "No longer present after rebase." || !e.fake.resolved["T1000"] {
		t.Errorf("replies = %+v, resolved = %v", e.fake.replies, e.fake.resolved)
	}
}
