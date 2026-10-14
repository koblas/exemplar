package pathhistory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/search/mirror"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// newMirror builds a bare mirror at <root>/acme/app with dated commits and
// returns a Manager over root and the HEAD commit.
func newMirror(t *testing.T) (*mirror.Manager, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(date string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.invalid", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.invalid",
			"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("2026-01-01T00:00:00Z", "init", "-q", "-b", "main")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range []struct{ date, file, msg string }{
		{"2026-08-01T10:00:00Z", "svc/pay.go", "Add pay handler"},
		{"2026-08-20T10:00:00Z", "svc/cart.go", "Add cart"},
		{"2026-09-05T10:00:00Z", "svc/pay.go", "Retry charges on timeout"},
		{"2026-09-25T10:00:00Z", "svc/pay.go", strings.Repeat("Long subject ", 30)},
	} {
		if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(c.file, fmt.Sprintf("package svc // %d\n", i))
		git(c.date, "add", "-A")
		git(c.date, "commit", "-q", "-m", c.msg)
	}
	head := git("2026-09-25T10:00:00Z", "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("2026-09-25T10:00:00Z", "clone", "-q", "--bare", dir, filepath.Join(root, "acme", "app"))
	m, err := mirror.New(mirror.Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return m, head
}

func events(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		kind := "incident"
		if i%2 == 1 {
			kind = "revert"
		}
		if _, err := st.Pool.Exec(t.Context(), `INSERT INTO path_events (repo, path, kind, ref, at) VALUES ('acme/app', 'svc/pay.go', $1, $2, $3)`,
			kind, fmt.Sprintf("REF-%02d", i), time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistory(t *testing.T) {
	st := storetest.New(t)
	m, head := newMirror(t)
	events(t, st, 2)
	s, err := New(st, Local(m))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.History(t.Context(), "acme/app", "svc/pay.go", head)
	if err != nil {
		t.Fatal(err)
	}
	if res.Degraded {
		t.Fatal("degraded with a mirror")
	}
	var got []string
	for _, e := range res.Entries {
		got = append(got, e.At.UTC().Format("01-02")+" "+e.Kind)
	}
	want := "09-25 commit,09-10 incident,09-09 revert,09-05 commit,08-01 commit" // cart.go's commit is not included
	if strings.Join(got, ",") != want {
		t.Errorf("entries = %v, want %s", got, want)
	}
	if s := res.Entries[0].Summary; len([]rune(s)) > 200+1 || !strings.HasSuffix(s, "…") {
		t.Errorf("long subject not truncated: %q", s)
	}
	if res.Entries[3].Summary != "Retry charges on timeout" || len(res.Entries[3].Ref) != 12 {
		t.Errorf("commit entry = %+v", res.Entries[3])
	}
	out := Format(res)
	if !strings.Contains(out, "2026-09-10 incident REF-00\n") || strings.Contains(out, "dev@example.invalid") || strings.Contains(out, "Dev") {
		t.Errorf("Format = %q", out)
	}
}

func TestHistoryCapsAndShares(t *testing.T) {
	st := storetest.New(t)
	m, head := newMirror(t)
	events(t, st, 15)
	s, _ := New(st, Local(m))
	res, err := s.History(t.Context(), "acme/app", "svc/pay.go", head)
	if err != nil {
		t.Fatal(err)
	}
	var ev, commits int
	for _, e := range res.Entries {
		if e.Kind == KindCommit {
			commits++
		} else {
			ev++
		}
	}
	if ev != MaxEvents || commits != 3 || len(res.Entries) > MaxEntries {
		t.Errorf("events = %d, commits = %d, total = %d", ev, commits, len(res.Entries))
	}
}

func TestHistoryDegrades(t *testing.T) {
	st := storetest.New(t)
	m, head := newMirror(t)
	events(t, st, 1)
	for name, tc := range map[string]struct {
		commits Commits
		repo    string
		rev     string
	}{
		"no mirrors":         {nil, "acme/app", head},
		"no mirror for repo": {Local(m), "acme/other", head},
		"unknown commit":     {Local(m), "acme/app", strings.Repeat("a", 40)},
		"bad rev":            {Local(m), "acme/app", "HEAD~1"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := New(st, tc.commits)
			res, err := s.History(t.Context(), tc.repo, "svc/pay.go", tc.rev)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Degraded || (tc.repo == "acme/app" && len(res.Entries) != 1) {
				t.Errorf("res = %+v", res)
			}
			if !strings.Contains(Format(res), "degraded") {
				t.Error("Format does not say degraded")
			}
		})
	}
}

func TestHistoryRejectsBadPaths(t *testing.T) {
	st := storetest.New(t)
	m, head := newMirror(t)
	s, _ := New(st, Local(m))
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../../b", "-n1", "a//b", "a\x00b"} {
		if _, err := s.History(t.Context(), "acme/app", p, head); err == nil {
			t.Errorf("History(%q) accepted", p)
		}
	}
	// Paths are literal: a glob matches nothing rather than every Go file.
	res, err := s.History(t.Context(), "acme/app", "svc/*.go", head)
	if err != nil || len(res.Entries) != 0 {
		t.Errorf("glob path = %+v, %v", res, err)
	}
}
