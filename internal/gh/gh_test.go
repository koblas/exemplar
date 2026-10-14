package gh

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
)

// fakeGitHub records requests and serves canned responses.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	requests []recorded
	reviews  []*github.PullRequestReview
	decision string // reviewDecision returned by GraphQL
	approved bool   // flips decision to APPROVED after an APPROVE review
}

type recorded struct {
	Method, Path, Auth string
	Body               map[string]any
}

func newFake(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{t: t, decision: "REVIEW_REQUIRED"}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/access_tokens"):
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_test", "expires_at": time.Now().Add(time.Hour)})
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/reviews"):
		f.mu.Lock()
		_ = json.NewEncoder(w).Encode(f.reviews)
		f.mu.Unlock()
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/reviews"):
		f.mu.Lock()
		rv := &github.PullRequestReview{ID: github.Ptr(int64(100 + len(f.reviews))), Body: github.Ptr(body["body"].(string)), CommitID: github.Ptr(body["commit_id"].(string))}
		f.reviews = append(f.reviews, rv)
		if body["event"] == "APPROVE" && f.approved {
			f.decision = "APPROVED"
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(rv)
	case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/dismissals"):
		f.mu.Lock()
		f.decision = "REVIEW_REQUIRED"
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	case r.Method == "POST" && r.URL.Path == "/graphql":
		f.mu.Lock()
		d := f.decision
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"pullRequest": map[string]any{"reviewDecision": d, "mergeStateStatus": "BLOCKED"},
		}}})
	case strings.Contains(r.URL.Path, "/check-runs"):
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 55})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeGitHub) find(method, suffix string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func newTestApp(t *testing.T, srv *httptest.Server) *App {
	t.Helper()
	app, err := NewApp(AppConfig{AppID: 1, PrivateKey: testKey(t), ConfigRepo: "acme/exemplar-config", BaseURL: srv.URL + "/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestTokenFlavors(t *testing.T) {
	f, srv := newFake(t)
	app := newTestApp(t, srv)

	tests := []struct {
		flavor Flavor
		repos  []string
		perms  map[string]any
	}{
		{FlavorWorker, []string{"acme/app"}, map[string]any{"contents": "read", "pull_requests": "read"}},
		{FlavorContext, []string{"acme/app", "acme/shared"}, map[string]any{"contents": "read", "issues": "read"}},
		{FlavorPoster, []string{"acme/app"}, map[string]any{"pull_requests": "write", "checks": "write", "security_events": "write"}},
		{FlavorConfigWriter, []string{"acme/exemplar-config"}, map[string]any{"contents": "write", "pull_requests": "write", "issues": "write"}},
		{FlavorIngest, []string{"acme/app"}, map[string]any{"contents": "read", "pull_requests": "read", "members": "read"}},
		{FlavorGate, []string{"acme/app"}, map[string]any{"administration": "read", "members": "read"}},
		{FlavorExport, []string{"acme/app"}, map[string]any{"contents": "write", "pull_requests": "write"}},
		{FlavorSweep, []string{"acme/app", "acme/web"}, map[string]any{"pull_requests": "read"}},
	}
	for _, tt := range tests {
		t.Run(tt.flavor.String(), func(t *testing.T) {
			before := len(f.find("POST", "/access_tokens"))
			if _, err := app.Token(t.Context(), 9, tt.flavor, tt.repos); err != nil {
				t.Fatalf("Token: %v", err)
			}
			reqs := f.find("POST", "/access_tokens")
			if len(reqs) != before+1 {
				t.Fatal("no token request recorded")
			}
			got := reqs[len(reqs)-1]
			if !strings.HasPrefix(got.Auth, "Bearer ") {
				t.Errorf("token mint not authenticated with the app JWT: %q", got.Auth)
			}
			gotPerms, _ := got.Body["permissions"].(map[string]any)
			if len(gotPerms) != len(tt.perms) {
				t.Errorf("permissions = %v, want exactly %v", gotPerms, tt.perms)
			}
			for k, v := range tt.perms {
				if gotPerms[k] != v {
					t.Errorf("permission %s = %v, want %v", k, gotPerms[k], v)
				}
			}
			repos, _ := got.Body["repositories"].([]any)
			if len(repos) != len(tt.repos) {
				t.Errorf("repositories = %v, want %d names", repos, len(tt.repos))
			}
		})
	}
}

func TestTokenScopeRefusals(t *testing.T) {
	_, srv := newFake(t)
	app := newTestApp(t, srv)
	tests := []struct {
		name   string
		flavor Flavor
		repos  []string
	}{
		{"worker on two repos", FlavorWorker, []string{"acme/a", "acme/b"}},
		{"poster on no repo", FlavorPoster, nil},
		{"config writer on a PR repo", FlavorConfigWriter, []string{"acme/app"}},
		{"context reading config repo", FlavorContext, []string{"acme/app", "acme/exemplar-config"}},
		{"ingest on two repos", FlavorIngest, []string{"acme/a", "acme/b"}},
		{"ingest reading config repo", FlavorIngest, []string{"acme/exemplar-config"}},
		{"export on two repos", FlavorExport, []string{"acme/a", "acme/b"}},
		{"export writing config repo", FlavorExport, []string{"acme/exemplar-config"}},
		{"cross-owner", FlavorContext, []string{"acme/app", "other/app"}},
		{"bad repo name", FlavorWorker, []string{"app"}},
		{"unknown flavor", Flavor(99), []string{"acme/app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := app.Token(t.Context(), 9, tt.flavor, tt.repos); err == nil {
				t.Fatal("Token succeeded, want refusal")
			}
		})
	}
}

func posterFor(t *testing.T, srv *httptest.Server) *Poster {
	t.Helper()
	app := newTestApp(t, srv)
	c, err := app.Client(t.Context(), 9, FlavorPoster, []string{"acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	return NewPoster(c)
}

func TestPostReviewIsIdempotent(t *testing.T) {
	f, srv := newFake(t)
	p := posterFor(t, srv)
	pr := PR{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "abc123"}
	rv := ReviewPost{ReviewID: 42, Summary: "2 findings", Comments: []Comment{
		{Path: "a.go", Side: "RIGHT", StartLine: 3, EndLine: 3, Body: "single"},
		{Path: "b.go", Side: "LEFT", StartLine: 10, EndLine: 12, Body: "multi"},
	}}

	id1, posted, err := p.PostReview(t.Context(), pr, rv)
	if err != nil || !posted {
		t.Fatalf("first PostReview = %d, %v, %v", id1, posted, err)
	}
	id2, posted, err := p.PostReview(t.Context(), pr, rv)
	if err != nil || posted || id2 != id1 {
		t.Fatalf("retry PostReview = %d, posted=%v, %v; want existing %d", id2, posted, err, id1)
	}
	creates := f.find("POST", "/reviews")
	if len(creates) != 1 {
		t.Fatalf("%d reviews created, want 1", len(creates))
	}
	body := creates[0].Body
	if body["event"] != "COMMENT" {
		t.Errorf("event = %v, want COMMENT", body["event"])
	}
	if !strings.Contains(body["body"].(string), Marker(42, "abc123")) {
		t.Error("review body lacks the idempotency marker")
	}
	if auth := creates[0].Auth; auth != "Bearer ghs_test" {
		t.Errorf("review posted with %q, want the poster installation token", auth)
	}
	comments := body["comments"].([]any)
	single := comments[0].(map[string]any)
	if _, has := single["start_line"]; has || single["line"] != float64(3) {
		t.Errorf("single-line comment = %v", single)
	}
	multi := comments[1].(map[string]any)
	if multi["start_line"] != float64(10) || multi["line"] != float64(12) || multi["start_side"] != "LEFT" {
		t.Errorf("multi-line comment = %v", multi)
	}

	// A different head is a different review.
	if _, posted, _ := p.PostReview(t.Context(), PR{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "def456"}, rv); !posted {
		t.Error("review on a new head was treated as a duplicate")
	}
}

func TestPostReviewRejectsBadRange(t *testing.T) {
	_, srv := newFake(t)
	p := posterFor(t, srv)
	_, _, err := p.PostReview(t.Context(), PR{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "h"},
		ReviewPost{ReviewID: 1, Comments: []Comment{{Path: "a.go", Side: "RIGHT", StartLine: 5, EndLine: 4}}})
	if err == nil {
		t.Fatal("reversed range accepted")
	}
}

func TestCheckRunIncompleteHasRerun(t *testing.T) {
	f, srv := newFake(t)
	p := posterFor(t, srv)
	pr := PR{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "h"}
	id, err := p.StartCheck(t.Context(), pr, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CompleteCheck(t.Context(), pr, id, CheckResult{Conclusion: "neutral", Title: "Review did not complete", Incomplete: true}); err != nil {
		t.Fatal(err)
	}
	upd := f.find("PATCH", "/check-runs/55")
	if len(upd) != 1 {
		t.Fatalf("got %d check updates", len(upd))
	}
	actions, _ := upd[0].Body["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["identifier"] != RerunAction {
		t.Fatalf("actions = %v, want the Re-run action", actions)
	}
}

func TestProbeApproval(t *testing.T) {
	for _, counts := range []bool{true, false} {
		f, srv := newFake(t)
		f.approved = counts
		p := posterFor(t, srv)
		res, err := p.ProbeApproval(t.Context(), PR{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "h"}, 0)
		if err != nil {
			t.Fatalf("ProbeApproval: %v", err)
		}
		if res.Counted != counts || !res.Dismissed {
			t.Errorf("counts=%v: probe = %+v", counts, res)
		}
		wantMode := map[bool]string{true: "native_approve", false: "merge_policy_check"}[counts]
		if res.Mode != wantMode {
			t.Errorf("mode = %s, want %s", res.Mode, wantMode)
		}
		if len(f.find("PUT", "/dismissals")) != 1 {
			t.Error("probe approval was not dismissed")
		}
	}

	f, srv := newFake(t)
	f.decision = "APPROVED"
	p := posterFor(t, srv)
	if _, err := p.ProbeApproval(t.Context(), PR{Owner: "acme", Repo: "app", Number: 7, HeadSHA: "h"}, 0); !errors.Is(err, ErrAlreadyApproved) {
		t.Fatalf("ProbeApproval on approved PR = %v, want ErrAlreadyApproved", err)
	}
	if len(f.find("POST", "/reviews")) != 0 {
		t.Error("probe submitted an approval on an already-approved PR")
	}
}
