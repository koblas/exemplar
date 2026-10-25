package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/buglane"
	"github.com/koblas/exemplar/internal/calibrate"
	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/synth"
)

const (
	repo   = "exemplar-sandbox/playground" // opted in by the example config
	prNum  = 5
	inst   = 42
	header = "package main\n\nimport (\n\t\"context\"\n\t\"net/http\"\n)\n\n"
)

var headMain = header + "func handler(ctx context.Context) {\n\tresp, err := http.Get(\"https://inventory.internal/items\")\n" +
	"\tif err != nil {\n\t\treturn\n\t}\n\tdefer resp.Body.Close()\n\t_ = ctx\n}\n"

// fakeGitHub answers the REST calls a review makes and records writes.
type fakeGitHub struct {
	t *testing.T
	// url and transport reach the fake: served in memory (memTransport), so
	// a synctest bubble's clock can move while its requests are idle.
	url              string
	transport        memTransport
	repo             string
	state, head      string
	base             string
	headAfterReview  string // when set, the PR head moves once the review is posted
	headAfterCheck   string // when set, the PR head moves once a check run starts
	mu               sync.Mutex
	reviews          []map[string]any
	checks           []map[string]any
	checkUpdates     []map[string]any
	checksDone       map[int]bool // check run ids an update completed
	existingApproval bool
	dismissed        int
	replies          []map[string]any
	rewritten        map[string]bool // old heads a force-push left behind
	headRepo         string          // the head's repo when not the PR's (a fork)
	resolved         map[string]bool // thread id -> resolved
	// linterPolls counts reads of the head's check runs; the golangci-lint
	// run completes once there have been more than linterRunning.
	linterPolls   int
	linterRunning int
	minimized     []string // node ids of minimized comments
	humanComments string   // the PR's review comments (JSON); none when empty
	issueComments string   // the PR's conversation comments (JSON); none when empty
	// botComments are conversation comments the app wrote, and how often
	// each was edited.
	botComments []string
	botEdits    int
	teamMembers map[string]bool // logins in every team
	readOnly    map[string]bool // logins with read but not write access
	noAccess    map[string]bool // logins that cannot read the repo
	reactions   []string        // "<comment id>:<content>"
	sarifs      []string        // uploaded SARIF logs, decoded
	alerts      string          // open code-scanning alerts (JSON); none when empty
	// reactionsOn are reactions on the first posted comment, as
	// "<login>:<User|Bot>:<content>".
	reactionsOn []string
	// approvals are people's reviews, as "<login>:<state>:<commit>".
	approvals []string
	// codeowners is the base's .github/CODEOWNERS; none when empty.
	codeowners string
	// listPosted lists the reviews posted here back, as GitHub does
	// (marker dedupe reads them); other tests script the list.
	listPosted bool
	// intercept, when set, sees each request first, with mu held: abort
	// drops the connection unanswered, after applying the request when
	// apply is set (a runner killed before or after GitHub took a write).
	intercept func(r *http.Request) (abort, apply bool)
	// park, when set, sees each request before mu is taken, so it may
	// block a runner at a call while other runners' calls go on.
	park atomic.Pointer[func(r *http.Request)]
}

func newFakeGitHub(t *testing.T, repoName, head, base string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, repo: repoName, state: "open", head: head, base: base, url: "http://github.invalid/"}
	f.transport = memTransport{h: f.handle, running: &sync.WaitGroup{}}
	t.Cleanup(f.transport.running.Wait)
	return f
}

// errConnReset is a request whose connection dropped unanswered.
var errConnReset = errors.New("fake github: connection reset")

// memTransport serves requests with h in memory, as a server would. The
// handler runs on its own goroutine with the request's body already read;
// a request whose context ends first fails with the context's error while
// the handler goes on (the server took it), and a request whose context
// already ended is never sent. A handler that aborts (http.ErrAbortHandler)
// is a dropped connection.
type memTransport struct {
	h       http.HandlerFunc
	running *sync.WaitGroup
}

func (m memTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("fake github: read body: %w", err)
		}
		body = b
	}
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	m.running.Go(func() {
		w := httptest.NewRecorder()
		res := result{}
		func() {
			defer func() {
				if p := recover(); p != nil {
					if p != http.ErrAbortHandler { //nolint:errorlint // the sentinel itself is what aborts
						panic(p)
					}
					res.err = errConnReset
				}
			}()
			m.h(w, req)
		}()
		if res.err == nil {
			res.resp = w.Result()
		}
		done <- res
	})
	select {
	case res := <-done:
		return res.resp, res.err
	case <-r.Context().Done():
		// The handler's answer wins when both are ready.
		select {
		case res := <-done:
			return res.resp, res.err
		default:
		}
		return nil, r.Context().Err()
	}
}

