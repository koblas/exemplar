package worker

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func pullRequest(action, state string) string { return pullRequestAt(action, state, "abc") }

func pullRequestAt(action, state, head string) string {
	return fmt.Sprintf(`{"action": %q, "number": 7, "installation": {"id": 42}, "repository": {"full_name": "acme/app"},
		"pull_request": {"state": %q, "head": {"sha": %q}}}`, action, state, head)
}

func checkRun(action, identifier, name string) string {
	return fmt.Sprintf(`{"action": %q, "requested_action": {"identifier": %q}, "installation": {"id": 42}, "repository": {"full_name": "acme/app"},
		"check_run": {"name": %q, "head_sha": "abc", "pull_requests": [{"number": 7}]}}`, action, identifier, name)
}

func TestRouteEvent(t *testing.T) {
	want := store.PRReviewArgs{InstallationID: 42, Repo: "acme/app", PR: 7, HeadSHA: "abc"}
	tests := []struct {
		name, event, payload string
		review, rerun        bool
	}{
		{"opened", "pull_request", pullRequest("opened", "open"), true, false},
		{"synchronize", "pull_request", pullRequest("synchronize", "open"), true, false},
		{"reopened", "pull_request", pullRequest("reopened", "open"), true, false},
		{"ready_for_review", "pull_request", pullRequest("ready_for_review", "open"), true, false},
		{"base changed", "pull_request", `{"action": "edited", "number": 7, "installation": {"id": 42}, "repository": {"full_name": "acme/app"},
			"pull_request": {"state": "open", "head": {"sha": "abc"}}, "changes": {"base": {"ref": {"from": "dev"}}}}`, true, false},
		{"title edited", "pull_request", `{"action": "edited", "number": 7, "pull_request": {"state": "open"}, "changes": {"title": {"from": "x"}}}`, false, false},
		{"labeled", "pull_request", pullRequest("labeled", "open"), false, false},
		{"rerun action", "check_run", checkRun("requested_action", "rerun", "exemplar"), true, true},
		{"rerequested", "check_run", checkRun("rerequested", "", "exemplar"), true, true},
		{"someone else's check", "check_run", checkRun("rerequested", "", "ci"), false, false},
		{"completed check", "check_run", checkRun("completed", "", "exemplar"), false, false},
		{"push", "push", `{}`, false, false},
	}
	for _, tt := range tests {
		r, err := RouteEvent(tt.event, []byte(tt.payload))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if tt.review != (len(r.Reviews) == 1 && r.Reviews[0] == want) || r.ReRun != tt.rerun || (!tt.review && r.Ignored == "") {
			t.Errorf("%s: %+v", tt.name, r)
		}
	}
	if r, _ := RouteEvent("pull_request", []byte(pullRequest("synchronize", "open"))); r.Supersede == nil ||
		*r.Supersede != (Supersede{Repo: "acme/app", PR: 7, KeepHead: "abc"}) {
		t.Errorf("push supersede = %+v", r.Supersede)
	}
	if r, _ := RouteEvent("pull_request", []byte(pullRequest("closed", "closed"))); len(r.Reviews) != 0 || r.Supersede == nil ||
		*r.Supersede != (Supersede{Repo: "acme/app", PR: 7}) {
		t.Errorf("closed = %+v", r)
	}
	// Another check completing is a late-CI linter check (spec §6.10).
	if r, err := RouteEvent("check_run", []byte(checkRun("completed", "", "golangci-lint"))); err != nil || len(r.Reviews) != 0 ||
		len(r.LinterChecks) != 1 || r.LinterChecks[0] != (store.LinterCheckArgs{InstallationID: 42, Repo: "acme/app", PR: 7, HeadSHA: "abc", Check: "golangci-lint"}) {
		t.Errorf("linter completed = %+v, %v", r, err)
	}
	if r, _ := RouteEvent("check_run", []byte(checkRun("completed", "", "exemplar"))); len(r.LinterChecks) != 0 {
		t.Errorf("Exemplar's own check routed as a linter: %+v", r)
	}
	if r, _ := RouteEvent("check_run", []byte(checkRun("rerequested", "", "exemplar"))); r.Supersede != nil {
		t.Errorf("a re-run superseded reviews: %+v", r.Supersede)
	}
	if r, _ := RouteEvent("pull_request", nil); r.Ignored != "payload expired" {
		t.Errorf("expired payload = %+v", r)
	}
	if _, err := RouteEvent("pull_request", []byte(`{"action": "opened", "pull_request": {"state": "open"}}`)); err == nil {
		t.Error("payload without a head accepted")
	}
}

