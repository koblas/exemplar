package worker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// The worker simulation (spec §16.1): one PR driven through the webhook
// receiver's store, the event router (EventWorker), the review dispatcher,
// the claims and an in-process runner by a seeded schedule of deliveries
// and redeliveries, event jobs worked twice, Re-runs, missed dedupes,
// model outages, GitHub hanging past the client's timeout (then applying
// the call late or dropping it), runners starting late (in time or past
// the job's timeout), two runners at once (contending for the repo's review
// slots, or reviewing at once), runners killed at GitHub writes or at their own Postgres
// writes (on any attempt, so jobs are discarded; a hard kill is a SIGKILL,
// which also cuts the runner off Postgres), pushes to newer heads (between runs and mid-review) and
// zombie runners replaying old claims. River's executor is played by the
// sim, on River's own table, so a step is one state change; GitHub is
// fakeGitHub. After every step the invariants hold:
// each finding is posted at most once, each claim is redeemed at most
// once and released, a delivery enqueues nothing while a job for the
// head lives, a redelivery or a routed event's second run enqueues
// nothing, a Re-run enqueues only after an incomplete review, and nothing
// is posted on a head once a push left it. Run one seed with EXEMPLAR_SIM_SEED=<n>; the step trace
// is logged (go test -v) and a failure names its seed. Each seed runs in
// a synctest bubble: no clock is injected, the bubble's is set to the
// wall clock at the start, so the claims' expiry agrees with Postgres.

// simSeeds are the seeds CI runs. Between them they Re-run (enqueued
// and ignored), black out the model, discard and abandon a job,
// redeliver, route an event twice, land a push at each of its points,
// kill a runner hard at a GitHub call and at each of its Postgres kill
// points, overlap two runs at each park point (snoozed for a full repo or
// a busy head, or both reviewing), hang GitHub at each stall point (the
// call then applied and dropped), and start a runner late, in time and
// past its job's timeout. The seeds after 5 add what 1-5 miss.
var simSeeds = []uint64{1, 2, 3, 4, 5, 14, 21, 57, 80, 120, 123, 141, 159, 177}

// simBusySeed is the CI seed with the most chaos, replayed for
// TestWorkerSimReproduces.
const simBusySeed = 159

// simParks are where an overlap's first runner waits while a second one
// runs: before its check run, at the pre-post head check (after the check
// run started) and before its post.
var simParks = []string{"start-check", "get-pr", "post-review"}

// simStalls are where GitHub hangs during a run, past the client's
// 30-second timeout; then it applies the call late or drops it.
var simStalls = []string{"start-check", "get-pr", "post-review", "complete-check", "list-comments"}

// simSteps is how many scheduled steps a seed runs before the drain.
const simSteps = 10

// simKill is where a runner dies: at one of its GitHub calls, before
// GitHub took it or after (the response is lost), or at one of its
// Postgres queries (db, by sqlc name), before it is sent or once it has
// applied. A hard kill is a SIGKILL: the runner's Postgres connections
// drop with it, so its deferred cleanup and in-flight writes never land;
// a soft one only cancels the run, as a timeout or a shutdown does. A
// kill at a query is always hard.
type simKill struct {
	route string
	after bool
	db    bool
	hard  bool
}

func (k simKill) String() string {
	out := k.route + "/before"
	if k.after {
		out = k.route + "/after"
	}
	if k.db {
		out = "db:" + out
	}
	if k.hard {
		out += " hard"
	}
	return out
}

// simKills are the kill points: named calls and queries, so a seed kills
// at the same place on every run whatever order the lanes' reads take.
// The queries are the runner's sequential writes: redeeming its claim,
// storing the review (Finish, which is not one transaction, so a kill
// between its writes leaves part of them) and mapping posted comments.
var simKills = []simKill{
	{route: "start-check"},
	{route: "post-review"},
	{route: "post-review", after: true},
	{route: "complete-check"},
	{route: "complete-check", after: true},
	{route: "list-comments"},
	{route: "RedeemRunnerClaim", db: true},
	{route: "RedeemRunnerClaim", after: true, db: true},
	{route: "InsertFinding", after: true, db: true},
	{route: "FinishReview", db: true},
	{route: "FinishReview", after: true, db: true},
	{route: "MarkFindingPosted", db: true},
}

// errSimKilled is a hard-killed runner's Postgres.
var errSimKilled = errors.New("sim: runner killed")

// simDB is the runner's own Postgres pool, as a runner pod has. A hard
// kill cuts it: its live connections drop and new ones are refused. It
// traces the runner's queries to kill at a named one (at), and counts
// those that still succeed once the runner is dead (afterDeath, which
// must stay 0). The control process (the dispatcher, sim.st) keeps its
// own pool.
type simDB struct {
	st *store.Store

	mu         sync.Mutex
	dead       bool
	conns      map[net.Conn]bool
	at         *simKill
	fire       func()
	afterDeath int
}

// newSimDB opens the runner's pool on st's database.
func newSimDB(t *testing.T, st *store.Store) *simDB {
	t.Helper()
	d := &simDB{conns: map[net.Conn]bool{}}
	cfg := st.Pool.Config()
	cfg.ConnConfig.DialFunc = d.dial
	cfg.ConnConfig.Tracer = d
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	d.st = &store.Store{Pool: pool, Queries: queries.New(pool)}
	return d
}

func (d *simDB) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	dead := d.dead
	d.mu.Unlock()
	if dead {
		return nil, errSimKilled
	}
	c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead {
		_ = c.Close()
		return nil, errSimKilled
	}
	d.conns[c] = true
	return &simConn{Conn: c, d: d}, nil
}

