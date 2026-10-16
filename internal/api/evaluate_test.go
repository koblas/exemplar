package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/svcauth"
)

type fakeUsers struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeUsers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls[r.URL.Path]++
	f.mu.Unlock()
	users := map[string]string{
		"Bearer tok-alice": `{"login": "alice", "type": "User"}`, "Bearer tok-bob": `{"login": "bob", "type": "User"}`,
		"Bearer tok-bot": `{"login": "ci[bot]", "type": "Bot"}`,
	}
	auth := r.Header.Get("Authorization")
	switch r.URL.Path {
	case "/user":
		u, ok := users[auth]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(u))
	case "/repos/acme/app":
		_, _ = w.Write([]byte(`{"permissions": {"pull": true}}`))
	case "/orgs/acme/members/alice":
		w.WriteHeader(http.StatusNoContent)
	case "/orgs/acme/members/bob":
		w.WriteHeader(http.StatusNotFound)
	case "/repos/acme/nopull":
		_, _ = w.Write([]byte(`{"permissions": {"pull": false}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestEvaluateAPI(t *testing.T) {
	gh := &fakeUsers{calls: map[string]int{}}
	ghs := httptest.NewServer(gh)
	defer ghs.Close()

	var mu sync.Mutex
	stored := map[string]evaluate.Status{}
	owner := map[string]string{}
	runs := 0
	e, err := NewEvaluations(EvalConfig{
		GitHubAPI: ghs.URL + "/", DailyUSD: 5, DailyRuns: 3, PerMinute: 10, GitHub: &GitHubAuth{AllowEveryone: true},
		Create: func(_ context.Context, r evaluate.Request, login, _ string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			id := "11111111-1111-1111-1111-11111111111" + string(rune('0'+len(stored)))
			stored[id] = evaluate.Status{ID: id, Status: evaluate.StatusQueued, Repo: r.Repo, BaseSHA: r.BaseSHA}
			owner[id] = login
			runs++
			return id, nil
		},
		Get: func(_ context.Context, id string) (evaluate.Status, string, error) {
			mu.Lock()
			defer mu.Unlock()
			s, ok := stored[id]
			if !ok {
				return evaluate.Status{}, "", ErrNotFound
			}
			return s, owner[id], nil
		},
		Spent: func(_ context.Context, _ string, since time.Time) (float64, int, error) {
			if since.Hour() != 0 || since.Minute() != 0 {
				t.Errorf("budget day starts at %v", since)
			}
			mu.Lock()
			defer mu.Unlock()
			return 0, runs, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	do := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var b []byte
		if s, ok := body.(string); ok {
			b = []byte(s)
		} else if body != nil {
			var err error
			if b, err = json.Marshal(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	good := evaluate.Request{Repo: "acme/app", BaseSHA: strings.Repeat("a", 40), Diff: "diff --git a/x b/x\n"}

	for _, c := range []struct {
		name, token string
		body        any
		code        int
	}{
		{"no token", "", good, http.StatusUnauthorized},
		{"unknown token", "tok-nobody", good, http.StatusUnauthorized},
		{"a bot", "tok-bot", good, http.StatusUnauthorized},
		{"not JSON", "tok-alice", "{", http.StatusBadRequest},
		{"invalid", "tok-alice", evaluate.Request{Repo: "acme/app", BaseSHA: "abc", Diff: "diff --git "}, http.StatusBadRequest},
		{"unreadable repo", "tok-alice", evaluate.Request{Repo: "acme/secret", BaseSHA: good.BaseSHA, Diff: good.Diff}, http.StatusNotFound},
		{"no pull permission", "tok-alice", evaluate.Request{Repo: "acme/nopull", BaseSHA: good.BaseSHA, Diff: good.Diff}, http.StatusNotFound},
	} {
		if code, out := do(http.MethodPost, "/v1/evaluate", c.token, c.body); code != c.code {
			t.Errorf("%s: %d %v, want %d", c.name, code, out, c.code)
		}
	}
	if code, _ := do(http.MethodPost, "/v1/evaluate", "tok-alice", `{"repo": "acme/app", "diff": "`+strings.Repeat("x", evaluate.MaxDiff+70<<10)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized = %d", code)
	}

	code, out := do(http.MethodPost, "/v1/evaluate", "tok-alice", good)
	if code != http.StatusAccepted || out["status"] != "queued" {
		t.Fatalf("create = %d %v", code, out)
	}
	id, ok := out["id"].(string)
	if !ok {
		t.Fatalf("create id = %v", out["id"])
	}
	if code, out := do(http.MethodGet, "/v1/evaluate/"+id, "tok-alice", nil); code != http.StatusOK || out["id"] != id {
		t.Errorf("get = %d %v", code, out)
	}
	// Someone else's evaluation, or none, is not found.
	if code, _ := do(http.MethodGet, "/v1/evaluate/"+id, "tok-bob", nil); code != http.StatusNotFound {
		t.Errorf("bob = %d", code)
	}
	if code, _ := do(http.MethodGet, "/v1/evaluate/nope", "tok-alice", nil); code != http.StatusNotFound {
		t.Errorf("missing = %d", code)
	}
	// The login and repo access are cached; GitHub was asked once each.
	if gh.calls["/user"] > 4 || gh.calls["/repos/acme/app"] != 1 {
		t.Errorf("GitHub calls = %v", gh.calls)
	}

	// The daily budget: three runs.
	code, out = do(http.MethodPost, "/v1/evaluate", "tok-bob", good)
	if code != http.StatusAccepted {
		t.Fatalf("bob create = %d %v", code, out)
	}
	code, out = do(http.MethodPost, "/v1/evaluate", "tok-bob", good)
	if code != http.StatusAccepted {
		t.Fatalf("bob second = %d %v", code, out)
	}
	if code, out := do(http.MethodPost, "/v1/evaluate", "tok-bob", good); code != http.StatusTooManyRequests || !strings.Contains(fmt.Sprint(out["error"]), "daily budget") {
		t.Errorf("over budget = %d %v", code, out)
	}
	// The per-minute limit counts every signed-in POST, valid or not:
	// alice has made 6 of her 10.
	for i := range 5 {
		code, out := do(http.MethodPost, "/v1/evaluate", "tok-alice", "{")
		limited := code == http.StatusTooManyRequests && strings.Contains(fmt.Sprint(out["error"]), "slow down")
		if limited != (i == 4) {
			t.Errorf("post %d = %d %v", 7+i, code, out)
		}
	}
}