// TestEventWorkerRoutes runs gh_event jobs through River: a push enqueues
// a review once, and a Re-run enqueues the next attempt only after an
// incomplete review.
func TestEventWorkerRoutes(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	w, err := NewEventWorker(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{store.EventQueue: {MaxWorkers: 1}}, Workers: workers,
		// pr_review jobs are only listed here, not worked.
		SkipUnknownJobCheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(ctx) }()

	deliver := func(event, payload string) {
		t.Helper()
		id := uuid.New()
		if _, err := st.InsertGHEvent(ctx, queries.InsertGHEventParams{DeliveryID: id, Event: event, InstallationID: 42, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Insert(ctx, store.GHEventArgs{DeliveryID: id.String()}, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-events:
			if ev.Kind != river.EventKindJobCompleted {
				t.Fatalf("event job %s: %v", ev.Kind, ev.Job.Errors)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("event job did not finish")
		}
	}
	reviews := func() []store.PRReviewArgs {
		t.Helper()
		res, err := client.JobList(ctx, river.NewJobListParams().Kinds("pr_review").States(rivertype.JobStateAvailable))
		if err != nil {
			t.Fatal(err)
		}
		var out []store.PRReviewArgs
		for _, j := range res.Jobs {
			var a store.PRReviewArgs
			if err := json.Unmarshal(j.EncodedArgs, &a); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}

	deliver("pull_request", pullRequest("synchronize", "open"))
	deliver("pull_request", pullRequest("synchronize", "open")) // a second delivery of the same push
	if got := reviews(); len(got) != 1 || got[0].Attempt != 0 || got[0].HeadSHA != "abc" {
		t.Fatalf("reviews = %+v", got)
	}

	// The head's review completed: Re-run is ignored.
	if _, err := st.Pool.Exec(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, attempt, status)
		VALUES ('acme/app', 7, 'abc', 'b', 'v', 'production', 1, 'done')`); err != nil {
		t.Fatal(err)
	}
	deliver("check_run", checkRun("requested_action", "rerun", "exemplar"))
	if got := reviews(); len(got) != 1 {
		t.Fatalf("re-ran a complete review: %+v", got)
	}
	// Incomplete: the next attempt.
	if _, err := st.Pool.Exec(ctx, `UPDATE reviews SET status = 'incomplete' WHERE head_sha = 'abc'`); err != nil {
		t.Fatal(err)
	}
	deliver("check_run", checkRun("requested_action", "rerun", "exemplar"))
	if got := reviews(); len(got) != 2 || got[1].Attempt != 2 {
		t.Fatalf("re-run = %+v", got)
	}

	// A push to a new head cancels both of abc's queued reviews.
	deliver("pull_request", pullRequestAt("synchronize", "open", "def"))
	if got := reviews(); len(got) != 1 || got[0].HeadSHA != "def" {
		t.Fatalf("after a push = %+v", got)
	}
	cancelled, err := client.JobList(ctx, river.NewJobListParams().Kinds("pr_review").States(rivertype.JobStateCancelled))
	if err != nil || len(cancelled.Jobs) != 2 {
		t.Fatalf("cancelled = %d, %v", len(cancelled.Jobs), err)
	}
	// Closing the PR cancels the rest.
	deliver("pull_request", pullRequestAt("closed", "closed", "def"))
	if got := reviews(); len(got) != 0 {
		t.Fatalf("after close = %+v", got)
	}
}