// simConn is one of the runner's connections.
type simConn struct {
	net.Conn

	d *simDB
}

func (c *simConn) Close() error {
	c.d.mu.Lock()
	delete(c.d.conns, c.Conn)
	c.d.mu.Unlock()
	return c.Conn.Close()
}

// cut drops the runner off Postgres.
func (d *simDB) cut() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dead = true
	for c := range d.conns {
		_ = c.Close()
	}
	clear(d.conns)
}

// revive is the next runner: a fresh pool, nothing armed. It returns how
// many queries succeeded after the last one's death.
func (d *simDB) revive() int {
	d.mu.Lock()
	n := d.afterDeath
	d.dead, d.at, d.fire, d.afterDeath = false, nil, nil, 0
	d.mu.Unlock()
	d.st.Pool.Reset()
	return n
}

// arm kills the runner (fire) at k's query.
func (d *simDB) arm(k *simKill, fire func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.at, d.fire = k, fire
}

type simQueryKey struct{}

// TraceQueryStart implements pgx.QueryTracer.
func (d *simDB) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	_, name, _ := strings.Cut(data.SQL, "-- name: ")
	name, _, _ = strings.Cut(name, " ")
	d.trip(name, false)
	return context.WithValue(ctx, simQueryKey{}, name)
}

// TraceQueryEnd implements pgx.QueryTracer.
func (d *simDB) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil {
		return
	}
	d.mu.Lock()
	if d.dead {
		d.afterDeath++
	}
	d.mu.Unlock()
	name, _ := ctx.Value(simQueryKey{}).(string)
	d.trip(name, true)
}

