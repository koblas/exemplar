package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// The worker simulation (spec §16.1): one PR head driven through the
// review dispatcher, the claims and an in-process runner by a seeded
// schedule of redelivered webhooks, missed dedupes, runners killed at
// GitHub writes, pushes to newer heads (between runs and mid-review) and
// zombie runners replaying old claims. River's executor
// is played by the sim, on River's own table, so a step is one state
// change; GitHub is fakeGitHub. After every step the invariants hold:
// each finding is posted at most once, each claim is redeemed at most
// once and released, a redelivery enqueues nothing while a job for the
// head lives, and nothing is posted on a head once a push left it. Run one seed with EXEMPLAR_SIM_SEED=<n>; the step trace
// is logged (go test -v) and a failure names its seed. Each seed runs in
// a synctest bubble: no clock is injected, the bubble's is set to the
// wall clock at the start, so the claims' expiry agrees with Postgres.

// simSeeds are the seeds CI runs; 8 lands a push at start-check.
var simSeeds = []uint64{1, 2, 3, 4, 5, 8}

// simSteps is how many scheduled steps a seed runs before the drain.
const simSteps = 10

// simKill is where a runner dies: at one of its GitHub calls, before
// GitHub took it, or after (the response is lost).
type simKill struct {
	route string
	after bool
}

func (k simKill) String() string {
	if k.after {
		return k.route + "/after"
	}
	return k.route + "/before"
}

// simKills are the kill points: named calls, so a seed kills at the same
// place on every run whatever order the lanes' reads take.
var simKills = []simKill{
	{"start-check", false},
	{"post-review", false},
	{"post-review", true},
	{"complete-check", false},
	{"complete-check", true},
	{"list-comments", false},
}

// simPush is where a push to a newer head lands in a running review: at
// one of its GitHub calls before the post, which then never reaches
// GitHub (a push after the pre-post head check posts on the old head,
// which nothing can prevent). get-pr is that pre-post check. A delayed
// push's cancel has not reached the runner yet: only the head moves, and
// the review must notice it itself.
type simPush struct {
	route   string
	delayed bool
}

func (p simPush) String() string {
	if p.delayed {
		return p.route + "/delayed"
	}
	return p.route
}

// simPushes are the push points.
var simPushes = []simPush{
	{"start-check", false},
	{"get-pr", false},
	{"post-review", false},
	{"start-check", true},
}

// simRoute names the review's GitHub writes, the comment listing that
// maps posted findings and the PR read; "" for every other call.
func simRoute(f *fakeGitHub, r *http.Request) string {
	pr := fmt.Sprintf("/repos/%s/pulls/%d", f.repo, prNum)
	checks := "/repos/" + f.repo + "/check-runs"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == pr:
		return "get-pr"
	case r.Method == http.MethodPost && r.URL.Path == checks:
		return "start-check"
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, checks+"/"):
		return "complete-check"
	case r.Method == http.MethodPost && r.URL.Path == pr+"/reviews":
		return "post-review"
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, pr+"/reviews/") && strings.HasSuffix(r.URL.Path, "/comments"):
		return "list-comments"
	}
	return ""
}

// sim is one seeded run.
type sim struct {
	t      *testing.T
	seed   uint64
	rnd    *rand.Rand
	e      env
	st     *store.Store
	river  *river.Client[pgx.Tx]
	claims *Claims
	runner *Runner
	d      *Dispatcher[store.PRReviewArgs]
	trace  []string
	step   int

	// Guarded by e.fake.mu (intercept runs with it held).
	killAt   *simKill
	kill     context.CancelFunc
	killed   bool
	requests int
	// current is the task running; probed is set once its live claim was
	// tried again, and violation is what that probe found wrong.
	current   *Task
	probed    bool
	violation string
	exit      Exit
	// pushAt is where the running review's push lands, to newHead
	// (committed before the run); cancelRun is River's cancel of the run.
	// pushed is set once it landed, started once the run started its check.
	pushAt    *simPush
	newHead   string
	cancelRun context.CancelCauseFunc
	pushed    bool
	started   bool
	// left are the heads pushes left, each with how many reviews GitHub
	// held then: none posted later may be on it.
	left map[string]int
	// heads counts the pushes, naming heads in the trace (SHAs vary).
	heads int

	// tasks are the dispatched tasks, oldest first, for zombie replays.
	tasks []Task
}

