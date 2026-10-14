package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

// upstream is a local repo standing in for GitHub.
type upstream struct {
	t   *testing.T
	dir string
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{t: t, dir: filepath.Join(t.TempDir(), "up")}
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	u.git("init", "-q", "-b", "main")
	return u
}

func (u *upstream) git(args ...string) string {
	u.t.Helper()
	cmd := exec.CommandContext(u.t.Context(), "git", append([]string{"-C", u.dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + u.t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.invalid", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.invalid",
		"GIT_AUTHOR_DATE=2026-09-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-09-01T00:00:00Z"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		u.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (u *upstream) commit(file, body, msg string) string {
	u.t.Helper()
	p := filepath.Join(u.dir, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		u.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		u.t.Fatal(err)
	}
	u.git("add", "-A")
	u.git("commit", "-q", "-m", msg)
	return u.git("rev-parse", "HEAD")
}

func newManager(t *testing.T, u *upstream, creds Credentials) *Manager {
	t.Helper()
	m, err := New(Config{Root: t.TempDir(), URL: func(string) string { return u.dir }, Credentials: creds})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fakeToken() string { return "ghs_" + strings.Repeat("x7", 18) }

func TestFetchAndSnapshot(t *testing.T) {
	u := newUpstream(t)
	first := u.commit("svc/a.go", "package svc\n", "Add a")
	var calls atomic.Int32
	m := newManager(t, u, func(context.Context, string) (string, string, error) {
		calls.Add(1)
		return fakeToken(), "public", nil
	})
	ctx := t.Context()
	if _, err := m.Snapshot(ctx, "acme/app"); !errors.Is(err, ErrNotMirrored) {
		t.Fatalf("snapshot before fetch = %v", err)
	}
	sha, err := m.Fetch(ctx, "Acme/App")
	if err != nil || sha != first {
		t.Fatalf("Fetch = %s, %v; want %s", sha, err, first)
	}
	dir, _ := m.Dir("acme/app")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Fatalf("mirror not at %s: %v", dir, err)
	}
	if v, _ := m.Visibility(ctx, "acme/app"); v != "public" {
		t.Errorf("visibility = %q", v)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config"))
	if strings.Contains(string(cfg), fakeToken()) {
		t.Fatal("token written to the mirror's config")
	}

	second := u.commit("svc/b.go", "package svc\n", "Add b")
	if sha, err := m.Fetch(ctx, "acme/app"); err != nil || sha != second {
		t.Fatalf("refetch = %s, %v; want %s", sha, err, second)
	}
	// The default branch moving upstream moves the snapshot.
	u.git("checkout", "-q", "-b", "trunk")
	third := u.commit("svc/c.go", "package svc\n", "Add c")
	u.git("symbolic-ref", "HEAD", "refs/heads/trunk")
	if sha, err := m.Fetch(ctx, "acme/app"); err != nil || sha != third {
		t.Fatalf("after default-branch change = %s, %v; want %s", sha, err, third)
	}
	if calls.Load() != 3 {
		t.Errorf("credentials minted %d times, want once per fetch", calls.Load())
	}
}

func TestFailedFirstFetchLeavesNothing(t *testing.T) {
	m, err := New(Config{Root: t.TempDir(), URL: func(string) string { return "/nonexistent/repo" }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fetch(t.Context(), "acme/app"); err == nil {
		t.Fatal("fetch from a missing remote succeeded")
	}
	if _, err := m.Snapshot(t.Context(), "acme/app"); !errors.Is(err, ErrNotMirrored) {
		t.Fatalf("half-made mirror is serving: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(m.cfg.Root, "acme"))
	if len(entries) != 0 {
		t.Errorf("leftovers: %v", entries)
	}
}

func TestDir(t *testing.T) {
	m, _ := New(Config{Root: t.TempDir()})
	for _, bad := range []string{"acme", "../x", "acme/..", "acme/a/b", "acme/a b", "/acme/app", ".."} {
		if _, err := m.Dir(bad); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Dir(%q) = %v", bad, err)
		}
	}
	a, _ := m.Dir("Acme/App")
	b, _ := m.Dir("acme/app")
	if a != b {
		t.Errorf("case not normalized: %s vs %s", a, b)
	}
}

func TestReadFileAndLog(t *testing.T) {
	u := newUpstream(t)
	var body strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	c1 := u.commit("svc/big.txt", body.String(), "Add big")
	c2 := u.commit("svc/a.go", "package svc\n// two\n", "Add a")
	u.commit("svc/a.go", "package svc\n// three\n", "Change a")
	m := newManager(t, u, nil)
	ctx := t.Context()
	head, err := m.Fetch(ctx, "acme/app")
	if err != nil {
		t.Fatal(err)
	}

	f, err := m.ReadFile(ctx, "acme/app", head, "svc/big.txt", 10, 1000)
	if err != nil || f.Start != 10 || f.End != 409 || f.Total != 500 || !strings.HasPrefix(f.Text, "line 10\n") || !strings.HasSuffix(f.Text, "line 409\n") {
		t.Fatalf("ReadFile = %+v, %v", f.Start, err)
	}
	if f, err := m.ReadFile(ctx, "acme/app", head, "svc/big.txt", 600, 700); err != nil || f.Text != "" || f.Total != 500 {
		t.Errorf("past EOF = %+v, %v", f, err)
	}
	if f, err := m.ReadFile(ctx, "acme/app", c2, "svc/a.go", 1, 5); err != nil || !strings.Contains(f.Text, "two") || f.Commit != c2 {
		t.Errorf("at old commit = %+v, %v", f, err)
	}
	for name, tc := range map[string]struct {
		commit, path string
		start, end   int
		want         error
	}{
		"bad path":       {head, "../etc/passwd", 1, 2, ErrBadRequest},
		"absolute path":  {head, "/etc/passwd", 1, 2, ErrBadRequest},
		"bad range":      {head, "svc/a.go", 5, 2, ErrBadRequest},
		"not a sha":      {"HEAD", "svc/a.go", 1, 2, ErrBadRequest},
		"unknown commit": {strings.Repeat("a", 40), "svc/a.go", 1, 2, ErrNoCommit},
		"missing file":   {c1, "svc/a.go", 1, 2, ErrNoFile},
		"directory":      {head, "svc", 1, 2, ErrNoFile},
	} {
		if _, err := m.ReadFile(ctx, "acme/app", tc.commit, tc.path, tc.start, tc.end); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := m.ReadFile(ctx, "acme/other", head, "svc/a.go", 1, 2); !errors.Is(err, ErrNotMirrored) {
		t.Errorf("unmirrored repo err = %v", err)
	}

	commits, err := m.Log(ctx, "acme/app", head, "svc/a.go", 50)
	if err != nil || len(commits) != 2 || commits[0].Subject != "Change a" || commits[1].Subject != "Add a" {
		t.Fatalf("Log = %+v, %v", commits, err)
	}
	if commits, err := m.Log(ctx, "acme/app", head, "svc/*.go", 5); err != nil || len(commits) != 0 {
		t.Errorf("glob pathspec matched: %+v, %v", commits, err)
	}
}

func TestScope(t *testing.T) {
	snap := &config.Snapshot{
		Global: schema.GlobalConfig{Enabled: true},
		Repos: map[string]schema.RepoConfig{
			"acme/app":     {Enabled: true, Context: &schema.RepoContext{ExtraRepos: []string{"acme/shared", "acme/secret", "acme/public-lib"}}},
			"acme/off":     {Enabled: false, Context: &schema.RepoContext{ExtraRepos: []string{"acme/never"}}},
			"acme/blocked": {Enabled: true},
		},
		Eligibility: schema.Eligibility{Denylist: []schema.DenylistEntry{{Repo: "acme/secret", Reason: "legal"}, {Repo: "acme/blocked", Reason: "x"}}},
	}
	if got := strings.Join(InScope(snap), ","); got != "acme/app,acme/public-lib,acme/shared" {
		t.Errorf("InScope = %s", got)
	}
	for _, tc := range []struct {
		pr, repo string
		ok       bool
	}{
		{"acme/app", "acme/app", true},
		{"acme/app", "ACME/Shared", true},
		{"acme/app", "acme/secret", false}, // denylisted
		{"acme/app", "acme/other", false},  // not in extra_repos
		{"acme/off", "acme/off", false},    // not enabled
		{"acme/blocked", "acme/blocked", false},
		{"acme/nope", "acme/app", false},
	} {
		if err := Allowed(snap, tc.pr, tc.repo); (err == nil) != tc.ok {
			t.Errorf("Allowed(%s, %s) = %v", tc.pr, tc.repo, err)
		}
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{{"private", "internal", true}, {"public", "public", true}, {"public", "private", false}, {"", "", false}, {"private", "weird", false}} {
		if SameTier(tc.a, tc.b) != tc.want {
			t.Errorf("SameTier(%q, %q) != %v", tc.a, tc.b, tc.want)
		}
	}
}

func TestSchedulerDebounces(t *testing.T) {
	u := newUpstream(t)
	u.commit("a.go", "package a\n", "a")
	var fetches atomic.Int32
	m := newManager(t, u, func(context.Context, string) (string, string, error) {
		fetches.Add(1)
		return "", "private", nil
	})
	s := NewScheduler(m, func() []string { return []string{"acme/app"} }, 300*time.Millisecond, time.Hour, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitFor := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for fetches.Load() < n || s.running("acme/app") {
			if time.Now().After(deadline) {
				t.Fatalf("fetches = %d, want %d", fetches.Load(), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(1) // the initial pass
	// A burst inside the window coalesces into one more fetch.
	for range 5 {
		if !s.Request("acme/app") {
			t.Fatal("in-scope request refused")
		}
	}
	time.Sleep(100 * time.Millisecond)
	if fetches.Load() != 1 {
		t.Fatalf("fetched inside the debounce window: %d", fetches.Load())
	}
	waitFor(2)
	time.Sleep(400 * time.Millisecond)
	if fetches.Load() != 2 {
		t.Errorf("burst produced %d fetches, want 2 in total", fetches.Load())
	}
	if s.Request("acme/other") {
		t.Error("out-of-scope request accepted")
	}
	st := s.Status()
	if len(st) != 1 || !st[0].OK || st[0].SnapshotSHA == "" {
		t.Errorf("status = %+v", st)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// running reports whether repo's fetch is in flight (tests).
func (s *Scheduler) running(repo string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[repo]
	return st != nil && (st.running || st.timer != nil)
}