// trip fires the armed kill at query name, before it is sent or after it
// applied.
func (d *simDB) trip(name string, after bool) {
	d.mu.Lock()
	k, fire := d.at, d.fire
	if k == nil || k.route != name || k.after != after {
		d.mu.Unlock()
		return
	}
	d.at = nil
	d.mu.Unlock()
	fire()
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
	// db is the runner's Postgres, which a hard kill cuts.
	db *simDB
	// limit is the repo's review limit (queues.per_repo_concurrency), 1
	// or 2 by seed.
	limit int
	// lateBy delays the next runner's start (its pod pending).
	lateBy time.Duration
	// runCalls are the GitHub calls the last runner made (the control
	// process's own, abandoning its job, not included). Guarded by e.fake.mu.
	runCalls int
	d        *Dispatcher[store.PRReviewArgs]
	events   *EventWorker
	// outage, while set, fails every generator call (simOutage).
	outage *atomic.Bool
	trace  []string
	step   int
	// deliveries are the delivery ids the receiver stored, oldest first.
	deliveries []uuid.UUID

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
	pushID    uuid.UUID
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

// simConns is the pool of control's store in the sim. An overlap holds
// two reviews' dedicated connections (each its head lock and repo slot)
// while the second dispatch mints its claim, so pgxpool's default of
// max(4, NumCPU) deadlocks on a 4-CPU machine: the parked review waits for
// the second, which waits for a connection. serve sizes its pool for its
// slots the same way (cmd/exemplar-worker).
const simConns = 8

func newSim(t *testing.T, seed uint64) *sim {
	t.Helper()
	st := storetest.NewWithConns(t, simConns)
	outage := &atomic.Bool{}
	e := setupWith(t, repo, func(next http.RoundTripper) http.RoundTripper { return &simOutage{next: next, on: outage} })
	e.w.cfg.Store = st
	e.fake.listPosted = true
	claims := newClaims(t, st)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The runner reaches Postgres only through its own pool: its review,
	// config snapshot, claim and job row.
	db := newSimDB(t, st)
	runnerClaims, err := NewClaims(db.st, []byte("test claim key"))
	if err != nil {
		t.Fatal(err)
	}
	runnerJobs, err := river.NewClient(riverpgxv5.New(db.st.Pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rcfg := e.w.cfg
	rcfg.Store = db.st
	s := &sim{
		t: t, seed: seed, rnd: rand.New(rand.NewPCG(seed, 0x5eed)), e: e, st: st, river: client, claims: claims, left: map[string]int{}, outage: outage, db: db,
		runner: &Runner{Config: rcfg, Jobs: runnerJobs, Snapshot: Snapshots(db.st, e.w.cfg.Snapshot), Claims: runnerClaims, BaseURL: e.fake.url, Transport: e.fake.transport},
	}
	e.fake.intercept = s.intercept
	s.limit = 1 + s.rnd.IntN(2)
	// The dispatcher's config sets the repo's limit; the version is the
	// same, so the runner's own snapshot is unchanged.
	limited := func(ctx context.Context) (*config.Snapshot, error) {
		snap, err := e.w.cfg.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		cp := *snap
		cp.Global.Queues.PerRepoConcurrency, cp.Global.Queues.LargeRepos = s.limit, nil
		return &cp, nil
	}
	s.d, err = NewReviewDispatcher(DispatchConfig{
		Launcher: s, Snapshot: limited, Store: st,
		Claims: claims, Granter: &Granter{Minter: &fakeMinter{}, Store: st},
		Abandon: Abandoner{Store: st, GitHub: e.fake}.Abandon,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.events, err = NewEventWorker(st, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

// simOutage fails every generator call while on, as the gateway answers
// when every provider of the stage is down (503, not to be retried): the
// review ends incomplete.
type simOutage struct {
	next http.RoundTripper
	on   *atomic.Bool
}

func (o *simOutage) RoundTrip(r *http.Request) (*http.Response, error) {
	if !o.on.Load() || r.Header.Get(llm.HeaderStage) != llm.StageGenerator {
		return o.next.RoundTrip(r)
	}
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}, "X-Should-Retry": {"false"}},
		Body:   io.NopCloser(strings.NewReader(`{"type": "error", "error": {"type": "overloaded_error", "message": "every provider for this stage is unavailable"}}`)),
	}, nil
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
		s.killAt = nil
		s.die(k.hard)
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
	ctx = context.WithoutCancel(ctx)
	err := s.deliverHead(ctx, s.pushID, "synchronize", s.newHead)
	// JobCancelTx marks a running job; River's producer then cancels it.
	var marked bool
	if err == nil && s.current != nil {
		err = s.st.Pool.QueryRow(ctx, `SELECT metadata ? 'cancel_attempted_at' FROM river_job WHERE id = $1`, s.current.JobID).Scan(&marked)
	}
	if s.current == nil || err != nil || !marked {
		s.violation = fmt.Sprintf("push at %s did not supersede the running job: %v", p, err)
	}
	if p.delayed {
		return false
	}
	s.cancelRun(rivertype.ErrJobCancelledRemotely)
	return true
}

// receive is the webhook receiver: a delivery is stored once, by its id,
// in one transaction with the event job that routes it; a redelivery of
// the same id stores nothing.
func (s *sim) receive(ctx context.Context, id uuid.UUID, event, payload string) (bool, error) {
	stored := false
	repoName := strings.ToLower(s.e.fake.repo)
	err := s.st.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
		n, err := q.InsertGHEvent(ctx, queries.InsertGHEventParams{
			DeliveryID: id, Event: event, InstallationID: inst, RepoFullName: &repoName, Payload: []byte(payload),
		})
		if err != nil || n == 0 {
			return err
		}
		stored = true
		_, err = s.river.InsertTx(ctx, tx, store.GHEventArgs{DeliveryID: id.String()}, nil)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("receive %s: %w", event, err)
	}
	if stored {
		s.deliveries = append(s.deliveries, id)
	}
	return stored, nil
}

// routeEvents is River working each queued event job once.
func (s *sim) routeEvents(ctx context.Context) error {
	for {
		var id int64
		err := s.st.Pool.QueryRow(ctx, `UPDATE river_job SET state = 'running', attempt = attempt + 1, attempted_at = now()
			WHERE id = (SELECT id FROM river_job WHERE kind = 'gh_event' AND state = 'available' ORDER BY id LIMIT 1)
			RETURNING id`).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim event job: %w", err)
		}
		if err := s.workEvent(ctx, id); err != nil {
			return err
		}
	}
}

// workEvent runs running event job id through EventWorker's routing,
// with the sim's River client (River's executor puts it in Work's
// context), and settles it. Routing these events never fails.
func (s *sim) workEvent(ctx context.Context, id int64) error {
	row, err := s.river.JobGet(ctx, id)
	if err != nil {
		return fmt.Errorf("event job %d: %w", id, err)
	}
	job := &river.Job[store.GHEventArgs]{JobRow: row}
	if err := json.Unmarshal(row.EncodedArgs, &job.Args); err != nil {
		return fmt.Errorf("event job %d args: %w", id, err)
	}
	werr := s.events.route(ctx, s.river, job)
	if _, err := s.st.Pool.Exec(ctx, simSettle, id, settleState(row, werr)); err != nil {
		return fmt.Errorf("settle event job %d: %w", id, err)
	}
	if werr != nil {
		return fmt.Errorf("route event job %d: %w", id, werr)
	}
	return nil
}

// deliverHead receives and routes delivery id of a pull_request event
// for head.
func (s *sim) deliverHead(ctx context.Context, id uuid.UUID, action, head string) error {
	payload := fmt.Sprintf(`{"action": %q, "number": %d, "installation": {"id": %d}, "repository": {"full_name": %q},
		"pull_request": {"state": "open", "head": {"sha": %q}}}`, action, prNum, inst, s.e.fake.repo, head)
	if _, err := s.receive(ctx, id, "pull_request", payload); err != nil {
		return err
	}
	return s.routeEvents(ctx)
}

// delivery is a new delivery id, drawn from the seed so a trace repeats.
// A push armed in a run draws its id before it lands, if it does.
func (s *sim) delivery() uuid.UUID {
	var id uuid.UUID
	binary.BigEndian.PutUint64(id[:8], s.rnd.Uint64())
	binary.BigEndian.PutUint64(id[8:], s.rnd.Uint64())
	return id
}

// probe runs at a runner's first GitHub call, when it has redeemed its
// claim and the dispatcher has not yet released it: the claim must hold
// its tokens, and neither its nonce nor an earlier attempt's opens it
// again. It runs on the fake's goroutine, so it records, check fails.
func (s *sim) probe(ctx context.Context, task Task) {
	ctx = context.WithoutCancel(ctx)
	if held, err := s.slotsHeld(ctx); err != nil || held > s.limit {
		s.violation = fmt.Sprintf("job %d attempt %d ran with %d of the repo's slots held, limit %d: %v", task.JobID, task.Attempt, held, s.limit, err)
		return
	}
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

// die kills the running runner; hard also cuts it off Postgres. f.mu is
// held.
func (s *sim) die(hard bool) {
	s.killed = true
	if hard {
		s.db.cut()
	}
	s.kill()
}

// slotsHeld counts the repo's review slots held now (Postgres advisory
// locks); one past the limit counts too, as it must never be taken.
func (s *sim) slotsHeld(ctx context.Context) (int, error) {
	rows, err := s.st.Pool.Query(ctx, `SELECT classid::bigint, objid::bigint FROM pg_locks
		WHERE locktype = 'advisory' AND objsubid = 1 AND granted`)
	if err != nil {
		return 0, fmt.Errorf("advisory locks: %w", err)
	}
	locks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]int64, error) {
		var l [2]int64
		err := r.Scan(&l[0], &l[1])
		return l, err
	})
	if err != nil {
		return 0, fmt.Errorf("advisory locks: %w", err)
	}
	n := 0
	for slot := range s.limit + 1 {
		key := uint64(slotKey(s.e.fake.repo, slot))
		if slices.Contains(locks, [2]int64{int64(uint32(key >> 32)), int64(uint32(key))}) {
			n++
		}
	}
	return n, nil
}

