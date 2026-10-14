package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestAppliedSuggestion(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap := exampleSnapshot(t)
	for i := range snap.Rules {
		if snap.Rules[i].ID == "propagate-context" {
			snap.Rules[i].FixPolicy = schema.FixSuggest
		}
	}
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	fix := "\treq, _ := http.NewRequestWithContext(ctx, http.MethodGet, \"https://inventory.internal/items\", nil)\n\tresp, err := http.DefaultClient.Do(req)"
	e.fix.Store(&fix)
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Posted != 1 {
		t.Fatalf("first review = %+v, %v", out, err)
	}
	comments, _ := e.fake.reviews[0]["comments"].([]any)
	if len(comments) != 1 || !strings.Contains(fmt.Sprint(comments[0]), "```suggestion\n"+fix+"\n```") {
		t.Fatalf("comment = %v", comments)
	}

	// The author commits the suggestion, which also moves the code down a
	// line (a blank line added above).
	applied := strings.Replace(headMain, "\tresp, err := http.Get(\"https://inventory.internal/items\")", fix, 1)
	applied = strings.Replace(applied, "func handler", "\nfunc handler", 1)
	e.report.Store(false)
	e.push(t, map[string]string{"main.go": applied})
	out, err = e.w.Review(t.Context(), args(e))
	if err != nil || out.AppliedSuggestions != 1 {
		t.Fatalf("second review = %+v, %v", out, err)
	}
	var kind, actor, role string
	var weight float32
	if err := st.Pool.QueryRow(t.Context(), `SELECT kind, actor, role, weight FROM feedback_events`).Scan(&kind, &actor, &role, &weight); err != nil {
		t.Fatal(err)
	}
	if kind != "applied_suggestion" || actor != "alice" || role != "author" || weight != 1 {
		t.Errorf("event = %s %s %s %v", kind, actor, role, weight)
	}
	// The suggestion was applied, so the closure records no fixed too.
	var n int
	if err := st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM feedback_events`).Scan(&n); err != nil || n != 1 || out.Fixed != 0 {
		t.Errorf("events = %d, fixed %d, %v", n, out.Fixed, err)
	}
	// A third review of the same head records nothing new.
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.AppliedSuggestions != 0 {
		t.Errorf("repeat = %+v, %v", out, err)
	}
}

func TestHumanAgreedEvent(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	e.fake.humanComments = `[{"id": 5, "user": {"login": "dana", "type": "User"}, "author_association": "MEMBER", "path": "main.go", "side": "RIGHT", "line": 9, "body": "same-concern: pass ctx"}]`
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.HumanDuplicates != 1 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	var kind, actor, role, evidence, reason string
	var weight float32
	if err := st.Pool.QueryRow(t.Context(), `SELECT e.kind, e.actor, e.role, e.weight, e.evidence::text, f.suppressed_reason
		FROM feedback_events e JOIN findings f ON f.id = e.finding_id`).Scan(&kind, &actor, &role, &weight, &evidence, &reason); err != nil {
		t.Fatal(err)
	}
	if kind != "human_agreed" || actor != "dana" || role != "reviewer" || weight != 2 || !strings.Contains(evidence, `"comment_id": 5`) || reason != "human_duplicate" {
		t.Errorf("event = %s %s %s %v %s %s", kind, actor, role, weight, evidence, reason)
	}
}

// Fixed without the suggestion: the author changes the code by hand and
// the light model confirms the change addresses the finding.
func TestFixedWithoutSuggestion(t *testing.T) {
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Posted != 1 {
		t.Fatalf("first review = %+v, %v", out, err)
	}
	fixed := strings.Replace(headMain, "\tresp, err := http.Get(\"https://inventory.internal/items\")",
		"\treq, _ := http.NewRequestWithContext(ctx, http.MethodGet, \"https://inventory.internal/items\", nil)\n\tresp, err := http.DefaultClient.Do(req)", 1)
	e.report.Store(false)
	e.push(t, map[string]string{"main.go": fixed})
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Closed != 1 || out.Fixed != 1 {
		t.Fatalf("second review = %+v, %v", out, err)
	}
	var kind, actor string
	if err := st.Pool.QueryRow(t.Context(), `SELECT kind, actor FROM feedback_events`).Scan(&kind, &actor); err != nil || kind != "fixed" || actor != "alice" {
		t.Errorf("event = %s %s, %v", kind, actor, err)
	}
	var accepted bool
	if err := st.Pool.QueryRow(t.Context(), `SELECT accepted FROM finding_outcomes`).Scan(&accepted); err != nil || !accepted {
		t.Errorf("accepted = %v, %v", accepted, err)
	}
}