func newSim(t *testing.T, seed uint64) *sim {
	t.Helper()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	e.fake.listPosted = true
	claims := newClaims(t, st)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{
		t: t, seed: seed, rnd: rand.New(rand.NewPCG(seed, 0x5eed)), e: e, st: st, river: client, claims: claims, left: map[string]int{},
		runner: &Runner{Config: e.w.cfg, Jobs: client, Snapshot: Snapshots(st, e.w.cfg.Snapshot), Claims: claims, BaseURL: e.fake.srv.URL + "/"},
	}
	e.fake.intercept = s.intercept
	s.d, err = NewReviewDispatcher(DispatchConfig{
		Launcher: s, Snapshot: e.w.cfg.Snapshot, Store: st,
		Claims: claims, Granter: &Granter{Minter: &fakeMinter{}, Store: st},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// intercept counts the runner's calls, kills it at its kill point and
// lands its push at the push point; a dead runner's later calls never
// reach GitHub. It returns abort and apply, as fakeGitHub.intercept.
func (s *sim) intercept(r *http.Request) (bool, bool) {
	s.requests++
	if s.killed {
		return true, false
	}
	if s.current != nil && !s.probed {
		s.probed = true
		s.probe(r.Context(), *s.current)
	}
	if k := s.killAt; k != nil && simRoute(s.e.fake, r) == k.route {
		s.killed, s.killAt = true, nil
		s.kill()
		return true, k.after
	}
	route := simRoute(s.e.fake, r)
	if p := s.pushAt; p != nil && route == p.route && (route != "get-pr" || s.started) {
		s.pushAt = nil
		return s.land(r.Context(), *p), false
	}
	if route == "start-check" {
		s.started = true
	}
	return false, false
}

// land lands the armed push at the runner's current call: the head moves,
// routing cancels the running job and enqueues the new head's in one
// transaction, then River cancels the run (unless the cancel is delayed)
// and the call is dropped unapplied (it returns abort). It runs on the
// fake's goroutine with its lock held, so it records, check fails.
func (s *sim) land(ctx context.Context, p simPush) bool {
	f := s.e.fake
	s.left[f.head] = len(f.reviews)
	f.head, s.pushed = s.newHead, true
	ids, err := s.route(context.WithoutCancel(ctx), s.newHead)
	if s.current == nil || err != nil || !slices.Contains(ids, s.current.JobID) {
		s.violation = fmt.Sprintf("push at %s did not supersede the running job: cancelled %v, %v", p, ids, err)
	}
	if p.delayed {
		return false
	}
	s.cancelRun(rivertype.ErrJobCancelledRemotely)
	return true
}

// route is a push's routing (EventWorker.Work): in one transaction, every
// open review job of another head is cancelled and head's is enqueued.
func (s *sim) route(ctx context.Context, head string) ([]int64, error) {
	var ids []int64
	err := s.st.InTx(ctx, func(tx pgx.Tx, _ *queries.Queries) error {
		var err error
		if ids, err = store.ReviewJobsToSupersede(ctx, tx, s.e.fake.repo, prNum, head); err != nil {
			return fmt.Errorf("jobs to supersede: %w", err)
		}
		for _, id := range ids {
			if _, err := s.river.JobCancelTx(ctx, tx, id); err != nil {
				return fmt.Errorf("cancel job %d: %w", id, err)
			}
		}
		a := args(s.e)
		a.HeadSHA = head
		if _, err := s.river.InsertTx(ctx, tx, a, nil); err != nil {
			return fmt.Errorf("enqueue head: %w", err)
		}
		return nil
	})
	return ids, err
}

// probe runs at a runner's first GitHub call, when it has redeemed its
// claim and the dispatcher has not yet released it: the claim must hold
// its tokens, and neither its nonce nor an earlier attempt's opens it
// again. It runs on the fake's goroutine, so it records, check fails.
func (s *sim) probe(ctx context.Context, task Task) {
	ctx = context.WithoutCancel(ctx)
	var live int
	if err := s.st.Pool.QueryRow(ctx, `SELECT count(*) FROM runner_claims WHERE job_id = $1 AND tokens IS NOT NULL`, task.JobID).Scan(&live); err != nil || live != 1 {
		s.violation = fmt.Sprintf("job %d attempt %d's claim not live at its first GitHub call: %d rows, %v", task.JobID, task.Attempt, live, err)
		return
	}
	for _, old := range append(slices.Clip(s.tasks), task) {
		if old.JobID != task.JobID {
			continue
		}
		if _, _, err := s.claims.Redeem(ctx, old.JobID, old.Nonce); !errors.Is(err, errClaimRefused) {
			s.violation = fmt.Sprintf("job %d attempt %d's live claim redeemed again with attempt %d's nonce: %v", task.JobID, task.Attempt, old.Attempt, err)
			return
		}
	}
}

// Launch implements Launcher: the runner in this process, killed at the
// step's kill point (a killed runner has no exit code: 137, as SIGKILL).
func (s *sim) Launch(ctx context.Context, task Task) (Exit, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.e.fake.mu.Lock()
	s.kill, s.current, s.probed = cancel, &task, false
	s.e.fake.mu.Unlock()
	exit := s.runner.Run(ctx, task.JobID, task.ConfigVersion, task.Nonce)
	s.e.fake.mu.Lock()
	defer s.e.fake.mu.Unlock()
	s.tasks, s.current = append(s.tasks, task), nil
	if s.killed {
		exit = Exit(137)
	}
	s.exit = exit
	return exit, nil
}

func (s *sim) logf(format string, args ...any) {
	line := fmt.Sprintf("step %d: ", s.step) + fmt.Sprintf(format, args...)
	s.trace = append(s.trace, line)
	s.t.Log(line)
}

func (s *sim) fatalf(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d step %d: %s (rerun: EXEMPLAR_SIM_SEED=%d go test -run 'TestWorkerSim$' -v ./internal/worker)",
		s.seed, s.step, fmt.Sprintf(format, args...), s.seed)
}

func (s *sim) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.st.Pool.Exec(s.t.Context(), sql, args...); err != nil {
		s.fatalf("%s: %v", sql, err)
	}
}

func (s *sim) count(sql string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.st.Pool.QueryRow(s.t.Context(), sql, args...).Scan(&n); err != nil {
		s.fatalf("%s: %v", sql, err)
	}
	return n
}

// liveJobs counts the head's jobs River's uniqueness still covers.
func (s *sim) liveJobs() int {
	return s.count(`SELECT count(*) FROM river_job WHERE kind = 'pr_review' AND unique_key IS NOT NULL
		AND state NOT IN ('cancelled', 'discarded') AND args->>'head_sha' = $1`, s.e.head)
}

// deliver is a (re)delivered pull_request webhook: the router's insert.
func (s *sim) deliver() {
	live := s.liveJobs()
	res, err := s.river.Insert(s.t.Context(), args(s.e), nil)
	if err != nil {
		s.fatalf("insert: %v", err)
	}
	s.logf("deliver: job %d skipped=%v", res.Job.ID, res.UniqueSkippedAsDuplicate)
	if res.UniqueSkippedAsDuplicate != (live > 0) {
		s.fatalf("redelivery with %d live jobs: skipped=%v", live, res.UniqueSkippedAsDuplicate)
	}
}

// duplicate is a dedupe River missed: a second job for the same args,
// so the posting layer's own dedupe is what holds.
func (s *sim) duplicate() {
	var id int64
	err := s.st.Pool.QueryRow(s.t.Context(), `INSERT INTO river_job (args, kind, max_attempts, queue, priority, metadata)
		SELECT args, kind, max_attempts, queue, priority, '{}' FROM river_job WHERE kind = 'pr_review' ORDER BY id LIMIT 1
		RETURNING id`).Scan(&id)
	if err != nil {
		s.logf("duplicate: no job to copy")
		return
	}
	s.logf("duplicate: job %d", id)
}

// push is a push to a newer head between runs: routing cancels the old
// head's queued jobs and enqueues the new head's.
func (s *sim) push() {
	head := s.e.commit(s.t, nil)
	s.e.fake.mu.Lock()
	s.left[s.e.fake.head] = len(s.e.fake.reviews)
	s.e.fake.head = head
	s.e.fake.mu.Unlock()
	ids, err := s.route(s.t.Context(), head)
	if err != nil {
		s.fatalf("route push: %v", err)
	}
	s.e.head = head
	s.heads++
	s.logf("push: head %d, cancelled jobs %v", s.heads, ids)
	if n := s.count(`SELECT count(*) FROM river_job WHERE kind = 'pr_review'
		AND state NOT IN ('completed', 'cancelled', 'discarded') AND args->>'head_sha' <> $1`, head); n != 0 {
		s.fatalf("%d jobs of an old head still open after the push", n)
	}
}

// work is River running the next available job once. With chaos, it is
// killed at a seeded point unless this is its last attempt (a discarded
// job is a later slice's redelivery), or else a push to a newer head may
// land in it.
func (s *sim) work(chaos bool) bool {
	ctx := s.t.Context()
	var id int64
	err := s.st.Pool.QueryRow(ctx, `UPDATE river_job SET state = 'running', attempt = attempt + 1, attempted_at = now()
		WHERE id = (SELECT id FROM river_job WHERE kind = 'pr_review' AND state IN ('available', 'retryable') ORDER BY id LIMIT 1)
		RETURNING id`).Scan(&id)
	if err != nil {
		s.logf("work: nothing to run")
		return false
	}
	row, err := s.river.JobGet(ctx, id)
	if err != nil {
		s.fatalf("job %d: %v", id, err)
	}
	job := &river.Job[store.PRReviewArgs]{JobRow: row}
	if err := json.Unmarshal(row.EncodedArgs, &job.Args); err != nil {
		s.fatalf("job %d args: %v", id, err)
	}
	var kill *simKill
	var push *simPush
	switch {
	case chaos && row.Attempt < row.MaxAttempts && s.rnd.IntN(2) == 0:
		kill = &simKills[s.rnd.IntN(len(simKills))]
	case chaos && job.Args.HeadSHA == s.e.head && s.rnd.IntN(2) == 0:
		push = &simPushes[s.rnd.IntN(len(simPushes))]
		s.newHead = s.e.commit(s.t, nil)
	}
	// River's producer cancels the context it gave Work.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s.e.fake.mu.Lock()
	s.killAt, s.killed = kill, false
	s.pushAt, s.pushed, s.started, s.cancelRun = push, false, false, cancel
	before := supersededChecks(s.e.fake)
	s.e.fake.mu.Unlock()
	werr := s.d.Work(ctx, job)
	state := s.settle(row, werr)
	at := "none"
	switch {
	case kill != nil:
		at = "kill " + kill.String()
	case push != nil:
		at = "push " + push.String()
	}
	s.logf("work: job %d attempt %d chaos=%s -> %s", id, row.Attempt, at, state)
	if push != nil {
		s.superseded(*push, job.Args.HeadSHA, state, before)
	}
	return true
}

// superseded checks a run a push was armed in. If it landed, the run
// ended superseded (cancelled by River, or completed once it saw the head
// move itself), its review row is superseded, and the check run it
// started was completed as superseded. A push lands nowhere in a run
// that ends before its point (a retry whose review is already posted).
func (s *sim) superseded(p simPush, old, state string, before int) {
	s.t.Helper()
	f := s.e.fake
	f.mu.Lock()
	pushed, exit, checks := s.pushed, s.exit, supersededChecks(f)-before
	s.pushAt = nil
	f.mu.Unlock()
	if !pushed {
		s.logf("push at %s did not land", p)
		return
	}
	s.e.head = s.newHead
	s.heads++
	s.logf("push at %s: head %d, exit %d", p, s.heads, exit)
	want, wantChecks := "cancelled", 1
	if p.delayed {
		want = "completed"
	} else if exit != ExitSuperseded {
		s.fatalf("superseded run exited %d, want %d", exit, ExitSuperseded)
	}
	if p.route == "start-check" && !p.delayed {
		wantChecks = 0 // the check run never started
	}
	if state != want || checks != wantChecks {
		s.fatalf("push at %s: job %s with %d superseded check runs, want %s with %d", p, state, checks, want, wantChecks)
	}
	if n := s.count(`SELECT count(*) FROM reviews WHERE id = (SELECT max(id) FROM reviews WHERE head_sha = $1) AND status = 'superseded'`, old); n != 1 {
		s.fatalf("push at %s: the old head's review is not superseded", p)
	}
}

// supersededChecks counts check-run updates completing one as superseded;
// f.mu is held.
func supersededChecks(f *fakeGitHub) int {
	n := 0
	for _, u := range f.checkUpdates {
		if out, _ := u["output"].(map[string]any); out["title"] == "Superseded" {
			n++
		}
	}
	return n
}

// settle records Work's result as River's executor would.
func (s *sim) settle(row *rivertype.JobRow, err error) string {
	var cancel *rivertype.JobCancelError
	var snooze *rivertype.JobSnoozeError
	state := "retryable"
	switch {
	case err == nil:
		state = "completed"
	case errors.As(err, &snooze):
		state = "available"
	case errors.As(err, &cancel), errors.Is(err, rivertype.ErrJobCancelledRemotely):
		state = "cancelled"
	case row.Attempt >= row.MaxAttempts:
		state = "discarded"
	}
	s.exec(`UPDATE river_job SET state = $2::river_job_state,
		finalized_at = CASE WHEN $2 IN ('completed', 'cancelled', 'discarded') THEN now() END WHERE id = $1`, row.ID, state)
	return state
}

// replay is a zombie runner: one left over from an earlier dispatch
// redeems its old nonce while its job runs again. It must get nothing.
func (s *sim) replay() {
	if len(s.tasks) == 0 {
		s.logf("replay: no task yet")
		return
	}
	task := s.tasks[s.rnd.IntN(len(s.tasks))]
	ctx := s.t.Context()
	var state string
	if err := s.st.Pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id = $1`, task.JobID).Scan(&state); err != nil {
		s.fatalf("job %d: %v", task.JobID, err)
	}
	s.exec(`UPDATE river_job SET state = 'running', finalized_at = NULL WHERE id = $1`, task.JobID)
	s.e.fake.mu.Lock()
	before := s.requests
	s.e.fake.mu.Unlock()
	exit := s.runner.Run(ctx, task.JobID, task.ConfigVersion, task.Nonce)
	s.e.fake.mu.Lock()
	calls := s.requests - before
	s.e.fake.mu.Unlock()
	s.exec(`UPDATE river_job SET state = $2::river_job_state,
		finalized_at = CASE WHEN $2 IN ('completed', 'cancelled', 'discarded') THEN now() END WHERE id = $1`, task.JobID, state)
	s.logf("replay: job %d attempt %d -> exit %d, %d GitHub calls", task.JobID, task.Attempt, exit, calls)
	if exit != ExitCancel || calls != 0 {
		s.fatalf("zombie runner of job %d attempt %d: exit %d after %d GitHub calls", task.JobID, task.Attempt, exit, calls)
	}
}

// check asserts the invariants between steps.
func (s *sim) check() {
	s.t.Helper()
	f := s.e.fake
	f.mu.Lock()
	posted, markers := map[string]int{}, map[string]int{}
	for _, rv := range f.reviews {
		if _, marker, ok := strings.Cut(fmt.Sprint(rv["body"]), "<!-- exemplar:review "); ok {
			markers[marker]++
		}
		cs, _ := rv["comments"].([]any)
		for _, c := range cs {
			m, _ := c.(map[string]any)
			posted[fmt.Sprint(m["path"], ":", m["side"], ":", m["line"], ":", m["body"])]++
		}
	}
	for i, rv := range f.reviews {
		if n, ok := s.left[fmt.Sprint(rv["commit_id"])]; ok && i >= n && s.violation == "" {
			s.violation = fmt.Sprintf("review %d posted on a head a push left", i)
		}
	}
	checks, reviews, violation := len(f.checks), len(f.reviews), s.violation
	f.mu.Unlock()
	if violation != "" {
		s.fatalf("%s", violation)
	}
	for k, n := range posted {
		if n > 1 {
			s.fatalf("finding posted %d times: %s", n, k)
		}
	}
	// A retried review row finds its own post by marker and posts again
	// nothing, not even an empty review.
	for k, n := range markers {
		if n > 1 {
			s.fatalf("review posted %d times: %s", n, k)
		}
	}
	// Every claim was redeemed by its runner and released by its
	// dispatcher: no tokens are left, and no nonce opens again.
	if n := s.count(`SELECT count(*) FROM runner_claims WHERE tokens IS NOT NULL`); n != 0 {
		s.fatalf("%d claims still hold tokens", n)
	}
	for _, task := range s.tasks {
		if _, _, err := s.claims.Redeem(s.t.Context(), task.JobID, task.Nonce); !errors.Is(err, errClaimRefused) {
			s.fatalf("job %d attempt %d's claim redeemed again: %v", task.JobID, task.Attempt, err)
		}
	}
	s.logf("check: %d comments in %d reviews, %d check runs, %d dispatches, head %d", len(posted), reviews, checks, len(s.tasks), s.heads)
}

// run plays the seed's schedule, then drains the queue with no kills.
func (s *sim) run() {
	s.deliver()
	s.check()
	for s.step = 1; s.step <= simSteps; s.step++ {
		switch p := s.rnd.IntN(100); {
		case p < 20:
			s.deliver()
		case p < 35:
			s.duplicate()
		case p < 45:
			s.push()
		case p < 85:
			s.work(true)
		default:
			s.replay()
		}
		s.check()
	}
	for s.work(false) {
		s.check()
		s.step++
	}
	// Liveness: the head was reviewed, and its finding posted once.
	if n := s.count(`SELECT count(*) FROM river_job WHERE kind = 'pr_review' AND state = 'completed'`); n == 0 {
		s.fatalf("no review job completed")
	}
	s.e.fake.mu.Lock()
	ids := s.e.fake.commentIDs()
	s.e.fake.mu.Unlock()
	if len(ids) != 1 {
		s.fatalf("%d comments posted after the drain, want 1", len(ids))
	}
}

// TestWorkerSim runs the simulation over CI's seeds, or the one in
// EXEMPLAR_SIM_SEED. The sim tests are not parallel: claim clients use
// http.DefaultTransport, whose connections must not cross bubbles, and
// the package's parallel tests wait for sequential ones.
func TestWorkerSim(t *testing.T) {
	seeds := simSeeds
	if v := os.Getenv("EXEMPLAR_SIM_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("EXEMPLAR_SIM_SEED=%q: %v", v, err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			simulate(t, seed)
		})
	}
}

// TestWorkerSimReproduces checks a seed replays step for step, so a
// failing seed reproduces.
func TestWorkerSimReproduces(t *testing.T) {
	a, b := simulate(t, simSeeds[0]), simulate(t, simSeeds[0])
	if !slices.Equal(a, b) {
		t.Errorf("seed %d diverged:\n%s\n---\n%s", simSeeds[0], strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
}

// simulate runs seed in a synctest bubble and returns its trace. The
// bubble's clock starts in 2000; it is slept to the wall clock first, so
// claim expiry (set from time.Now, checked by Postgres now()) holds.
func simulate(t *testing.T, seed uint64) []string {
	t.Helper()
	wall := time.Now()
	var trace []string
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(wall))
		s := newSim(t, seed)
		s.run()
		trace = s.trace
	})
	return trace
}