// Launch implements Launcher: the runner in this process, killed at the
// step's kill point (a killed runner has no exit code: 137, as SIGKILL).
func (s *sim) Launch(ctx context.Context, task Task) (Exit, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.e.fake.mu.Lock()
	s.kill, s.current, s.probed = cancel, &task, false
	if k := s.killAt; k != nil && k.db {
		// A kill at a query fires on the runner's goroutine, without f.mu.
		s.killAt = nil
		s.db.arm(k, func() {
			s.e.fake.mu.Lock()
			defer s.e.fake.mu.Unlock()
			s.die(true)
		})
	}
	s.e.fake.mu.Unlock()
	if d := s.lateBy; d > 0 {
		s.lateBy = 0
		time.Sleep(d)
	}
	s.e.fake.mu.Lock()
	before := s.requests
	s.e.fake.mu.Unlock()
	exit := s.runner.Run(ctx, task.JobID, task.ConfigVersion, task.Nonce)
	s.e.fake.mu.Lock()
	defer s.e.fake.mu.Unlock()
	s.runCalls = s.requests - before
	s.tasks, s.current = append(s.tasks, task), nil
	if s.killed {
		exit = Exit(137)
	}
	// A dead runner wrote nothing more; the next one gets a fresh pool.
	if n := s.db.revive(); n != 0 && s.violation == "" {
		s.violation = fmt.Sprintf("job %d's runner ran %d queries after its hard kill", task.JobID, n)
	}
	// The run is over: the control process's own GitHub calls (abandoning
	// the job) get through, and an unfired kill or push lands nowhere.
	s.exit, s.killed, s.killAt, s.pushAt = exit, false, nil, nil
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

// jobs counts the review jobs with exactly these args, or only those
// River's uniqueness still covers (live).
func (s *sim) jobs(a store.PRReviewArgs, live bool) int {
	s.t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		s.fatalf("encode args: %v", err)
	}
	return s.count(`SELECT count(*) FROM river_job WHERE kind = 'pr_review' AND args = $1::jsonb
		AND (NOT $2 OR (unique_key IS NOT NULL AND state NOT IN ('cancelled', 'discarded')))`, b, live)
}

// reviewJobs counts every review job.
func (s *sim) reviewJobs() int {
	s.t.Helper()
	return s.count(`SELECT count(*) FROM river_job WHERE kind = 'pr_review'`)
}

// deliver is a new delivery of the head's pull_request webhook (opened,
// then synchronize, reopened and the like): routing enqueues the head's
// review unless River's uniqueness holds a live one.
func (s *sim) deliver(action string) {
	s.t.Helper()
	a := args(s.e)
	live, total := s.jobs(a, true), s.jobs(a, false)
	if err := s.deliverHead(s.t.Context(), s.delivery(), action, s.e.head); err != nil {
		s.fatalf("deliver: %v", err)
	}
	added := s.jobs(a, false) - total
	s.logf("deliver %d: %s, %d review jobs enqueued", len(s.deliveries), action, added)
	if (added == 0) != (live > 0) || added > 1 {
		s.fatalf("delivery with %d live jobs enqueued %d", live, added)
	}
}

// redeliver is GitHub redelivering an earlier delivery by its id: the
// receiver stores nothing, so nothing is routed again.
func (s *sim) redeliver() {
	s.t.Helper()
	if len(s.deliveries) == 0 {
		s.logf("redeliver: no delivery yet")
		return
	}
	i := s.rnd.IntN(len(s.deliveries))
	ctx, total := s.t.Context(), s.count(`SELECT count(*) FROM river_job`)
	stored, err := s.receive(ctx, s.deliveries[i], "pull_request", "{}")
	if err == nil {
		err = s.routeEvents(ctx)
	}
	s.logf("redeliver %d: stored=%v", i+1, stored)
	if err != nil || stored || s.count(`SELECT count(*) FROM river_job`) != total {
		s.fatalf("redelivery of delivery %d stored=%v, enqueued %d jobs: %v", i+1, stored, s.count(`SELECT count(*) FROM river_job`)-total, err)
	}
}

