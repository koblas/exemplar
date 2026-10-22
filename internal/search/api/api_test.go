package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/search/index"
	"github.com/koblas/exemplar/internal/search/mirror"
)

var token = strings.Repeat("t", 32)

func upstream(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "svc", "a.go"), []byte("package svc\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.invalid", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.invalid",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "Add A")
	return dir, run("rev-parse", "HEAD")
}

type env struct {
	srv    *httptest.Server
	client *Client
	m      *mirror.Manager
	server *Server
	head   string
	ix     *index.Index
}

func setup(t *testing.T, mirrorAll bool) env {
	t.Helper()
	app, head := upstream(t, "app")
	shared, _ := upstream(t, "shared")
	open, _ := upstream(t, "open")
	urls := map[string]string{"acme/app": app, "acme/shared": shared, "acme/open": open}
	vis := map[string]string{"acme/app": "private", "acme/shared": "internal", "acme/open": "public"}
	m, err := mirror.New(mirror.Config{
		Root: t.TempDir(), URL: func(r string) string { return urls[r] },
		Credentials: func(_ context.Context, r string) (string, string, error) { return "", vis[r], nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.Snapshot{
		Global: schema.GlobalConfig{Enabled: true},
		Repos: map[string]schema.RepoConfig{
			"acme/app": {Enabled: true, Context: &schema.RepoContext{ExtraRepos: []string{"acme/shared", "acme/open"}}},
		},
	}
	scope := func() []string { return mirror.InScope(snap) }
	if mirrorAll {
		for _, r := range scope() {
			if _, err := m.Fetch(t.Context(), r); err != nil {
				t.Fatal(err)
			}
		}
	}
	sched := mirror.NewScheduler(m, scope, time.Hour, time.Hour, nil)
	ix, err := index.New(index.Config{Root: t.TempDir(), Mirrors: m})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ix.Close)
	s, err := NewServer(m, sched, func() *config.Snapshot { return snap }, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.WithIndex(ix)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env{srv: srv, client: c, m: m, server: s, head: head, ix: ix}
}

func get(t *testing.T, url string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()
	e := setup(t, false)
	if get(t, e.srv.URL+"/healthz") != 200 {
		t.Error("healthz not ok")
	}
	if get(t, e.srv.URL+"/readyz") != 503 {
		t.Error("ready before any mirror exists")
	}
	for _, r := range []string{"acme/app", "acme/shared", "acme/open"} {
		if _, err := e.m.Fetch(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	if get(t, e.srv.URL+"/readyz") != 200 {
		t.Error("not ready with every mirror present")
	}
	e.server.Drain()
	if get(t, e.srv.URL+"/readyz") != 503 || get(t, e.srv.URL+"/healthz") != 200 {
		t.Error("draining must fail readiness only")
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	bad, _ := NewClient(e.srv.URL, strings.Repeat("x", 32), nil)
	var apiErr *Error
	if _, err := bad.Snapshot(t.Context(), "acme/app", "acme/app"); !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("wrong token err = %v", err)
	}
	if get(t, e.srv.URL+"/v1/status") != 401 {
		t.Error("status readable without a token")
	}
	if _, err := NewServer(e.m, mirror.NewScheduler(e.m, func() []string { return nil }, 0, 0, nil), func() *config.Snapshot { return nil }, "short", nil); err == nil {
		t.Error("accepted a short API token")
	}
}

func TestReadAndLog(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	ctx := t.Context()
	sha, err := e.client.Snapshot(ctx, "acme/app", "acme/app")
	if err != nil || sha != e.head {
		t.Fatalf("Snapshot = %s, %v", sha, err)
	}
	f, err := e.client.ReadFile(ctx, "acme/app", "acme/app", e.head, "svc/a.go", 3, 3)
	if err != nil || f.Text != "func A() {}\n" || f.Total != 3 || f.SnapshotSHA != e.head {
		t.Fatalf("ReadFile = %+v, %v", f, err)
	}
	commits, err := e.client.Log(ctx, "acme/app", "acme/app", e.head, "svc/a.go", 5)
	if err != nil || len(commits) != 1 || commits[0].Subject != "Add A" {
		t.Fatalf("Log = %+v, %v", commits, err)
	}
	// An extra repo in the same tier (private/internal) is readable.
	sharedHead, _ := e.m.Snapshot(ctx, "acme/shared")
	if _, err := e.client.ReadFile(ctx, "acme/app", "acme/shared", sharedHead, "svc/a.go", 1, 1); err != nil {
		t.Errorf("same-tier extra repo refused: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	ctx := t.Context()
	openHead, _ := e.m.Snapshot(ctx, "acme/open")
	for name, tc := range map[string]struct {
		pr, repo, commit, path string
		want                   error
	}{
		"public extra repo for a private PR": {"acme/app", "acme/open", openHead, "svc/a.go", mirror.ErrForbidden},
		"repo outside extra_repos":           {"acme/app", "acme/other", e.head, "svc/a.go", mirror.ErrForbidden},
		"PR repo not opted in":               {"acme/shared", "acme/shared", e.head, "svc/a.go", mirror.ErrForbidden},
		"traversal":                          {"acme/app", "acme/app", e.head, "../../etc/passwd", mirror.ErrBadRequest},
		"unknown commit":                     {"acme/app", "acme/app", strings.Repeat("b", 40), "svc/a.go", mirror.ErrNoCommit},
		"missing file":                       {"acme/app", "acme/app", e.head, "svc/nope.go", mirror.ErrNoFile},
	} {
		if _, err := e.client.ReadFile(ctx, tc.pr, tc.repo, tc.commit, tc.path, 1, 2); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}

	// Unknown fields and oversized bodies are rejected.
	for _, body := range []string{`{"pr_repo":"acme/app","repo":"acme/app","shell":"rm -rf /"}`, `{"pr_repo":"` + strings.Repeat("a", maxBody) + `"}`} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+"/v1/snapshot", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %.40q... = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestFetchRequest(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	if err := e.client.Fetch(t.Context(), "acme/app"); err != nil {
		t.Errorf("in-scope fetch: %v", err)
	}
	if err := e.client.Fetch(t.Context(), "acme/other"); !errors.Is(err, mirror.ErrNotMirrored) {
		t.Errorf("out-of-scope fetch err = %v", err)
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "ftp://x", "exemplar-search:8080", "http://"} {
		if _, err := NewClient(bad, token, nil); err == nil {
			t.Errorf("NewClient(%q) accepted", bad)
		}
	}
	if _, err := NewClient("http://exemplar-search:8080", "", nil); err == nil {
		t.Error("NewClient accepted an empty token")
	}
}

func TestSearchText(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	ctx := t.Context()
	// Before any build: answered by git grep, flagged degraded.
	res, err := e.client.SearchText(ctx, "acme/app", "acme/app", e.head, `func A\(`, "", "")
	if err != nil || !res.Degraded || len(res.Matches) != 1 || res.Matches[0].Path != "svc/a.go" || res.SnapshotMismatch {
		t.Fatalf("degraded search = %+v, %v", res, err)
	}
	if _, err := e.ix.Build(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	res, err = e.client.SearchText(ctx, "acme/app", "acme/app", strings.Repeat("c", 40), `func A\(`, `\.go$`, "Go")
	if err != nil || res.Degraded || len(res.Matches) != 1 || res.Matches[0].Line != 3 || res.SnapshotSHA != e.head {
		t.Fatalf("indexed search = %+v, %v", res, err)
	}
	if !res.SnapshotMismatch {
		t.Error("a different pinned snapshot was not flagged")
	}
	if _, err := e.client.SearchText(ctx, "acme/app", "acme/open", "", "A", "", ""); !errors.Is(err, mirror.ErrForbidden) {
		t.Errorf("cross-tier search err = %v", err)
	}
	if _, err := e.client.SearchText(ctx, "acme/app", "acme/app", "", "(", "", ""); !errors.Is(err, mirror.ErrBadRequest) {
		t.Errorf("bad pattern err = %v", err)
	}
}

func TestFindDefinition(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	ctx := t.Context()
	res, err := e.client.FindDefinition(ctx, "acme/app", "acme/app", "", "A", "svc/b.go")
	if err != nil || len(res.Definitions) != 1 || res.Definitions[0].Path != "svc/a.go" || res.Definitions[0].Line != 3 || !res.Degraded {
		t.Fatalf("FindDefinition = %+v, %v", res, err)
	}
	if _, err := e.client.FindDefinition(ctx, "acme/app", "acme/app", "", "a b", ""); !errors.Is(err, mirror.ErrBadRequest) {
		t.Errorf("bad name err = %v", err)
	}
	if _, err := e.client.FindDefinition(ctx, "acme/app", "acme/open", "", "A", ""); !errors.Is(err, mirror.ErrForbidden) {
		t.Errorf("cross-tier err = %v", err)
	}
}

func TestFindReferencesAndDependents(t *testing.T) {
	t.Parallel()
	e := setup(t, true)
	ctx := t.Context()
	// Not indexed yet: degraded, and the snapshot says so.
	st, err := e.client.SnapshotStatus(ctx, "acme/app", "acme/app")
	if err != nil || st.SnapshotSHA != e.head || !st.Degraded {
		t.Fatalf("snapshot = %+v, %v", st, err)
	}
	res, err := e.client.FindReferences(ctx, "acme/app", "acme/app", e.head, "A", "svc/a.go")
	if err != nil || len(res.References) != 1 || res.References[0].Path != "svc/a.go" || res.References[0].Line != 3 ||
		!res.Degraded || res.SnapshotMismatch || !res.Proximity {
		t.Fatalf("FindReferences = %+v, %v", res, err)
	}
	dep, err := e.client.Dependents(ctx, "acme/app", "acme/app", "", "svc/a.go", 1)
	if err != nil || len(dep.Files) != 0 || !dep.Proximity {
		t.Fatalf("Dependents = %+v, %v", dep, err)
	}
	if _, err := e.client.FindReferences(ctx, "acme/app", "acme/open", "", "A", "svc/a.go"); !errors.Is(err, mirror.ErrForbidden) {
		t.Errorf("cross-tier err = %v", err)
	}
	if _, err := e.client.FindReferences(ctx, "acme/app", "acme/app", "", "a b", ""); !errors.Is(err, mirror.ErrBadRequest) {
		t.Errorf("bad name err = %v", err)
	}
	// Indexed: no longer degraded.
	if _, err := e.ix.Build(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.client.SnapshotStatus(ctx, "acme/app", "acme/app"); st.Degraded {
		t.Error("indexed repo reported degraded")
	}
	if res, _ := e.client.FindReferences(ctx, "acme/app", "acme/app", e.head, "A", "svc/a.go"); res.Degraded || len(res.References) != 1 {
		t.Errorf("indexed FindReferences = %+v", res)
	}
}
