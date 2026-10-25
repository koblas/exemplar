package propose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// fakeRepo records what the writer does to exemplar-config.
type fakeRepo struct {
	mu       sync.Mutex
	branches map[string]bool
	files    map[string][]byte // branch:path
	prs      map[string]string // branch -> url
	opened   int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{branches: map[string]bool{}, files: map[string][]byte{}, prs: map[string]string{}}
}

func (f *fakeRepo) DefaultBranch(context.Context) (string, string, error) {
	return "main", "abc123", nil
}

func (f *fakeRepo) FindPR(_ context.Context, branch string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.prs[branch]
	return u, ok, nil
}

func (f *fakeRepo) EnsureBranch(_ context.Context, branch, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[branch] = true
	return nil
}

func (f *fakeRepo) PutFile(_ context.Context, branch, path string, content []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[branch+":"+path] = content
	return nil
}

func (f *fakeRepo) OpenDraftPR(_ context.Context, branch, base, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if base != "main" {
		return "", fmt.Errorf("unexpected base %s", base)
	}
	f.opened++
	u := fmt.Sprintf("https://github.com/acme/exemplar-config/pull/%d", f.opened)
	f.prs[branch] = u
	return u, nil
}

func exec(t *testing.T, s *store.Store, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// seed creates a done cluster run and candidates with one source each.
func seed(t *testing.T, s *store.Store, owners map[string]int, title func(i int) string) {
	t.Helper()
	exec(t, s, `INSERT INTO cluster_runs (id, params, status) VALUES (1, '{}', 'done')`)
	i := 0
	for owner, n := range owners {
		for range n {
			i++
			exec(t, s, `INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url, body)
			            VALUES ($1, 'acme/pay', 1, 'svc/a.go', '.go', 'r', 'a', now(), $2, 'b')`,
				i, fmt.Sprintf("https://github.com/acme/pay/pull/1#discussion_r%d", i))
			exec(t, s, `INSERT INTO extractions (comment_id, title, description, category, confidence, extractor_model, extractor_version)
			            VALUES ($1, 't', 'd', 'Reliability', 0.9, 'm', 'v')`, i)
			exec(t, s, `INSERT INTO candidates (id, ext, title, description, cluster_size, distinct_reviewers, score, score_detail, owner, cluster_hash, last_run_id)
			            VALUES ($1, '.go', $2, 'Why it matters.', 1, 1, $3, '{"factors":{}}', $4, $5, 1)`,
				i, title(i), float64(100-i), owner, fmt.Sprintf("h%d", i))
			exec(t, s, `INSERT INTO candidate_sources VALUES ($1, $1)`, i)
		}
	}
}

func status(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	rows, err := s.Pool.Query(t.Context(), `SELECT status, count(*) FROM candidates GROUP BY status`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		_ = rows.Scan(&st, &n)
		out[st] = n
	}
	return out
}

func newWriter(t *testing.T, s *store.Store, repo Repo) *Writer {
	t.Helper()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{}, s, repo, sc)
}

// TestProposeThrottlesPerOwner runs at now, in a synctest bubble slept to it.
func TestProposeThrottlesPerOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(now))
		testProposeThrottlesPerOwner(t)
	})
}

func testProposeThrottlesPerOwner(t *testing.T) {
	t.Helper()
	s := storetest.New(t)
	seed(t, s, map[string]int{"@acme/payments": 5, "@acme/search": 1}, func(i int) string { return fmt.Sprintf("Rule number %d", i) })
	repo := newFakeRepo()
	w := newWriter(t, s, repo)

	outs, err := w.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, o := range outs {
		actions[o.Owner+":"+o.Action]++
	}
	if actions["@acme/payments:opened"] != 3 || actions["@acme/payments:throttled"] != 2 || actions["@acme/search:opened"] != 1 {
		t.Fatalf("actions = %v", actions)
	}
	if got := status(t, s); got["pr_open"] != 4 || got["new"] != 2 {
		t.Fatalf("statuses = %v", got)
	}
	// Each proposal records its branch and rule file, for the promotion sweep.
	var missing int
	if err := s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM candidates WHERE status = 'pr_open'
		AND NOT (config_pr_branch LIKE 'exemplar/candidate-%' AND rule_path = 'rules/reliability/' || rule_id || '.yaml')`).Scan(&missing); err != nil || missing != 0 {
		t.Fatalf("%d proposals without branch and rule path, %v", missing, err)
	}
	// Each PR adds one shadow rule on its own branch.
	for key, data := range repo.files {
		if !strings.HasPrefix(key, "exemplar/candidate-") || !strings.Contains(key, ":rules/reliability/") || !strings.Contains(string(data), "status: shadow") {
			t.Errorf("unexpected file %s:\n%s", key, data)
		}
	}

	// A second run opens nothing: payments is at its cap, search has none left.
	before := repo.opened
	if _, err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if repo.opened != before {
		t.Fatalf("second run opened %d more PRs", repo.opened-before)
	}

	// Once one payments PR is resolved (promoted), the next candidate goes out.
	exec(t, s, `UPDATE candidates SET status = 'promoted' WHERE id = (SELECT min(id) FROM candidates WHERE owner = '@acme/payments' AND status = 'pr_open')`)
	if _, err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if repo.opened != before+1 {
		t.Fatalf("after a slot freed, opened %d, want 1", repo.opened-before)
	}
}

// TestProposeRecoversExistingPR runs at now, in a synctest bubble slept to it.
func TestProposeRecoversExistingPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(now))
		testProposeRecoversExistingPR(t)
	})
}

func testProposeRecoversExistingPR(t *testing.T) {
	t.Helper()
	s := storetest.New(t)
	seed(t, s, map[string]int{"@acme/payments": 1}, func(int) string { return "Recovered rule" })
	repo := newFakeRepo()
	repo.prs["exemplar/candidate-1"] = "https://github.com/acme/exemplar-config/pull/99" // a crashed run's PR
	outs, err := newWriter(t, s, repo).Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Action != "recovered" || outs[0].URL != "https://github.com/acme/exemplar-config/pull/99" || repo.opened != 0 {
		t.Fatalf("outs = %+v, opened %d", outs, repo.opened)
	}
	var url string
	if err := s.Pool.QueryRow(t.Context(), `SELECT config_pr_url FROM candidates WHERE id = 1`).Scan(&url); err != nil || !strings.HasSuffix(url, "/99") {
		t.Fatalf("recorded url = %q, %v", url, err)
	}
}

// TestProposeRefusesSecretsAndDryRunWritesNothing runs at now, in a synctest bubble slept to it.
func TestProposeRefusesSecretsAndDryRunWritesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(now))
		testProposeRefusesSecretsAndDryRunWritesNothing(t)
	})
}

func testProposeRefusesSecretsAndDryRunWritesNothing(t *testing.T) {
	t.Helper()
	s := storetest.New(t)
	sum := sha256.Sum256([]byte("propose test token"))
	secret := "ghp_" + hex.EncodeToString(sum[:])[:36]
	seed(t, s, map[string]int{"@acme/payments": 2}, func(i int) string {
		if i == 1 {
			return "Rotate " + secret
		}
		return "Clean rule"
	})
	outs, err := newWriter(t, s, nil).Run(t.Context()) // dry run
	if err != nil {
		t.Fatal(err)
	}
	if outs[0].Action != "skipped" || !strings.Contains(outs[0].Reason, "secret") || outs[1].Action != "drafted" {
		t.Fatalf("outs = %+v", outs)
	}
	if got := status(t, s); got["new"] != 2 {
		t.Fatalf("dry run changed candidates: %v", got)
	}
}