// reroute is an event job worked again, as when its runner died after
// routing committed but before River recorded the job done: routing marked
// the event processed in its transaction, so nothing is enqueued again.
func (s *sim) reroute() {
	s.t.Helper()
	n := s.count(`SELECT count(*) FROM river_job WHERE kind = 'gh_event' AND state = 'completed'`)
	if n == 0 {
		s.logf("reroute: no event routed yet")
		return
	}
	var id int64
	ctx := s.t.Context()
	if err := s.st.Pool.QueryRow(ctx, `SELECT id FROM river_job WHERE kind = 'gh_event' AND state = 'completed' ORDER BY id OFFSET $1 LIMIT 1`, s.rnd.IntN(n)).Scan(&id); err != nil {
		s.fatalf("pick event job: %v", err)
	}
	total := s.reviewJobs()
	s.exec(`UPDATE river_job SET state = 'running', attempt = attempt + 1, finalized_at = NULL WHERE id = $1`, id)
	if err := s.workEvent(ctx, id); err != nil {
		s.fatalf("reroute: %v", err)
	}
	s.logf("reroute: event job %d", id)
	if n := s.reviewJobs() - total; n != 0 {
		s.fatalf("event job %d worked again enqueued %d review jobs", id, n)
	}
}

// rerun is a person pressing Re-run on the head's check run: routing
// enqueues the next attempt after an incomplete review (the first with
// none), and nothing after any other.
func (s *sim) rerun() {
	s.t.Helper()
	ctx := s.t.Context()
	want, status := args(s.e), "none"
	var attempt int
	err := s.st.Pool.QueryRow(ctx, `SELECT attempt, status FROM reviews WHERE repo = $1 AND pr_number = $2 AND head_sha = $3 AND caller = 'production'
		ORDER BY attempt DESC LIMIT 1`, want.Repo, prNum, want.HeadSHA).Scan(&attempt, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		want.Attempt = 1
	case err != nil:
		s.fatalf("latest review: %v", err)
	default:
		want.Attempt = attempt + 1
	}
	live, mine, total := s.jobs(want, true), s.jobs(want, false), s.reviewJobs()
	payload := fmt.Sprintf(`{"action": "requested_action", "requested_action": {"identifier": %q}, "installation": {"id": %d},
		"repository": {"full_name": %q}, "check_run": {"name": %q, "head_sha": %q, "pull_requests": [{"number": %d}]}}`,
		gh.RerunAction, inst, s.e.fake.repo, gh.CheckName, want.HeadSHA, prNum)
	if _, err := s.receive(ctx, s.delivery(), "check_run", payload); err != nil {
		s.fatalf("rerun: %v", err)
	}
	if err := s.routeEvents(ctx); err != nil {
		s.fatalf("rerun: %v", err)
	}
	added := s.reviewJobs() - total
	s.logf("rerun: latest review %s, %d review jobs enqueued", status, added)
	expect := 0
	if (status == "none" || status == reviewIncomplete) && live == 0 {
		expect = 1 // River's uniqueness skips one still live
	}
	if added != expect || s.jobs(want, false)-mine != added {
		s.fatalf("Re-run after a %s review enqueued %d jobs (%d live for attempt %d)", status, added, live, want.Attempt)
	}
}

// sweep is the sweeper (SweepWorker): it enqueues the head if no
// production review of it exists.
func (s *sim) sweep() {
	s.t.Helper()
	a := args(s.e)
	reviewed, err := s.st.ReviewedHead(s.t.Context(), queries.ReviewedHeadParams{Repo: a.Repo, PrNumber: prNum, HeadSha: a.HeadSHA})
	if err != nil {
		s.fatalf("reviewed head: %v", err)
	}
	if !reviewed {
		if _, err := s.river.Insert(s.t.Context(), a, nil); err != nil {
			s.fatalf("sweep: %v", err)
		}
	}
	s.logf("sweep: reviewed=%v", reviewed)
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
	cancelled := `SELECT count(*) FROM river_job WHERE kind = 'pr_review' AND state = 'cancelled'`
	before := s.count(cancelled)
	if err := s.deliverHead(s.t.Context(), s.delivery(), "synchronize", head); err != nil {
		s.fatalf("route push: %v", err)
	}
	s.e.head = head
	s.heads++
	s.logf("push: head %d, %d jobs cancelled", s.heads, s.count(cancelled)-before)
	if n := s.count(`SELECT count(*) FROM river_job WHERE kind = 'pr_review'
		AND state NOT IN ('completed', 'cancelled', 'discarded') AND args->>'head_sha' <> $1`, head); n != 0 {
		s.fatalf("%d jobs of an old head still open after the push", n)
	}
}

// work is River running the next available job once. With chaos, it is
// killed at a seeded point (on its last attempt too: the job is then
// discarded), or a push to a newer head lands in it, or its model calls
// fail (the review ends incomplete).
func (s *sim) work(chaos bool) bool {
	job, ok := s.claimJob()
	if !ok {
		s.logf("work: nothing to run")
		return false
	}
	s.workJob(job, chaos)
	return true
}

// claimJob is River's producer taking the next available review job.
func (s *sim) claimJob() (*river.Job[store.PRReviewArgs], bool) {
	s.t.Helper()
	ctx := s.t.Context()
	var id int64
	err := s.st.Pool.QueryRow(ctx, `UPDATE river_job SET state = 'running', attempt = attempt + 1, attempted_at = now()
		WHERE id = (SELECT id FROM river_job WHERE kind = 'pr_review' AND state IN ('available', 'retryable') ORDER BY id LIMIT 1)
		RETURNING id`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		s.fatalf("claim job: %v", err)
	}
	row, err := s.river.JobGet(ctx, id)
	if err != nil {
		s.fatalf("job %d: %v", id, err)
	}
	job := &river.Job[store.PRReviewArgs]{JobRow: row}
	if err := json.Unmarshal(row.EncodedArgs, &job.Args); err != nil {
		s.fatalf("job %d args: %v", id, err)
	}
	return job, true
}

