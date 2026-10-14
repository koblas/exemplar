package index

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/search/mirror"
)

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
		"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.invalid", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.invalid"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		u.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write sets files (empty body deletes) and commits.
func (u *upstream) write(msg string, files map[string]string) {
	u.t.Helper()
	for name, body := range files {
		p := filepath.Join(u.dir, name)
		if body == "" {
			_ = os.Remove(p)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			u.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			u.t.Fatal(err)
		}
	}
	u.git("add", "-A")
	u.git("commit", "-q", "-m", msg)
}

type fixture struct {
	u   *upstream
	m   *mirror.Manager
	ix  *Index
	now time.Time
	mu  sync.Mutex
	cfg Config
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func setup(t *testing.T) *fixture {
	t.Helper()
	u := newUpstream(t)
	u.write("initial", map[string]string{
		".gitattributes":        "gen/*.pb.go linguist-generated\n",
		"svc/fetch.go":          "package svc\n\nfunc Fetch() { http.Get(url) }\n",
		"svc/fetch_test.go":     "package svc\n\nfunc TestFetch() { http.Get(url) }\n",
		"gen/api.pb.go":         "package gen\n\nfunc x() { http.Get(url) }\n",
		"web/app.ts":            "export function load() { fetch(url) }\n",
		"web/package-lock.json": "{\"http.Get\": 1}\n",
	})
	m, err := mirror.New(mirror.Config{Root: t.TempDir(), URL: func(string) string { return u.dir }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fetch(t.Context(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{u: u, m: m, now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	f.cfg = Config{Root: t.TempDir(), Mirrors: m, CTags: FindCTags(), Now: f.clock}
	if f.ix, err = New(f.cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.ix.Close)
	return f
}

func paths(r Result) []string {
	var out []string
	for _, m := range r.Matches {
		out = append(out, fmt.Sprintf("%s:%d", m.Path, m.Line))
	}
	return out
}

func TestBuildAndSearch(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	m1, err := f.ix.Build(ctx, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if m1.Gen != 1 || m1.Deltas != 0 {
		t.Fatalf("first build = %+v", m1)
	}
	res, err := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `http\.Get\(`})
	if err != nil {
		t.Fatal(err)
	}
	// The generated file and the lockfile are excluded from results.
	if got := strings.Join(paths(res), ","); got != "svc/fetch.go:3,svc/fetch_test.go:3" || res.Degraded || res.Commit != m1.Commit {
		t.Fatalf("search = %s %+v", got, res)
	}
	if res, _ := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `http\.Get`, PathFilter: `_test\.go$`}); strings.Join(paths(res), ",") != "svc/fetch_test.go:3" {
		t.Errorf("path filter = %v", paths(res))
	}
	if res, _ := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `url`, Lang: "TypeScript"}); strings.Join(paths(res), ",") != "web/app.ts:1" {
		t.Errorf("lang filter = %v", paths(res))
	}
	if st := f.ix.Status("acme/app"); st.Degraded || st.Gen != 1 || st.Symbols != (f.cfg.CTags != "") {
		t.Errorf("status = %+v", st)
	}

	// Same commit: no new generation.
	if m, _ := f.ix.Build(ctx, "acme/app"); m.Gen != 1 {
		t.Errorf("rebuilt an unchanged commit: %+v", m)
	}

	// A change builds a delta generation and swaps it in.
	f.u.write("change", map[string]string{"svc/fetch.go": "package svc\n\nfunc Fetch(ctx context.Context) { client.Do(req) }\n", "svc/fetch_test.go": ""})
	sha, err := f.m.Fetch(ctx, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	f.ix.NoteMirror("acme/app", sha)
	m2, err := f.ix.Build(ctx, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if m2.Gen != 2 || m2.Deltas != 1 || m2.Commit != sha {
		t.Fatalf("second build = %+v", m2)
	}
	// zoekt really built a delta: the first shard (linked) plus a delta shard.
	if shards, _ := filepath.Glob(filepath.Join(f.cfg.Root, "acme", "app", genName(2), "*.zoekt")); len(shards) != 2 {
		t.Errorf("gen 2 shards = %v, want the original plus one delta shard", shards)
	}
	res, _ = f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `http\.Get\(`})
	if len(res.Matches) != 0 {
		t.Errorf("stale or deleted content still matches: %v", paths(res))
	}
	if res, _ := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `client\.Do`}); strings.Join(paths(res), ",") != "svc/fetch.go:3" {
		t.Errorf("new content = %v", paths(res))
	}
	// The old generation is removed once released.
	waitGone(t, filepath.Join(f.cfg.Root, "acme", "app", genName(1)))

	// After a day, the next build is full.
	f.advance(FullEvery + time.Minute)
	f.u.write("again", map[string]string{"svc/b.go": "package svc\n"})
	if _, err := f.m.Fetch(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	if m3, err := f.ix.Build(ctx, "acme/app"); err != nil || m3.Deltas != 0 || m3.Gen != 3 {
		t.Errorf("daily rebuild = %+v, %v", m3, err)
	}
}