// writeJSON encodes v as the response, failing the test if it cannot.
func (f *fakeGitHub) writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("encode response: %v", err)
	}
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if park := f.park.Load(); park != nil {
		(*park)(r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.intercept != nil {
		if abort, apply := f.intercept(r); abort {
			if apply {
				f.serve(httptest.NewRecorder(), r)
			}
			// The runner died: its connection drops with no response.
			panic(http.ErrAbortHandler)
		}
	}
	f.serve(w, r)
}

// serve answers r; f.mu is held.
func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	pr := fmt.Sprintf("/repos/%s/pulls/%d", f.repo, prNum)
	body := func() map[string]any {
		data, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		return m
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == pr:
		head := f.head
		if f.headAfterReview != "" && len(f.reviews) > 0 {
			head = f.headAfterReview
		}
		if f.headAfterCheck != "" && len(f.checks) > 0 {
			head = f.headAfterCheck
		}
		headRepo := f.repo
		if f.headRepo != "" {
			headRepo = f.headRepo
		}
		fmt.Fprintf(w, `{"title": "Fetch inventory", "body": "Adds a fetch.", "state": %q, "draft": false,
			"user": {"login": "alice", "type": "User"}, "labels": [{"name": "agent:cursor"}],
			"base": {"sha": %q, "ref": "main"}, "head": {"sha": %q, "repo": {"full_name": %q}}}`, f.state, f.base, head, headRepo)
	case r.Method == http.MethodGet && r.URL.Path == pr+"/commits":
		_, _ = w.Write([]byte(`[{"commit": {"message": "Fetch inventory"}}]`))
	case r.Method == http.MethodGet && r.URL.Path == pr+"/reviews":
		var out []map[string]any
		if f.existingApproval {
			out = append(out, map[string]any{
				"id": 1, "state": "APPROVED", "commit_id": "0ld", "user": map[string]any{"type": "Bot"},
				"body": gh.ApproveMarker(1, "0ld"),
			})
		}
		for i, a := range f.approvals {
			parts := strings.Split(a, ":")
			out = append(out, map[string]any{"id": 10 + i, "state": parts[1], "commit_id": parts[2], "user": map[string]any{"login": parts[0], "type": "User"}})
		}
		if f.listPosted {
			for i, rv := range f.reviews {
				state := "COMMENTED"
				if rv["event"] == "APPROVE" {
					state = "APPROVED"
				}
				out = append(out, map[string]any{"id": 101 + i, "state": state, "commit_id": rv["commit_id"], "body": rv["body"], "user": map[string]any{"type": "Bot"}})
			}
		}
		f.writeJSON(w, out)
	case r.Method == http.MethodGet && r.URL.Path == pr+"/files":
		_, _ = w.Write([]byte(`[{"filename": "main.go", "status": "modified"}]`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/contents/"):
		if f.codeowners == "" || r.URL.Path != "/repos/"+f.repo+"/contents/.github/CODEOWNERS" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
			return
		}
		fmt.Fprintf(w, `{"type": "file", "encoding": "base64", "content": %q}`, base64.StdEncoding.EncodeToString([]byte(f.codeowners)))
	case r.Method == http.MethodPost && r.URL.Path == "/graphql":
		b := body()
		q := fmt.Sprint(b["query"])
		switch {
		case strings.Contains(q, "reactionGroups"):
			groups := map[string][]map[string]any{}
			for _, r := range f.reactionsOn {
				p := strings.SplitN(r, ":", 3)
				groups[p[2]] = append(groups[p[2]], map[string]any{"__typename": p[1], "login": p[0]})
			}
			var rg []map[string]any
			for content, nodes := range groups {
				rg = append(rg, map[string]any{"content": content, "reactors": map[string]any{"nodes": nodes}})
			}
			var nodes []map[string]any
			for i, id := range f.commentIDs() {
				cm := map[string]any{"databaseId": id, "reactionGroups": []any{}}
				if i == 0 {
					cm["reactionGroups"] = rg
				}
				nodes = append(nodes, map[string]any{"comments": map[string]any{"nodes": []any{cm}}})
			}
			f.writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"reviewThreads": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}},
			}}}})
		case strings.Contains(q, "minimizeComment"):
			vars, _ := b["variables"].(map[string]any)
			f.minimized = append(f.minimized, fmt.Sprint(vars["id"]))
			_, _ = w.Write([]byte(`{"data": {"minimizeComment": {"minimizedComment": {"isMinimized": true}}}}`))
		case strings.Contains(q, "resolveReviewThread"):
			vars, _ := b["variables"].(map[string]any)
			if f.resolved == nil {
				f.resolved = map[string]bool{}
			}
			f.resolved[fmt.Sprint(vars["id"])] = true
			_, _ = w.Write([]byte(`{"data": {"resolveReviewThread": {"thread": {"isResolved": true}}}}`))
		case strings.Contains(q, "reviewThreads"):
			// One thread per posted comment, id "T<comment id>".
			var nodes []map[string]any
			for _, id := range f.commentIDs() {
				tid := fmt.Sprintf("T%d", id)
				nodes = append(nodes, map[string]any{
					"id": tid, "isResolved": f.resolved[tid],
					"comments": map[string]any{"nodes": []map[string]any{{"databaseId": id}}},
				})
			}
			f.writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"reviewThreads": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}},
			}}}})
		default:
			// Merge safety: no force-push, and an author with merged PRs.
			_, _ = w.Write([]byte(`{"data": {"repository": {"pullRequest": {"timelineItems": {"totalCount": 0}}}, "search": {"issueCount": 3}}}`))
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, pr+"/reviews/") && strings.HasSuffix(r.URL.Path, "/comments"):
		var k int
		_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, pr+"/reviews/"), "%d", &k)
		var out []map[string]any
		if i := k - 101; i >= 0 && i < len(f.reviews) {
			cs, _ := f.reviews[i]["comments"].([]any)
			for j, c := range cs {
				m, _ := c.(map[string]any)
				out = append(out, map[string]any{"id": 1000*(i+1) + j, "path": m["path"], "side": m["side"], "line": m["line"], "body": m["body"]})
			}
		}
		f.writeJSON(w, out)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/compare/"):
		old, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/compare/"), "...")
		status := "ahead"
		if f.rewritten[old] {
			status = "diverged"
		}
		fmt.Fprintf(w, `{"status": %q}`, status)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/pulls/comments/"):
		id := strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/pulls/comments/")
		fmt.Fprintf(w, `{"id": %s, "node_id": "PRRC_%s"}`, id, id)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/collaborators/") && strings.HasSuffix(r.URL.Path, "/permission"):
		login := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/collaborators/"), "/permission")
		perm := "write"
		switch {
		case f.noAccess[login]:
			perm = "none"
		case f.readOnly[login]:
			perm = "read"
		}
		fmt.Fprintf(w, `{"permission": %q}`, perm)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/orgs/") && strings.Contains(r.URL.Path, "/memberships/"):
		login := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !f.teamMembers[login] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"state": "active"}`))
	case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/%s/issues/%d/comments", f.repo, prNum):
		var all []any
		if f.issueComments != "" {
			_ = json.Unmarshal([]byte(f.issueComments), &all)
		}
		for i, c := range f.botComments {
			all = append(all, map[string]any{"id": 9000 + i, "body": c, "user": map[string]any{"login": "exemplar[bot]", "type": "Bot"}})
		}
		data, err := json.Marshal(append([]any{}, all...))
		if err != nil {
			f.t.Errorf("encode comments: %v", err)
		}
		_, _ = w.Write(data)
	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/repos/%s/issues/%d/comments", f.repo, prNum):
		f.botComments = append(f.botComments, fmt.Sprint(body()["body"]))
		_, _ = fmt.Fprintf(w, `{"id": %d}`, 9000+len(f.botComments)-1)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/issues/comments/"):
		i, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/issues/comments/"))
		if i-9000 < 0 || i-9000 >= len(f.botComments) {
			http.NotFound(w, r)
			return
		}
		f.botComments[i-9000] = fmt.Sprint(body()["body"])
		f.botEdits++
		_, _ = fmt.Fprintf(w, `{"id": %d}`, i)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/pulls/comments/") && strings.HasSuffix(r.URL.Path, "/reactions"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/pulls/comments/"), "/reactions")
		f.reactions = append(f.reactions, id+":"+fmt.Sprint(body()["content"]))
		_, _ = w.Write([]byte(`{"id": 1}`))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/"+f.repo+"/code-scanning/alerts":
		if r.URL.Query().Get("ref") != fmt.Sprintf("refs/pull/%d/merge", prNum) || r.URL.Query().Get("state") != "open" {
			f.t.Errorf("alerts query = %s", r.URL.RawQuery)
		}
		if f.alerts == "" {
			_, _ = w.Write([]byte("[]"))
		} else {
			_, _ = w.Write([]byte(f.alerts))
		}
	case r.Method == http.MethodPost && r.URL.Path == "/repos/"+f.repo+"/code-scanning/sarifs":
		gz, _ := base64.StdEncoding.DecodeString(fmt.Sprint(body()["sarif"]))
		if zr, err := gzip.NewReader(bytes.NewReader(gz)); err == nil {
			plain, _ := io.ReadAll(zr)
			f.sarifs = append(f.sarifs, string(plain))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id": "sarif-1"}`))
	case r.Method == http.MethodGet && r.URL.Path == pr+"/comments":
		if f.humanComments == "" {
			_, _ = w.Write([]byte("[]"))
		} else {
			_, _ = w.Write([]byte(f.humanComments))
		}
	case r.Method == http.MethodPost && r.URL.Path == pr+"/comments":
		f.replies = append(f.replies, body())
		_, _ = w.Write([]byte(`{"id": 1}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/commits/") && r.URL.Query().Get("check_name") == gh.CheckName:
		// Exemplar's own check runs on a commit.
		sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/commits/"), "/check-runs")
		runs := []map[string]any{}
		for i, c := range f.checks {
			if c["head_sha"] != sha {
				continue
			}
			status := "in_progress"
			if f.checksDone[i+1] {
				status = "completed"
			}
			runs = append(runs, map[string]any{"id": i + 1, "name": c["name"], "head_sha": sha, "external_id": c["external_id"], "status": status})
		}
		f.writeJSON(w, map[string]any{"total_count": len(runs), "check_runs": runs})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/"+f.repo+"/commits/"+f.head+"/check-runs":
		f.linterPolls++
		status := "in_progress"
		if f.linterPolls > f.linterRunning {
			status = "completed"
		}
		fmt.Fprintf(w, `{"total_count": 1, "check_runs": [{"id": 77, "name": "golangci-lint", "status": %q}]}`, status)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/"+f.repo+"/check-runs/77/annotations":
		_, _ = w.Write([]byte(`[{"path": "main.go", "start_line": 9, "end_line": 9, "title": "noctx", "message": "should rewrite http.NewRequestWithContext"}]`))
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/dismissals"):
		f.dismissed++
		_, _ = w.Write([]byte(`{"id": 1}`))
	case r.Method == http.MethodPost && r.URL.Path == pr+"/reviews":
		f.reviews = append(f.reviews, body())
		fmt.Fprintf(w, `{"id": %d}`, 100+len(f.reviews))
	case r.Method == http.MethodPost && r.URL.Path == "/repos/"+f.repo+"/check-runs":
		f.checks = append(f.checks, body())
		fmt.Fprintf(w, `{"id": %d}`, len(f.checks))
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/"+f.repo+"/check-runs/"):
		u := body()
		if id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/"+f.repo+"/check-runs/")); err == nil && u["status"] == "completed" {
			if f.checksDone == nil {
				f.checksDone = map[int]bool{}
			}
			f.checksDone[id] = true
		}
		f.checkUpdates = append(f.checkUpdates, u)
		_, _ = w.Write([]byte(`{"id": 1}`))
	default:
		f.t.Errorf("unexpected GitHub call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	}
}

// commentIDs are the ids the fake gave posted review comments.
func (f *fakeGitHub) commentIDs() []int {
	var ids []int
	for i, rv := range f.reviews {
		cs, _ := rv["comments"].([]any)
		for j := range cs {
			ids = append(ids, 1000*(i+1)+j)
		}
	}
	return ids
}

func (f *fakeGitHub) client() *github.Client {
	base := f.url
	c, err := github.NewClient(github.WithURLs(&base, &base), github.WithTransport(f.transport))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fakeGitHub) Reader(context.Context, int64, string) (*github.Client, error) {
	return f.client(), nil
}

func (f *fakeGitHub) Gate(context.Context, int64, string) (*github.Client, error) {
	return f.client(), nil
}

func (f *fakeGitHub) Poster(context.Context, int64, string) (*github.Client, error) {
	return f.client(), nil
}
func (f *fakeGitHub) CloneToken(context.Context, int64, string) (string, error) { return "", nil }

type env struct {
	fake *fakeGitHub
	w    *Worker
	head string
	// dir is the repo the worker clones; report switches the scripted
	// propagate-context finding on and off.
	dir    *string
	report *atomic.Bool
	// line is where the scripted finding is reported; main is the head's
	// main.go, for the verifier's evidence quotes.
	line *atomic.Int32
	main *atomic.Pointer[string]
	// fix, when set, is the scripted finding's suggested_fix.
	fix *atomic.Pointer[string]
	// also repeats the scripted finding on these lines.
	also *atomic.Pointer[[]int32]
}

func setup(t *testing.T, repoName string) env { t.Helper(); return setupWith(t, repoName, nil) }

// setupWith wraps the scripted model's transport (nil: none).
func setupWith(t *testing.T, repoName string, wrap func(http.RoundTripper) http.RoundTripper) env {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	r, err := synth.Build(t.Context(), src,
		map[string]string{"main.go": header + "func handler(ctx context.Context) {}\n"},
		map[string]string{"main.go": headMain}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "cfg-test")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	main := &atomic.Pointer[string]{}
	main.Store(&headMain)
	line := &atomic.Int32{}
	line.Store(9)
	report := &atomic.Bool{}
	report.Store(true)
	fix := &atomic.Pointer[string]{}
	also := &atomic.Pointer[[]int32]{}
	dir := r.Dir
	tr := &engine.ScriptedTransport{Script: engine.Script{
		// propagate-context flags http.Get on line 9; other rules report nothing.
		Generator: func(task string, files []string) ([]string, string) {
			if strings.Contains(task, "deadlines reach them") && report.Load() {
				extra := ""
				if s := fix.Load(); s != nil {
					b, err := json.Marshal(*s)
					if err != nil {
						t.Errorf("encode suggested fix: %v", err)
					}
					extra = `, "suggested_fix": ` + string(b)
				}
				lines := []int32{line.Load()}
				if a := also.Load(); a != nil {
					lines = append(lines, *a...)
				}
				vs := make([]string, 0, len(lines))
				for _, n := range lines {
					vs = append(vs, fmt.Sprintf(`{"path": "main.go", "side": "RIGHT", "start_line": %d, "end_line": %d, "explanation": "http.Get ignores ctx."%s}`, n, n, extra))
				}
				return files, `{"violations": [` + strings.Join(vs, ", ") + `]}`
			}
			return files, `{"violations": []}`
		},
		Quote: func(_ string, n int) string { return strings.TrimSpace(strings.Split(*main.Load(), "\n")[n-1]) },
		// A reviewer comment containing "same-concern" is judged a duplicate.
		HumanJudge: func(pair string) bool { return strings.Contains(pair, "same-concern") },
		// Teach-the-bot: "generated" is scope, "on purpose" an exception,
		// "outdated" a wrong rule; anything else is noise.
		TeachClass: func(ctx string) string {
			_, reason, _ := strings.Cut(ctx, "The person's reason:")
			switch {
			case strings.Contains(reason, "generated"):
				return "scope"
			case strings.Contains(reason, "on purpose"):
				return "exception"
			case strings.Contains(reason, "outdated"):
				return "rule_wrong"
			}
			return "noise"
		},
		// A change that adds a request context addresses the finding.
		FixedJudge: func(ctx string) bool {
			_, after, _ := strings.Cut(ctx, "Code after:")
			return strings.Contains(after, "NewRequestWithContext")
		},
		// Replies: "will fix" agrees, "wrong" disagrees, a question asks.
		ReplyClass: func(ctx string) string {
			_, reply, _ := strings.Cut(ctx, "Reply:")
			switch {
			case strings.Contains(reply, "will fix"):
				return "agree"
			case strings.Contains(reply, "wrong"):
				return "disagree"
			case strings.Contains(reply, "?"):
				return "question"
			}
			return "other"
		},
	}}
	var rt http.RoundTripper = tr
	if wrap != nil {
		rt = wrap(tr)
	}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "t", HTTPClient: &http.Client{Transport: rt}})
	fake := newFakeGitHub(t, repoName, r.HeadSHA, r.BaseSHA)
	w, err := New(Config{
		GitHub: fake, Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil },
		Engine: engine.New(h, sc, compiled.Runner{ASTGrep: "ast-grep-not-installed"}), Scanner: sc,
		WorkDir: t.TempDir(), CloneURL: func(string) string { return dir },
	})
	if err != nil {
		t.Fatal(err)
	}
	return env{fake: fake, w: w, head: r.HeadSHA, dir: &dir, report: report, line: line, main: main, fix: fix, also: also}
}

func args(e env) store.PRReviewArgs {
	return store.PRReviewArgs{InstallationID: inst, Repo: e.fake.repo, PR: prNum, HeadSHA: e.head}
}

func TestReviewPostsCommentsAndCheck(t *testing.T) {
	t.Parallel()
	e := setup(t, repo)
	e.fake.existingApproval = true
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusReviewed || out.Posted != 1 || out.Approved || out.Dismissed != 1 || out.Decision != "ineligible" {
		t.Fatalf("outcome = %+v", out)
	}
	f := e.fake
	if len(f.reviews) != 1 || f.reviews[0]["event"] != "COMMENT" || f.reviews[0]["commit_id"] != e.head {
		t.Fatalf("reviews = %+v", f.reviews)
	}
	comments, _ := f.reviews[0]["comments"].([]any)
	if len(comments) != 1 || !strings.Contains(fmt.Sprint(comments[0]), "propagate-context") {
		t.Errorf("comments = %v", comments)
	}
	// The check run starts before the lanes and completes after posting,
	// and its output leads with the verdict: this PR goes to humans, and why.
	if len(f.checks) != 1 || f.checks[0]["status"] != "in_progress" || len(f.checkUpdates) != 1 || f.checkUpdates[0]["conclusion"] != "neutral" {
		t.Errorf("checks = %+v, updates = %+v", f.checks, f.checkUpdates)
	}
	output, _ := f.checkUpdates[0]["output"].(map[string]any)
	title, _ := output["title"].(string)
	summary, _ := output["summary"].(string)
	if !strings.HasSuffix(title, "human review needed") || !strings.HasPrefix(summary, "**Human review needed.** Not auto-approved because ") {
		t.Errorf("check output = %+v", output)
	}
	// These reviews run without exemplar-search, and the check run says so.
	if !strings.HasSuffix(summary, "Cross-repository callers were not checked (search is not configured).") {
		t.Errorf("check summary lacks the search-off scope line: %q", summary)
	}
}

func TestReviewSkips(t *testing.T) {
	t.Parallel()
	e := setup(t, repo)
	a := args(e)
	a.HeadSHA = strings.Repeat("a", 40)
	if out, err := e.w.Review(t.Context(), a); err != nil || out.Status != StatusSuperseded {
		t.Errorf("old head = %+v, %v", out, err)
	}
	e.fake.state = "closed"
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Status != StatusClosed {
		t.Errorf("closed = %+v, %v", out, err)
	}
	if len(e.fake.reviews)+len(e.fake.checks) != 0 {
		t.Errorf("posted on a skipped PR: %+v %+v", e.fake.reviews, e.fake.checks)
	}
	if _, err := e.w.Review(t.Context(), store.PRReviewArgs{Repo: "nope"}); err == nil {
		t.Error("bad job accepted")
	}
}

func TestReviewIneligibleRepo(t *testing.T) {
	t.Parallel()
	e := setup(t, "acme/not-opted-in")
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Status != StatusIneligible {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	f := e.fake
	if len(f.reviews) != 0 || len(f.checks) != 1 || len(f.checkUpdates) != 1 || f.checkUpdates[0]["conclusion"] != "neutral" ||
		!strings.Contains(fmt.Sprint(f.checkUpdates[0]["output"]), "does not review") {
		t.Errorf("checks = %+v %+v", f.checks, f.checkUpdates)
	}
}

func TestApproveRechecksHead(t *testing.T) {
	t.Parallel()
	e := setup(t, repo)
	reader, _ := e.fake.Reader(t.Context(), inst, repo)
	poster := gh.NewPoster(e.fake.client())
	target, _ := gh.ParsePR(repo, prNum, e.head)
	p := &engine.Prepared{PR: engine.PRInfo{Repo: repo, Number: prNum}, Effective: &config.Effective{}}
	ok, err := e.w.approve(t.Context(), reader, poster, target, p, 9, 0)
	if err != nil || !ok || len(e.fake.reviews) != 1 || e.fake.reviews[0]["event"] != "APPROVE" {
		t.Fatalf("approve = %v, %v, %+v", ok, err, e.fake.reviews)
	}
	if body, _ := e.fake.reviews[0]["body"].(string); !strings.HasPrefix(body, "Exemplar found nothing to flag") {
		t.Errorf("approval body = %q", body)
	}
	// Suggestions posted with a clean run are named in the approval.
	if ok, err := e.w.approve(t.Context(), reader, poster, target, p, 12, 2); err != nil || !ok || len(e.fake.reviews) != 2 {
		t.Fatalf("approve with suggestions = %v, %v", ok, err)
	}
	if body, _ := e.fake.reviews[1]["body"].(string); !strings.HasPrefix(body, "Every auto-approve gate passed. Exemplar left 2 suggestion(s); none blocks this approval.") {
		t.Errorf("approval body with suggestions = %q", body)
	}
	e.fake.reviews = e.fake.reviews[:1]
	// The head moved during the review: no approval.
	e.fake.headAfterReview = strings.Repeat("b", 40)
	if ok, err := e.w.approve(t.Context(), reader, poster, target, p, 10, 0); err != nil || ok || len(e.fake.reviews) != 1 {
		t.Errorf("approved a moved head: %v, %v", ok, err)
	}
	// merge_policy_check repos are never approved: they get the check.
	e.fake.headAfterReview = ""
	p.Effective.RepoConfig.ApprovalMode = "merge_policy_check"
	if ok, _ := e.w.approve(t.Context(), reader, poster, target, p, 11, 0); ok {
		t.Error("approved in merge_policy_check mode")
	}
}

// TestRiverJobPersists runs one job through River into the store.
func TestRiverJobPersists(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	workers := river.NewWorkers()
	river.AddWorker(workers, e.w)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{store.ReviewQueue: {MaxWorkers: 1}}, Workers: workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled)
	defer cancel()
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(context.WithoutCancel(t.Context())) }()
	if _, err := client.Insert(t.Context(), args(e), nil); err != nil {
		t.Fatal(err)
	}
	// A duplicate enqueue (redelivered webhook) is skipped.
	if res, err := client.Insert(t.Context(), args(e), nil); err != nil || !res.UniqueSkippedAsDuplicate {
		t.Fatalf("duplicate insert = %+v, %v", res, err)
	}
	if ev := <-events; ev.Kind != river.EventKindJobCompleted {
		t.Fatalf("job = %s", ev.Kind)
	}
	var caller, decision string
	if err := st.Pool.QueryRow(t.Context(), `SELECT r.caller, d.decision FROM reviews r JOIN approval_decisions d ON d.review_id = r.id
		WHERE r.repo = $1 AND r.pr_number = $2`, repo, prNum).Scan(&caller, &decision); err != nil {
		t.Fatal(err)
	}
	if caller != Caller || decision != "ineligible" {
		t.Errorf("review = %s %s", caller, decision)
	}
}

// A shadow repo is reviewed and stored, and nothing reaches its PRs
// except revoking an earlier Exemplar approval.
func TestReviewShadowRepo(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap := exampleSnapshot(t)
	rc := snap.Repos[repo]
	rc.Shadow = true
	snap.Repos[repo] = rc
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	e.fake.existingApproval = true

	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Status != StatusReviewed || !out.Shadow || out.Posted != 0 || out.Dismissed != 1 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if len(e.fake.reviews)+len(e.fake.checks)+len(e.fake.checkUpdates)+len(e.fake.replies) != 0 {
		t.Errorf("wrote to a shadow repo's PR: reviews %v, checks %v, updates %v", e.fake.reviews, e.fake.checks, e.fake.checkUpdates)
	}
	var state, reason string
	if err := st.Pool.QueryRow(t.Context(), `SELECT state, coalesce(suppressed_reason, '') FROM findings WHERE rule_id = 'propagate-context'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "shadow" || reason != "repo_shadow" {
		t.Errorf("finding = %s %s", state, reason)
	}
	// The PR's agent, from its label, for upstream effectiveness.
	var agent string
	var reads []string
	if err := st.Pool.QueryRow(t.Context(), `SELECT agent, agent_reads FROM reviews WHERE caller = 'production'`).Scan(&agent, &reads); err != nil {
		t.Fatal(err)
	}
	if agent != "cursor" || strings.Join(reads, ",") != "cursor,agents_md" {
		t.Errorf("review agent = %q %v", agent, reads)
	}

	// An ineligible PR (a fork) of a shadow repo gets no "not reviewed" check.
	e.fake.mu.Lock()
	e.fake.headRepo = "someone/fork"
	e.fake.mu.Unlock()
	if out, err := e.w.Review(t.Context(), args(e)); err != nil || out.Status != StatusIneligible || len(e.fake.checks) != 0 {
		t.Errorf("ineligible shadow PR = %+v, %v, checks %v", out, err, e.fake.checks)
	}
}

func TestReviewLinterDuplicate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		wait      int
		running   int // polls that see golangci-lint still running
		wantPolls int
		wantDup   bool
	}{
		{name: "waits for the linter", wait: 120, running: 2, wantPolls: 3, wantDup: true},
		{name: "already reported", wait: 120, wantPolls: 1, wantDup: true},
		{name: "no wait configured", wait: 0, running: 5, wantPolls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := setup(t, repo)
			e.w.cfg.LinterPoll = 10 * time.Millisecond
			snap := exampleSnapshot(t)
			rc := snap.Repos[repo]
			rc.Linters = []string{"golangci-lint"}
			snap.Repos[repo] = rc
			snap.Global.LinterWait = &tt.wait
			for i := range snap.Rules {
				if snap.Rules[i].ID == "propagate-context" {
					snap.Rules[i].LinterMap = map[string][]string{"golangci-lint": {"noctx"}}
				}
			}
			e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
			e.fake.linterRunning = tt.running

			out, err := e.w.Review(t.Context(), args(e))
			if err != nil || out.Status != StatusReviewed {
				t.Fatalf("outcome = %+v, %v", out, err)
			}
			if got := out.LinterDuplicates == 1; got != tt.wantDup || (out.Posted == 0) != tt.wantDup {
				t.Errorf("outcome = %+v, want duplicate %v", out, tt.wantDup)
			}
			if e.fake.linterPolls != tt.wantPolls {
				t.Errorf("check-run reads = %d, want %d", e.fake.linterPolls, tt.wantPolls)
			}
		})
	}
}