// workJob runs claimed job to its end and settles it.
func (s *sim) workJob(job *river.Job[store.PRReviewArgs], chaos bool) {
	row, id := job.JobRow, job.ID
	var kill *simKill
	var push *simPush
	outage := false
	switch {
	case chaos && s.rnd.IntN(2) == 0:
		k := simKills[s.rnd.IntN(len(simKills))]
		k.hard = k.db || s.rnd.IntN(2) == 0
		kill = &k
	case chaos && job.Args.HeadSHA == s.e.head && s.rnd.IntN(2) == 0:
		push = &simPushes[s.rnd.IntN(len(simPushes))]
		s.newHead, s.pushID = s.e.commit(s.t, nil), s.delivery()
	case chaos && job.Args.HeadSHA == s.e.head && s.rnd.IntN(3) == 0:
		outage = true
	}
	s.outage.Store(outage)
	defer s.outage.Store(false)
	ctx, cancel, timeout := s.jobContext(job)
	defer cancel(nil)
	defer timeout()
	s.e.fake.mu.Lock()
	s.killAt, s.killed = kill, false
	s.pushAt, s.pushed, s.started, s.cancelRun = push, false, false, cancel
	before, posts, incomplete := supersededChecks(s.e.fake), len(s.e.fake.reviews), incompleteChecks(s.e.fake)
	s.e.fake.mu.Unlock()
	werr := s.d.Work(ctx, job)
	s.unstalled(ctx, job)
	state := s.settle(row, werr)
	at := "none"
	switch {
	case kill != nil:
		at = "kill " + kill.String()
	case push != nil:
		at = "push " + push.String()
	case outage:
		at = "outage"
	}
	s.logf("work: job %d attempt %d chaos=%s -> %s", id, row.Attempt, at, state)
	switch {
	case push != nil:
		s.superseded(*push, job.Args.HeadSHA, state, before)
	case outage:
		s.incomplete(job.Args, state, posts, incomplete)
	}
	if state == "discarded" {
		s.abandoned(job.Args)
	}
}

// overlap is two runners at once. The next job runs until a seeded
// GitHub call and waits there, holding its repo slot and its head's lock,
// while the job after it runs to its end; then the first goes on. The
// second is snoozed, without spending an attempt or getting a claim, at a
// repo limit of 1 or when it reviews the same head; otherwise both review
// at once. Neither run has other chaos.
func (s *sim) overlap() {
	s.t.Helper()
	a, ok := s.claimJob()
	if !ok {
		s.logf("overlap: nothing to run")
		return
	}
	at := simParks[s.rnd.IntN(len(simParks))]
	p := s.parkAt(at)
	defer p.disarm()
	s.calm()
	ctx, cancel, timeout := s.jobContext(a)
	defer cancel(nil)
	defer timeout()
	done := make(chan error, 1)
	go func() { done <- s.d.Work(ctx, a) }()
	select {
	case err := <-done:
		p.disarm()
		s.logf("overlap: job %d attempt %d finished before %s -> %s", a.ID, a.Attempt, at, s.settle(a.JobRow, err))
		return
	case <-p.parked:
	}
	b, ok := s.claimJob()
	if !ok {
		close(p.release)
		s.logf("overlap: job %d parked at %s, nothing else to run -> %s", a.ID, at, s.settle(a.JobRow, <-done))
		return
	}
	var claimsBefore int
	claims := `SELECT count(*) FROM runner_claims WHERE job_id = $1`
	claimsBefore = s.count(claims, b.ID)
	berr := s.d.Work(ctx, b)
	bstate := s.settle(b.JobRow, berr)
	close(p.release)
	aerr := <-done
	s.unstalled(ctx, a)
	astate := s.settle(a.JobRow, aerr)
	s.logf("overlap: limit %d, job %d (args attempt %d) parked at %s -> %s; job %d (args attempt %d, unique %v) -> %s",
		s.limit, a.ID, a.Args.Attempt, at, astate, b.ID, b.Args.Attempt, s.unique(b.ID), bstate)
	var snooze *rivertype.JobSnoozeError
	snoozed := errors.As(berr, &snooze)
	sameHead := a.Args.Repo == b.Args.Repo && a.Args.PR == b.Args.PR && a.Args.HeadSHA == b.Args.HeadSHA
	if wait := s.limit == 1 || sameHead; snoozed != wait || snoozed && s.count(claims, b.ID) != claimsBefore {
		s.fatalf("at limit %d, job %d (same head %v) ran beside job %d: snoozed %v, %v", s.limit, b.ID, sameHead, a.ID, snoozed, berr)
	}
	if snoozed && s.count(`SELECT attempt FROM river_job WHERE id = $1`, b.ID) != b.Attempt-1 {
		s.fatalf("snoozed job %d spent an attempt", b.ID)
	}
}

// simPark is a request parked by parkAt.
type simPark struct {
	parked, release chan struct{}
	// drop, set before release, drops the call (a dropped connection);
	// otherwise GitHub serves it.
	drop   atomic.Bool
	disarm func()
}

// parkAt parks the next request at route (get-pr: the pre-post read,
// after the check run started), before the fake takes its lock, until
// release is closed.
func (s *sim) parkAt(route string) *simPark {
	f := s.e.fake
	p := &simPark{parked: make(chan struct{}), release: make(chan struct{})}
	var armed, started atomic.Bool
	armed.Store(true)
	hook := func(r *http.Request) {
		at := simRoute(f, r)
		if at == "start-check" {
			defer started.Store(true)
		}
		if at != route || route == "get-pr" && !started.Load() || !armed.CompareAndSwap(true, false) {
			return
		}
		close(p.parked)
		<-p.release
		if p.drop.Load() {
			panic(http.ErrAbortHandler)
		}
	}
	f.park.Store(&hook)
	p.disarm = func() {
		armed.Store(false)
		f.park.Store(nil)
	}
	return p
}