type fakeServices struct{}

func (fakeServices) Verify(_ context.Context, token string) (svcauth.Claims, error) {
	if token == "h.oidc.s" {
		return svcauth.Claims{Subject: "dana", Permissions: []string{"evaluate:acme/*"}, User: true, Issuer: "https://idp", Sub: "u1"}, nil
	}
	if token == "h.unlinked.s" {
		return svcauth.Claims{Subject: "erin", Permissions: []string{"evaluate:acme/*"}, User: true, Issuer: "https://idp", Sub: "u9"}, nil
	}
	if token != "h.ci.s" {
		return svcauth.Claims{}, svcauth.ErrInvalid
	}
	return svcauth.Claims{Subject: "svc:ci@clients", Permissions: []string{"evaluate:acme/*"}}, nil
}

func TestEvaluateServices(t *testing.T) {
	gh := &fakeUsers{calls: map[string]int{}}
	ghs := httptest.NewServer(gh)
	defer ghs.Close()
	var got struct{ requester, caller string }
	e, err := NewEvaluations(EvalConfig{
		GitHubAPI: ghs.URL + "/", DailyUSD: 5, DailyRuns: 10, PerMinute: 10, Services: fakeServices{}, GitHub: &GitHubAuth{AllowedOrgs: []string{"acme"}},
		Create: func(_ context.Context, _ evaluate.Request, requester, caller string) (string, error) {
			got.requester, got.caller = requester, caller
			return "e1", nil
		},
		Get: func(_ context.Context, id string) (evaluate.Status, string, error) {
			return evaluate.Status{ID: id, Status: evaluate.StatusDone}, "svc:ci@clients", nil
		},
		Spent: func(context.Context, string, time.Time) (float64, int, error) { return 0, 0, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()
	do := func(method, path, token string, body any) int {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	req := func(repo string) evaluate.Request {
		return evaluate.Request{Repo: repo, BaseSHA: strings.Repeat("a", 40), Diff: "diff --git a/x b/x\n", Agent: "ci"}
	}
	if code := do(http.MethodPost, "/v1/evaluate", "h.ci.s", req("acme/app")); code != http.StatusAccepted || got.requester != "svc:ci@clients" || got.caller != "agent" {
		t.Errorf("service create = %d, %+v", code, got)
	}
	// Mode eval on the config repo, which the service's acme/* covers.
	eval := evaluate.Request{Mode: evaluate.ModeEval, Repo: "acme/exemplar-config", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
	if code := do(http.MethodPost, "/v1/evaluate", "h.ci.s", eval); code != http.StatusAccepted || got.caller != "agent" {
		t.Errorf("eval create = %d, %+v", code, got)
	}
	eval.Diff = "diff --git a/x b/x\n"
	if code := do(http.MethodPost, "/v1/evaluate", "h.ci.s", eval); code != http.StatusBadRequest {
		t.Errorf("eval with a diff = %d", code)
	}
	if code := do(http.MethodPost, "/v1/evaluate", "h.ci.s", req("other/app")); code != http.StatusNotFound {
		t.Errorf("repo outside the permission = %d", code)
	}
	if code := do(http.MethodPost, "/v1/evaluate", "h.bad.s", req("acme/app")); code != http.StatusUnauthorized {
		t.Errorf("bad service token = %d", code)
	}
	if code := do(http.MethodGet, "/v1/evaluate/e1", "h.ci.s", nil); code != http.StatusOK {
		t.Errorf("service poll = %d", code)
	}
	// GitHub was never asked about a service; a GitHub user still works.
	if len(gh.calls) != 0 {
		t.Errorf("GitHub calls for a service = %v", gh.calls)
	}
	if code := do(http.MethodPost, "/v1/evaluate", "tok-alice", req("acme/app")); code != http.StatusAccepted || got.caller != "cli" || got.requester != "alice" {
		t.Errorf("user create = %d, %+v", code, got)
	}
	// Outside the allowed orgs.
	if code := do(http.MethodPost, "/v1/evaluate", "tok-bob", req("acme/app")); code != http.StatusForbidden {
		t.Errorf("non-member = %d", code)
	}
	// An OIDC sign-in is a person (caller cli), with repos from its groups.
	if code := do(http.MethodPost, "/v1/evaluate", "h.oidc.s", req("acme/app")); code != http.StatusAccepted || got.caller != "cli" || got.requester != "dana" {
		t.Errorf("oidc user = %d, %+v", code, got)
	}
	// Connected GitHub accounts: the OIDC person is their GitHub login;
	// without one, required connecting refuses them.
	e.cfg.Identities = &memIdentities{links: map[string][2]string{"https://idp u1": {"dana-gh", "7"}}}
	e.cfg.GitHubLink = LinkRequired
	if code := do(http.MethodPost, "/v1/evaluate", "h.oidc.s", req("acme/app")); code != http.StatusAccepted || got.requester != "dana-gh" {
		t.Errorf("linked oidc user = %d, %+v", code, got)
	}
	if code := do(http.MethodPost, "/v1/evaluate", "h.unlinked.s", req("acme/app")); code != http.StatusForbidden {
		t.Errorf("unlinked oidc user = %d", code)
	}
	e.cfg.GitHubLink = LinkOptional
	if code := do(http.MethodPost, "/v1/evaluate", "h.unlinked.s", req("acme/app")); code != http.StatusAccepted || got.requester != "erin" {
		t.Errorf("unlinked, optional = %d, %+v", code, got)
	}
	// With GitHub sign-in off, GitHub tokens are refused.
	e.cfg.GitHub = nil
	if code := do(http.MethodPost, "/v1/evaluate", "tok-alice", req("acme/app")); code != http.StatusUnauthorized {
		t.Errorf("github token with GitHub off = %d", code)
	}
}