func TestReviewHumanDuplicate(t *testing.T) {
	t.Parallel()
	e := setup(t, repo)
	e.fake.humanComments = `[
	  {"id": 5, "user": {"login": "dana", "type": "User"}, "author_association": "MEMBER", "path": "main.go", "side": "RIGHT", "line": 9, "body": "same-concern: pass ctx"},
	  {"id": 6, "user": {"login": "dana", "type": "User"}, "author_association": "MEMBER", "path": "main.go", "side": "RIGHT", "line": null, "original_line": 9, "body": "same-concern, outdated"}]`
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Status != StatusReviewed || out.Posted != 0 || out.HumanDuplicates != 1 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
}

// An open code-scanning alert from a configured tool is a linter
// duplicate too, with no check-run annotation at all.
func TestReviewCodeScanningDuplicate(t *testing.T) {
	t.Parallel()
	e := setup(t, repo)
	snap := exampleSnapshot(t)
	rc := snap.Repos[repo]
	rc.Linters = []string{"CodeQL"}
	snap.Repos[repo] = rc
	noWait := 0
	snap.Global.LinterWait = &noWait
	for i := range snap.Rules {
		if snap.Rules[i].ID == "propagate-context" {
			snap.Rules[i].LinterMap = map[string][]string{"codeql": {"go/request-without-context"}}
		}
	}
	e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return snap, nil }
	e.fake.alerts = `[
	  {"number": 4, "state": "open", "rule": {"id": "go/request-without-context", "severity": "warning"}, "tool": {"name": "CodeQL"},
	   "most_recent_instance": {"ref": "refs/pull/5/merge", "location": {"path": "main.go", "start_line": 9, "end_line": 9}, "message": {"text": "No context."}}},
	  {"number": 5, "state": "open", "rule": {"id": "go/request-without-context"}, "tool": {"name": "Exemplar"},
	   "most_recent_instance": {"location": {"path": "main.go", "start_line": 9, "end_line": 9}}}]`
	out, err := e.w.Review(t.Context(), args(e))
	if err != nil || out.Posted != 0 || out.LinterDuplicates != 1 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
}