// calm clears the run state no chaos is armed in.
func (s *sim) calm() {
	s.e.fake.mu.Lock()
	defer s.e.fake.mu.Unlock()
	s.killAt, s.killed, s.pushAt, s.pushed, s.started = nil, false, nil, false, false
}

// jobContext is the context River's executor gives Work: cancellable with
// a cause (a remote cancel), under the job's timeout. cancel and timeout
// must both be called.
func (s *sim) jobContext(job *river.Job[store.PRReviewArgs]) (context.Context, context.CancelCauseFunc, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(s.t.Context())
	ctx, timeout := context.WithTimeout(ctx, s.d.Timeout(job))
	return ctx, cancel, timeout
}

// unstalled fails a run that reached its job timeout with nothing set to
// stall it: once time can move, a deadlock looks like a timeout.
func (s *sim) unstalled(ctx context.Context, job *river.Job[store.PRReviewArgs]) {
	s.t.Helper()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.fatalf("job %d attempt %d ran into its %s timeout with nothing stalling it", job.ID, job.Attempt, s.d.Timeout(job))
	}
}

// stall is GitHub hanging at a seeded call of a run, past the client's
// timeout, so the run gives the call up; then GitHub applies it late (a
// post may land after the runner moved on) or drops it. The run has no
// other chaos.
func (s *sim) stall() {
	s.t.Helper()
	job, ok := s.claimJob()
	if !ok {
		s.logf("stall: nothing to run")
		return
	}
	at := simStalls[s.rnd.IntN(len(simStalls))]
	hang := time.Duration(1+s.rnd.IntN(10)) * time.Minute
	drop := s.rnd.IntN(2) == 0
	p := s.parkAt(at)
	defer p.disarm()
	s.calm()
	ctx, cancel, timeout := s.jobContext(job)
	defer cancel(nil)
	defer timeout()
	done := make(chan error, 1)
	go func() { done <- s.d.Work(ctx, job) }()
	select {
	case err := <-done:
		p.disarm()
		s.logf("stall: job %d attempt %d finished before %s -> %s", job.ID, job.Attempt, at, s.settle(job.JobRow, err))
		return
	case <-p.parked:
	}
	time.Sleep(hang)
	p.drop.Store(drop)
	close(p.release)
	err := <-done
	// The late call has landed or dropped before anything else runs.
	s.e.fake.transport.running.Wait()
	s.unstalled(ctx, job)
	then := "applied"
	if drop {
		then = "dropped"
	}
	s.logf("stall: job %d attempt %d: GitHub hung %s at %s, then %s -> %s", job.ID, job.Attempt, hang, at, then, s.settle(job.JobRow, err))
}

// late is a runner that starts late, its pod pending: in time, or past
// the job's timeout, when River has given up on the run and the runner
// must do nothing.
func (s *sim) late() {
	s.t.Helper()
	job, ok := s.claimJob()
	if !ok {
		s.logf("late: nothing to run")
		return
	}
	by, past := 5*time.Minute, s.rnd.IntN(2) == 0
	if past {
		by = s.d.Timeout(job) + time.Duration(1+s.rnd.IntN(5))*time.Minute
	}
	s.calm()
	ctx, cancel, timeout := s.jobContext(job)
	defer cancel(nil)
	defer timeout()
	f := s.e.fake
	f.mu.Lock()
	s.runCalls = 0
	f.mu.Unlock()
	s.lateBy = by
	err := s.d.Work(ctx, job)
	f.mu.Lock()
	calls := s.runCalls
	f.mu.Unlock()
	state := s.settle(job.JobRow, err)
	s.logf("late: job %d attempt %d, runner %s late -> %s, the runner made %d GitHub calls", job.ID, job.Attempt, by, state, calls)
	if timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded); timedOut != past || past && calls != 0 {
		s.fatalf("runner %s late (timeout %s): timed out %v, %d GitHub calls", by, s.d.Timeout(job), timedOut, calls)
	}
}

// unique reports whether River's uniqueness covers job id (a duplicate()
// row is not).
func (s *sim) unique(id int64) bool {
	s.t.Helper()
	return s.count(`SELECT count(*) FROM river_job WHERE id = $1 AND unique_key IS NOT NULL`, id) == 1
}

// abandoned checks a job River discarded: its review, if it has one, was
// recorded incomplete (so a Re-run re-runs it) unless superseded, and none
// of its check runs is left in progress.
func (s *sim) abandoned(a store.PRReviewArgs) {
	s.t.Helper()
	var id int64
	var status string
	err := s.st.Pool.QueryRow(s.t.Context(), `SELECT id, status FROM reviews WHERE repo = $1 AND pr_number = $2 AND head_sha = $3
		AND attempt = $4 AND caller = 'production' ORDER BY id DESC LIMIT 1`, a.Repo, prNum, a.HeadSHA, max(a.Attempt, 1)).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		s.logf("abandoned: no review")
		return
	}
	if err != nil {
		s.fatalf("abandoned: review row: %v", err)
	}
	f := s.e.fake
	f.mu.Lock()
	open := 0
	for i, c := range f.checks {
		if c["external_id"] == pipeline.CheckExternalID(id) && !f.checksDone[i+1] {
			open++
		}
	}
	f.mu.Unlock()
	s.logf("abandoned: review %s, %d check runs in progress", status, open)
	if status != reviewIncomplete && status != "superseded" || open != 0 {
		s.fatalf("discarded job left its review %s with %d check runs in progress", status, open)
	}
}