func waitGone(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still exists", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRestartServesAtOnce(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	if _, err := f.ix.Build(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	// Leftovers of an interrupted build are cleaned up on open.
	junk := filepath.Join(f.cfg.Root, "acme", "app", ".building-123")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	f.ix.Close()
	ix2, err := New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ix2.Close()
	if st := ix2.Status("acme/app"); st.Gen != 1 || st.Degraded {
		t.Errorf("after restart status = %+v", st)
	}
	if res, err := ix2.SearchText(ctx, "acme/app", TextQuery{Pattern: "Fetch"}); err != nil || res.Degraded || len(res.Matches) == 0 {
		t.Errorf("after restart search = %+v, %v", res, err)
	}
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Error("incomplete build dir not removed")
	}
}

func TestDegradedMode(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	// No generation yet: git grep at the mirror commit, same filtering.
	res, err := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `http\.Get\(`})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded || strings.Join(paths(res), ",") != "svc/fetch.go:3,svc/fetch_test.go:3" {
		t.Fatalf("degraded search = %v %+v", paths(res), res)
	}
	if res, _ := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `url`, Lang: "TypeScript"}); strings.Join(paths(res), ",") != "web/app.ts:1" {
		t.Errorf("degraded lang filter = %v", paths(res))
	}

	// An index more than an hour behind the mirror degrades too.
	if _, err := f.ix.Build(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	f.u.write("ahead", map[string]string{"svc/new.go": "package svc\n// http.Get(\n"})
	sha, _ := f.m.Fetch(ctx, "acme/app")
	f.ix.NoteMirror("acme/app", sha)
	if res, _ := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `http\.Get\(`}); res.Degraded {
		t.Error("degraded before the lag limit")
	}
	f.advance(LagLimit + time.Minute)
	res, _ = f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: `http\.Get\(`})
	if !res.Degraded || res.Commit != sha || !strings.Contains(strings.Join(paths(res), ","), "svc/new.go") {
		t.Errorf("lagging search = %+v", res)
	}
	if st := f.ix.Status("acme/app"); !st.Degraded || st.Lag < LagLimit {
		t.Errorf("status = %+v", st)
	}
}

func TestSearchLimits(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	var body strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&body, "needle %d\n", i)
	}
	f.u.write("many", map[string]string{"svc/many.txt": body.String()})
	if _, err := f.m.Fetch(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ix.Build(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	res, err := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: "needle"})
	if err != nil || len(res.Matches) != MaxResults || !res.Truncated {
		t.Errorf("search = %d matches, truncated %v, %v", len(res.Matches), res.Truncated, err)
	}
	for _, q := range []TextQuery{{Pattern: ""}, {Pattern: strings.Repeat("a", 201)}, {Pattern: "("}, {Pattern: "a", PathFilter: "["}, {Pattern: "a", Lang: "go;rm"}} {
		if _, err := f.ix.SearchText(ctx, "acme/app", q); err == nil {
			t.Errorf("SearchText(%+v) accepted", q)
		}
	}
}

func TestSearchDuringSwap(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	if _, err := f.ix.Build(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := f.ix.SearchText(ctx, "acme/app", TextQuery{Pattern: "package"}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 5; i++ {
		f.u.write(fmt.Sprint(i), map[string]string{fmt.Sprintf("svc/f%d.go", i): "package svc\n"})
		if _, err := f.m.Fetch(ctx, "acme/app"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ix.Build(ctx, "acme/app"); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}
