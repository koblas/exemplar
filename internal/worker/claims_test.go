package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
)

// fakeMinter mints "tok-<flavor>-<repos>" tokens and records each request.
type fakeMinter struct {
	mu      sync.Mutex
	minted  []string
	refuse  bool
	expires time.Time
}

func (m *fakeMinter) Token(_ context.Context, inst int64, f gh.Flavor, repos []string) (*github.InstallationToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refuse {
		return nil, gh.ErrTokenScope
	}
	name := fmt.Sprintf("%d/%s/%s", inst, f, strings.Join(repos, ","))
	m.minted = append(m.minted, name)
	exp := m.expires
	if exp.IsZero() {
		exp = time.Now().Add(time.Hour)
	}
	return &github.InstallationToken{Token: new("tok-" + name), ExpiresAt: &github.Timestamp{Time: exp}}, nil
}

func newClaims(t *testing.T, st *store.Store) *Claims {
	t.Helper()
	c, err := NewClaims(st, []byte("test claim key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := PutSnapshot(t.Context(), st, exampleSnapshot(t)); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaimsRedeemOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := storetest.New(t)
	c := newClaims(t, st)
	grant := Grant{Tokens: []GrantToken{{Repos: []string{repo}, Installation: inst, Flavor: "worker", Token: "ghs_secret", ExpiresAt: time.Now().Add(time.Hour)}}}
	task := Task{JobID: 1, Attempt: 1, ConfigVersion: "cfg-test"}
	nonce, err := c.Mint(ctx, task, grant)
	if err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := st.Pool.QueryRow(ctx, `SELECT tokens FROM runner_claims WHERE job_id = 1`).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("ghs_secret")) {
		t.Fatalf("stored tokens in clear: %v", err)
	}
	if _, _, err := c.Redeem(ctx, 1, "wrong"); !errors.Is(err, errClaimRefused) {
		t.Errorf("wrong nonce = %v", err)
	}
	// Another job's nonce redeems nothing of this one, and the reverse.
	other, err := c.Mint(ctx, Task{JobID: 2, Attempt: 1, ConfigVersion: "cfg-test"}, grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Redeem(ctx, 1, other); !errors.Is(err, errClaimRefused) {
		t.Errorf("job 2's nonce on job 1 = %v", err)
	}
	got, version, err := c.Redeem(ctx, 1, nonce)
	if err != nil || version != "cfg-test" || len(got.Tokens) != 1 || got.Tokens[0].Token != "ghs_secret" {
		t.Fatalf("redeem = %+v %s %v", got, version, err)
	}
	if _, _, err := c.Redeem(ctx, 1, nonce); !errors.Is(err, errClaimRefused) {
		t.Errorf("second redeem = %v", err)
	}
	// Released: no token is left in the row.
	if err := c.Release(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT tokens FROM runner_claims WHERE job_id = 1`).Scan(&sealed); err != nil || sealed != nil {
		t.Errorf("released tokens = %v, %v", sealed, err)
	}
	// A retry's claim replaces the last attempt's nonce.
	retry, err := c.Mint(ctx, Task{JobID: 2, Attempt: 2, ConfigVersion: "cfg-test"}, grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Redeem(ctx, 2, other); !errors.Is(err, errClaimRefused) {
		t.Errorf("the last attempt's nonce = %v", err)
	}
	if _, _, err := c.Redeem(ctx, 2, retry); err != nil {
		t.Errorf("retry's nonce = %v", err)
	}
	// Tokens past their expiry are not handed out.
	expired := Grant{Tokens: []GrantToken{{Repos: []string{repo}, Installation: inst, Flavor: "worker", Token: "t", ExpiresAt: time.Now().Add(-time.Minute)}}}
	n3, err := c.Mint(ctx, Task{JobID: 3, Attempt: 1, ConfigVersion: "cfg-test"}, expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Redeem(ctx, 3, n3); !errors.Is(err, errClaimRefused) {
		t.Errorf("expired claim = %v", err)
	}
	// A claim sealed for one row does not open on another.
	if _, err := st.Pool.Exec(ctx, `UPDATE runner_claims SET tokens = (SELECT tokens FROM runner_claims WHERE job_id = 2), claimed_at = NULL WHERE job_id = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE runner_claims SET expires_at = now() + interval '1 hour' WHERE job_id = 3`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Redeem(ctx, 3, n3); !errors.Is(err, errClaimSealed) {
		t.Errorf("moved grant = %v", err)
	}
	// Retention drops claims whose tokens expired unreleased (job 2,
	// redeemed but never released) and keeps the rest for a day.
	if _, err := st.Pool.Exec(ctx, `UPDATE runner_claims SET expires_at = now() - interval '1 minute' WHERE job_id = 2`); err != nil {
		t.Fatal(err)
	}
	n, err := st.ClearExpiredRunnerClaims(ctx, queries.ClearExpiredRunnerClaimsParams{
		Cutoff: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true}, Batch: 10,
	})
	var left int
	if err == nil {
		err = st.Pool.QueryRow(ctx, `SELECT count(*) FROM runner_claims`).Scan(&left)
	}
	if err != nil || n != 1 || left != 2 {
		t.Errorf("retention cleared %d, left %d, %v", n, left, err)
	}
}

// A runner gets clients for exactly what its claim grants.
func TestClaimClientsScope(t *testing.T) {
	t.Parallel()
	exp := time.Now().Add(time.Hour)
	c := ClaimClients{Grant: Grant{Tokens: []GrantToken{
		{Repos: []string{"acme/app"}, Installation: 1, Flavor: "worker", Token: "w", ExpiresAt: exp},
		{Repos: []string{"acme/app"}, Installation: 1, Flavor: "gate", Token: "g", ExpiresAt: exp},
		{Repos: []string{"acme/lib", "acme/web"}, Installation: 1, Flavor: "eval", Token: "e", ExpiresAt: exp},
	}}}
	ctx := t.Context()
	if tok, err := c.CloneToken(ctx, 1, "ACME/app"); err != nil || tok != "w" {
		t.Errorf("clone acme/app = %q, %v", tok, err)
	}
	if _, err := c.Gate(ctx, 1, "acme/app"); err != nil {
		t.Errorf("gate = %v", err)
	}
	// Mode eval's read-only token serves reads and clones of its repos.
	if tok, err := c.CloneToken(ctx, 1, "acme/web"); err != nil || tok != "e" {
		t.Errorf("clone acme/web = %q, %v", tok, err)
	}
	for name, call := range map[string]func() error{
		"poster not granted":   func() error { _, err := c.Poster(ctx, 1, "acme/app"); return err },
		"another repo":         func() error { _, err := c.Reader(ctx, 1, "acme/other"); return err },
		"another installation": func() error { _, err := c.Reader(ctx, 2, "acme/app"); return err },
		"gate on an eval repo": func() error { _, err := c.Gate(ctx, 1, "acme/web"); return err },
		"poster on eval repo":  func() error { _, err := c.Poster(ctx, 1, "acme/lib"); return err },
	} {
		if err := call(); !errors.Is(err, errNotClaimed) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func flavors(g Grant) []string {
	out := make([]string, 0, len(g.Tokens))
	for _, t := range g.Tokens {
		out = append(out, t.Flavor+":"+strings.Join(t.Repos, ","))
	}
	slices.Sort(out)
	return out
}

func TestGrants(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := storetest.New(t)
	snap := exampleSnapshot(t)
	snap.Eligibility.Denylist = []schema.DenylistEntry{{Repo: "acme/secret", Reason: "test"}}
	// Opted in (repos/<org>/<repo>.yaml); acme/secret is also denylisted.
	for _, r := range []string{"acme/app", "acme/web", "acme/secret", "other/lib"} {
		snap.Repos[r] = schema.RepoConfig{Enabled: true}
	}
	m := &fakeMinter{}
	g := Granter{Minter: m, Store: st, ConfigRepo: "acme/exemplar-config"}
	for r, i := range map[string]int64{"acme/app": 7, "acme/web": 7, "acme/secret": 7, "acme/exemplar-config": 7, "other/lib": 9} {
		if _, err := st.Pool.Exec(ctx, `INSERT INTO gh_events (delivery_id, event, installation_id, repo_full_name) VALUES (gen_random_uuid(), 'pull_request', $1, $2)`, i, r); err != nil {
			t.Fatal(err)
		}
	}

	review, err := g.Review(ctx, store.PRReviewArgs{Repo: "acme/app", InstallationID: 7, PR: 1, HeadSHA: "h"}, snap)
	if want := []string{"gate:acme/app", "poster:acme/app", "worker:acme/app"}; err != nil || !slices.Equal(flavors(review), want) {
		t.Errorf("review grant = %v, %v", flavors(review), err)
	}
	if _, err := g.Review(ctx, store.PRReviewArgs{Repo: "acme/app"}, snap); !errors.Is(err, errBadJob) {
		t.Errorf("review without installation = %v", err)
	}

	pid, err := st.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: "alice", Repo: "acme/web", PrNumber: 1, RuleID: "r", RuleYaml: "x"})
	if err != nil {
		t.Fatal(err)
	}
	play, err := g.Playground(ctx, store.PlaygroundArgs{ID: pid}, snap)
	if want := []string{"worker:acme/web"}; err != nil || !slices.Equal(flavors(play), want) {
		t.Errorf("playground grant = %v, %v", flavors(play), err)
	}
	var cancel *rivertype.JobCancelError
	if _, err := g.Playground(ctx, store.PlaygroundArgs{ID: pid + 100}, snap); !errors.As(err, &cancel) {
		t.Errorf("vanished playground run = %v", err)
	}

	evaluation := func(repo, mode string) string {
		t.Helper()
		head := "h"
		id, err := st.CreateEvaluation(ctx, queries.CreateEvaluationParams{
			Repo: repo, BaseSha: "b", RequestedBy: "alice", Caller: "cli", Mode: mode, HeadSha: &head, Rules: []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return id.String()
	}
	ev, err := g.Evaluate(ctx, store.EvaluateArgs{ID: evaluation("acme/app", evaluate.ModeReview)}, snap)
	if want := []string{"worker:acme/app"}; err != nil || !slices.Equal(flavors(ev), want) {
		t.Errorf("evaluate grant = %v, %v", flavors(ev), err)
	}
	// An ineligible repo gets nothing; the runner refuses it.
	if ev, err := g.Evaluate(ctx, store.EvaluateArgs{ID: evaluation("acme/secret", evaluate.ModeReview)}, snap); err != nil || len(ev.Tokens) != 0 {
		t.Errorf("ineligible evaluate grant = %v, %v", flavors(ev), err)
	}
	// Mode eval: exemplar-config, and one read-only token per installation
	// over its eligible repos.
	me, err := g.Evaluate(ctx, store.EvaluateArgs{ID: evaluation("acme/exemplar-config", evaluate.ModeEval)}, snap)
	if want := []string{"eval:acme/app,acme/web", "eval:other/lib", "worker:acme/exemplar-config"}; err != nil || !slices.Equal(flavors(me), want) {
		t.Errorf("mode eval grant = %v, %v", flavors(me), err)
	}
	// Mode eval on another repo gets nothing.
	if me, err := g.Evaluate(ctx, store.EvaluateArgs{ID: evaluation("acme/app", evaluate.ModeEval)}, snap); err != nil || len(me.Tokens) != 0 {
		t.Errorf("mode eval elsewhere = %v, %v", flavors(me), err)
	}

	m.refuse = true
	if _, err := g.Review(ctx, store.PRReviewArgs{Repo: "acme/app", InstallationID: 7}, snap); !errors.Is(err, gh.ErrTokenScope) {
		t.Errorf("refused mint = %v", err)
	}
}

// The dispatcher mints the job's claim, the in-process runner redeems it,
// and posts through its claim's clients; afterwards no token is left.
func TestRiverDispatchWithClaims(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	claims := newClaims(t, st)
	runner := &Runner{Config: e.w.cfg, Snapshot: Snapshots(st, e.w.cfg.Snapshot), Claims: claims, BaseURL: e.fake.srv.URL + "/"}
	runner.Config.GitHub = nil // only the claim's clients
	minter := &fakeMinter{}
	d, err := NewReviewDispatcher(DispatchConfig{
		Launcher: InProcess{Runner: runner}, Snapshot: e.w.cfg.Snapshot, Store: st,
		Claims: claims, Granter: &Granter{Minter: minter, Store: st},
	})
	if err != nil {
		t.Fatal(err)
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, d)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{store.ReviewQueue: {MaxWorkers: 1}}, Workers: workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.Jobs = client
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(context.WithoutCancel(t.Context())) }()
	if _, err := client.Insert(t.Context(), args(e), nil); err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Kind != river.EventKindJobCompleted {
		t.Fatalf("job = %s", ev.Kind)
	}
	if len(e.fake.reviews) != 1 || len(minter.minted) != 3 {
		t.Errorf("posted %d, minted %v", len(e.fake.reviews), minter.minted)
	}
	var live int
	if err := st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM runner_claims WHERE tokens IS NOT NULL OR released_at IS NULL OR claimed_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Errorf("claims left with tokens or unclaimed = %d, %v", live, err)
	}
}