// incomplete checks a run whose model calls all failed: the job completed,
// its review row is incomplete, nothing was posted, and its check run says
// the review did not complete (so a person can Re-run it).
func (s *sim) incomplete(a store.PRReviewArgs, state string, posts, checks int) {
	s.t.Helper()
	s.e.fake.mu.Lock()
	posted, said := len(s.e.fake.reviews)-posts, incompleteChecks(s.e.fake)-checks
	s.e.fake.mu.Unlock()
	var status string
	if err := s.st.Pool.QueryRow(s.t.Context(), `SELECT status FROM reviews WHERE repo = $1 AND pr_number = $2 AND head_sha = $3
		AND attempt = $4 AND caller = 'production'`, a.Repo, prNum, a.HeadSHA, max(a.Attempt, 1)).Scan(&status); err != nil {
		s.fatalf("outage: review row: %v", err)
	}
	if state != "completed" || status != reviewIncomplete || posted != 0 || said != 1 {
		s.fatalf("outage: job %s, review %s, %d posts, %d incomplete checks; want completed, incomplete, 0, 1", state, status, posted, said)
	}
}

// incompleteChecks counts check-run updates saying a review did not
// complete; f.mu is held.
func incompleteChecks(f *fakeGitHub) int {
	n := 0
	for _, u := range f.checkUpdates {
		if out, _ := u["output"].(map[string]any); out["title"] == "Review did not complete" {
			n++
		}
	}
	return n
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

// simSettle records a job's end state, as River's completer.
const simSettle = `UPDATE river_job SET state = $2::river_job_state,
	finalized_at = CASE WHEN $2 IN ('completed', 'cancelled', 'discarded') THEN now() END WHERE id = $1`

// settle records Work's result as River's executor would.
func (s *sim) settle(row *rivertype.JobRow, err error) string {
	s.t.Helper()
	state := settleState(row, err)
	s.exec(simSettle, row.ID, state)
	// A snooze gives the attempt back and counts itself. River schedules
	// the job SlotWait ahead; the sim has no time, so it is available now.
	if _, ok := errors.AsType[*rivertype.JobSnoozeError](err); ok {
		s.exec(`UPDATE river_job SET attempt = attempt - 1,
			metadata = jsonb_set(metadata, '{snoozes}', to_jsonb(coalesce((metadata->>'snoozes')::int, 0) + 1)) WHERE id = $1`, row.ID)
	}
	return state
}

// settleState is the state River's executor gives a job Work returned
// err for.
func settleState(row *rivertype.JobRow, err error) string {
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
	return state
}

// replay is a zombie runner: one left over from an earlier dispatch
// redeems its old nonce while its job runs again (or, for a job River
// cancelled or discarded, which a newer one may share a unique key with,
// while it stays settled). It must get nothing.
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
	if state != "cancelled" && state != "discarded" {
		s.exec(`UPDATE river_job SET state = 'running', finalized_at = NULL WHERE id = $1`, task.JobID)
	}
	s.e.fake.mu.Lock()
	before := s.requests
	s.e.fake.mu.Unlock()
	exit := s.runner.Run(ctx, task.JobID, task.ConfigVersion, task.Nonce)
	s.e.fake.mu.Lock()
	calls := s.requests - before
	s.e.fake.mu.Unlock()
	s.exec(simSettle, task.JobID, state)
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
	// A claim lives at least as long as its job may run: a runner River
	// still waits for can always redeem it.
	if n := s.count(`SELECT count(*) FROM runner_claims WHERE expires_at < created_at + make_interval(secs => $1)`, ReviewTimeout.Seconds()); n != 0 {
		s.fatalf("%d claims expire before their job's timeout", n)
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

// run plays the seed's schedule, then the sweeper and a person's Re-run
// of an incomplete review, then drains the queue with no chaos.
func (s *sim) run() {
	s.deliver("opened")
	s.check()
	for s.step = 1; s.step <= simSteps; s.step++ {
		switch p := s.rnd.IntN(100); {
		case p < 12:
			s.deliver("synchronize")
		case p < 17:
			s.redeliver()
		case p < 21:
			s.reroute()
		case p < 28:
			s.rerun()
		case p < 38:
			s.duplicate()
		case p < 46:
			s.push()
		case p < 74:
			s.work(true)
		case p < 80:
			s.overlap()
		case p < 85:
			s.stall()
		case p < 88:
			s.late()
		default:
			s.replay()
		}
		s.check()
	}
	s.sweep()
	// The check run of an incomplete review asks for a Re-run.
	if s.count(`SELECT count(*) FROM reviews WHERE id = (SELECT max(id) FROM reviews WHERE head_sha = $1 AND caller = 'production')
		AND status = $2`, s.e.head, reviewIncomplete) == 1 {
		s.rerun()
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
	a, b := simulate(t, simBusySeed), simulate(t, simBusySeed)
	if !slices.Equal(a, b) {
		t.Errorf("seed %d diverged:\n%s\n---\n%s", simBusySeed, strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
}

// simulate runs seed in a synctest bubble and returns its trace. The
// bubble's clock starts in 2000; it is slept to the wall clock first, so
// it never runs behind Postgres's: claim expiry is set from time.Now and
// checked against the later of the two clocks. GitHub is served in
// memory, so the clock moves whenever every goroutine waits (a stall, a
// late runner, a client or job timeout).
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