// Without exemplar-search, local context answers find_references from the
// checkout: on a repository with no context.extra_repos the bug lane's
// contract class runs, so the bug lane is covered and nothing is degraded;
// with extra_repos it is skipped, and the uncovered bug lane keeps the PR
// from auto-approval.
func TestReviewWithoutSearchRunsContract(t *testing.T) {
	t.Parallel()
	for name, extra := range map[string][]string{"single repository": nil, "extra_repos": {"acme/lib"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			st := storetest.New(t)
			e := setup(t, repo)
			e.w.cfg.Store = st
			e.w.cfg.Engine.WithLocalContext(nil)
			snap, _ := e.w.cfg.Snapshot(t.Context())
			cp := *snap
			cp.Global.BugLane.Classes = map[string]schema.BugLaneFlag{buglane.Contract: {Enabled: true}}
			rc := cp.Repos[repo]
			rc.BugLane = &schema.RepoBugLane{CriticalPaths: []string{"main.go"}}
			if extra != nil {
				rc.Context = &schema.RepoContext{ExtraRepos: extra}
			}
			cp.Repos = maps.Clone(cp.Repos)
			cp.Repos[repo] = rc
			e.w.cfg.Snapshot = func(context.Context) (*config.Snapshot, error) { return &cp, nil }
			if _, err := e.w.Review(t.Context(), args(e)); err != nil {
				t.Fatal(err)
			}
			// The skip's detail naming the repositories is checked in the engine.
			var status string
			var degraded, covered bool
			if err := st.Pool.QueryRow(t.Context(), `SELECT rr.status, d.stage1->'input'->>'Degraded' = 'true', d.stage1->'input'->>'BugLaneCovered' = 'true'
				FROM rule_runs rr JOIN reviews r ON r.id = rr.review_id JOIN approval_decisions d ON d.review_id = r.id
				WHERE r.repo = $1 AND rr.rule_id = $2`, repo, buglane.Contract).Scan(&status, &degraded, &covered); err != nil {
				t.Fatal(err)
			}
			if extra == nil && (status != "ok" || degraded || !covered) {
				t.Errorf("single repository: contract %s, degraded %v, bug lane covered %v", status, degraded, covered)
			}
			if extra != nil && (status != "degraded_skip" || covered) {
				t.Errorf("extra_repos: contract %s, bug lane covered %v", status, covered)
			}
		})
	}
}

// Same-family verification keeps a PR from approval until the verifier
// calibration passed for the deployed verifier inputs (FR-E10); the
// latest result decides.
func TestReviewSameFamilyAfterCalibration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	snap, _ := e.w.cfg.Snapshot(ctx)
	trace := func(attempt int) (bool, bool, []string) {
		t.Helper()
		a := args(e)
		a.Attempt = attempt
		if _, err := e.w.Review(ctx, a); err != nil {
			t.Fatal(err)
		}
		var same, calibrated bool
		var reasons []string
		if err := st.Pool.QueryRow(ctx, `SELECT (d.stage1->'input'->>'SameFamilyVerification')::bool, (d.stage1->'input'->>'VerifierCalibrated')::bool,
			ARRAY(SELECT jsonb_array_elements_text(d.stage1->'reasons'))
			FROM approval_decisions d JOIN reviews r ON r.id = d.review_id WHERE r.repo = $1 AND r.attempt = $2`, repo, max(attempt, 1)).Scan(&same, &calibrated, &reasons); err != nil {
			t.Fatal(err)
		}
		return same, calibrated, reasons
	}
	same, calibrated, reasons := trace(0)
	if !same || calibrated || !slices.Contains(reasons, gates.ReasonSameFamily) {
		t.Fatalf("uncalibrated: same %v, calibrated %v, reasons %v", same, calibrated, reasons)
	}
	record := func(passed bool) {
		t.Helper()
		if err := st.RecordCalibration(ctx, queries.RecordCalibrationParams{
			Kind: calibrate.KindVerifier, Key: calibrate.VerifierKey(snap), ConfigVersion: snap.Version,
			Providers: calibrate.VerifierProviders(snap), Labels: 4, Passed: passed,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(true)
	same, calibrated, reasons = trace(2)
	if !same || !calibrated || slices.Contains(reasons, gates.ReasonSameFamily) {
		t.Fatalf("calibrated: same %v, calibrated %v, reasons %v", same, calibrated, reasons)
	}
	// A later failure re-gates; another key's result does not count.
	record(false)
	if verifierCalibrated(ctx, st, snap) {
		t.Error("a later failing calibration still counts as passed")
	}
	other := *snap
	other.Global.Providers.Verifier = []schema.ProviderModel{{Provider: "bedrock", Model: "x"}}
	if verifierCalibrated(ctx, st, &other) {
		t.Error("another key's result counted")
	}
}

// Past rounds.max rounds since a person last reviewed, a new finding goes
// to the one round summary comment instead of inline; a reset lets the
// next push post inline again (FR-C8).
func TestReviewRoundCap(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := storetest.New(t)
	e := setup(t, repo)
	e.w.cfg.Store = st
	// Three earlier pushes each posted a new finding: rounds 1-3.
	for i := 1; i <= 3; i++ {
		if _, err := st.Pool.Exec(ctx, `INSERT INTO reviews (repo, pr_number, head_sha, base_sha, config_version, caller, status, attempt, round, started_at)
			VALUES ($1, $2, $3, 'b', 'cfg-test', 'production', 'done', 1, $4, now() - make_interval(hours => 10 - $4))`,
			repo, prNum, fmt.Sprintf("%040d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	run := func(attempt int) Outcome {
		t.Helper()
		a := args(e)
		a.Attempt = attempt
		out, err := e.w.Review(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run(0)
	if !out.RoundCapped || out.Posted != 0 || out.Round != 0 {
		t.Fatalf("capped run = %+v", out)
	}
	if len(e.fake.reviews) != 1 {
		t.Fatalf("reviews = %d", len(e.fake.reviews))
	}
	if comments, _ := e.fake.reviews[0]["comments"].([]any); len(comments) != 0 {
		t.Errorf("posted inline past the cap: %v", comments)
	}
	if len(e.fake.botComments) != 1 || !strings.Contains(e.fake.botComments[0], pipeline.RoundSummaryMarker) ||
		!strings.Contains(e.fake.botComments[0], "`propagate-context`") || !strings.Contains(e.fake.botComments[0], "3 review rounds") {
		t.Fatalf("round summary = %v", e.fake.botComments)
	}
	text, _ := e.fake.checkUpdates[0]["output"].(map[string]any)
	if s, _ := text["summary"].(string); !strings.Contains(s, "round cap is reached") {
		t.Errorf("check summary = %q", s)
	}
	var state, reason string
	err := st.Pool.QueryRow(ctx, `SELECT state, coalesce(suppressed_reason, '') FROM findings WHERE rule_id = 'propagate-context'`).Scan(&state, &reason)
	if err != nil || state != "summarized" || reason != "round_cap" {
		t.Errorf("stored finding = %s %s, %v", state, reason, err)
	}
	// Another push past the cap edits the same comment.
	run(2)
	if len(e.fake.botComments) != 1 {
		t.Errorf("a second summary comment: %d", len(e.fake.botComments))
	}
	// A person reviewed: the next push posts inline and is round 4.
	if err := st.RecordRoundReset(ctx, queries.RecordRoundResetParams{
		Repo: repo, PrNumber: prNum, Reason: "review",
		At: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	out = run(3)
	if out.RoundCapped || out.Posted != 1 || out.Round != 4 {
		t.Errorf("after a reset = %+v", out)
	}
}
