package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/store"
)

type env struct {
	srv     *httptest.Server
	gh      *httptest.Server
	links   *feedbacklink.Signer
	client  *http.Client
	queued  []store.FeedbackLinkArgs
	now     time.Time
	ghLogin string
	ghType  string
	s       *Server
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), ghLogin: "alice", ghType: "User"}
	e.gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "shh" {
				fmt.Fprint(w, `{"error": "bad_verification_code"}`)
				return
			}
			fmt.Fprint(w, `{"access_token": "gho_user"}`)
		case "/api/user":
			if r.Header.Get("Authorization") != "Bearer gho_user" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"login": %q, "type": %q}`, e.ghLogin, e.ghType)
		case "/api/orgs/acme/members/alice":
			w.WriteHeader(http.StatusNoContent)
		case "/api/orgs/acme/members/bob":
			w.WriteHeader(http.StatusNotFound)
		case "/api/repos/acme/app":
			fmt.Fprint(w, `{"permissions": {"pull": true}}`)
		case "/api/repos/acme/other":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected GitHub call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(e.gh.Close)
	var err error
	e.links, err = feedbacklink.New([]byte(strings.Repeat("l", 32)), "http://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	s, err := New(Config{
		Links: e.links, SessionKey: []byte(strings.Repeat("s", 32)), PublicURL: e.srv.URL,
		GitHub:    &GitHubAuth{ClientID: "Iv1.client", ClientSecret: "shh", AllowedOrgs: []string{"acme"}},
		GitHubWeb: e.gh.URL + "/", GitHubAPI: e.gh.URL + "/api/",
		Lookup: func(_ context.Context, id int64) (Finding, error) {
			if id != 8812 {
				return Finding{}, errors.New("no rows")
			}
			return Finding{RuleID: "propagate-context", Repo: "acme/app", PR: 4}, nil
		},
		Enqueue: func(_ context.Context, a store.FeedbackLinkArgs) error { e.queued = append(e.queued, a); return nil },
		Now:     func() time.Time { return e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", s.Handler())
	e.s = s
	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

// result is a response, read and closed.
type result struct {
	StatusCode int
	Header     http.Header
}

func (e *env) get(t *testing.T, u string) (result, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{resp.StatusCode, resp.Header}, string(b)
}

// link turns a signed link's base (127.0.0.1) into the test server's.
func (e *env) link(id int64, reason string) string {
	return strings.Replace(e.links.URL(id, reason), "http://127.0.0.1", e.srv.URL, 1)
}

// signIn follows the redirect to GitHub and back, as a browser would.
func (e *env) signIn(t *testing.T, link, code string) result {
	t.Helper()
	resp, _ := e.get(t, link)
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login?return=") {
		t.Fatalf("link without session = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// With GitHub the only source, /login goes straight to it.
	resp, _ = e.get(t, e.srv.URL+resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/oauth/start?return=") {
		t.Fatalf("login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.get(t, e.srv.URL+resp.Header.Get("Location"))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), e.gh.URL+"/login/oauth/authorize") ||
		loc.Query().Get("client_id") != "Iv1.client" || loc.Query().Get("redirect_uri") != e.srv.URL+"/oauth/callback" {
		t.Fatalf("start = %d %s", resp.StatusCode, loc)
	}
	resp, _ = e.get(t, e.srv.URL+"/oauth/callback?code="+code+"&state="+url.QueryEscape(loc.Query().Get("state")))
	return resp
}

var tokenRE = regexp.MustCompile(`name="t" value="([^"]+)"`)

func TestFeedbackLinkFlow(t *testing.T) {
	e := setup(t)
	link := e.link(8812, "not_important")
	resp := e.signIn(t, link, "good")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/f/8812/not_important?s=") {
		t.Fatalf("callback = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Signed in: the GET shows a form and records nothing.
	resp, body := e.get(t, link)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Record: Not important here") || !strings.Contains(body, "acme/app#4") ||
		len(e.queued) != 0 || resp.Header.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("confirm = %d, queued %v\n%s", resp.StatusCode, e.queued, body)
	}
	tok := tokenRE.FindStringSubmatch(body)[1]
	sig := link[strings.Index(link, "?s=")+3:]
	post := func(path string, form url.Values) result {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return result{resp.StatusCode, resp.Header}
	}
	if r := post("/f/8812/not_important", url.Values{"s": {sig}, "text": {"generated code"}}); r.StatusCode != http.StatusForbidden || len(e.queued) != 0 {
		t.Errorf("no form token = %d", r.StatusCode)
	}
	if r := post("/f/8812/wrong", url.Values{"s": {e.links.Sig(8812, "wrong")}, "t": {tok}}); r.StatusCode != http.StatusForbidden {
		t.Errorf("token for another reason = %d", r.StatusCode)
	}
	if r := post("/f/8812/not_important", url.Values{"s": {sig}, "t": {tok}, "text": {"  generated code  "}}); r.StatusCode != http.StatusOK {
		t.Fatalf("record = %d", r.StatusCode)
	}
	if len(e.queued) != 1 || e.queued[0] != (store.FeedbackLinkArgs{FindingID: 8812, Reason: "not_important", Actor: "alice", At: e.now, Text: "generated code"}) {
		t.Errorf("queued = %+v", e.queued)
	}
	// A forged link, or one for a finding retention removed.
	if resp, _ := e.get(t, e.srv.URL+"/f/8812/wrong?s="+sig); resp.StatusCode != http.StatusNotFound {
		t.Errorf("forged = %d", resp.StatusCode)
	}
	if resp, _ := e.get(t, e.link(9, "wrong")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown finding = %d", resp.StatusCode)
	}
	// The session expires.
	e.now = e.now.Add(9 * time.Hour)
	if resp, _ := e.get(t, link); resp.StatusCode != http.StatusFound {
		t.Errorf("expired session = %d", resp.StatusCode)
	}
}

func TestSignInFailures(t *testing.T) {
	e := setup(t)
	link := e.link(8812, "fixed")
	if resp := e.signIn(t, link, "bad"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("bad code = %d", resp.StatusCode)
	}
	e.ghType = "Bot"
	if resp := e.signIn(t, link, "good"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("bot = %d", resp.StatusCode)
	}
	// A callback whose state did not start in this browser.
	fresh := setup(t)
	resp, _ := fresh.get(t, fresh.srv.URL+"/oauth/callback?code=good&state=a.b.c")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign state = %d", resp.StatusCode)
	}
	for _, ret := range []string{"https://evil.example/", "//evil.example/f/", "/oauth/start"} {
		if resp, _ := e.get(t, e.srv.URL+"/oauth/start?return="+url.QueryEscape(ret)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("return %q = %d", ret, resp.StatusCode)
		}
	}
}

// A sign-in for the web UI keeps the token, sealed, in a cookie only
// /ui/ receives; a feedback link's sign-in keeps none.
func TestUISession(t *testing.T) {
	e := setup(t)
	session := func(path string) (string, Access, bool) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+path, nil)
		u, _ := url.Parse(e.srv.URL + path)
		for _, c := range e.client.Jar.Cookies(u) {
			req.AddCookie(c)
		}
		return e.s.UISession(req)
	}
	// A feedback link's sign-in: a session, no UI token.
	link := e.link(8812, "fixed")
	if resp := e.signIn(t, link, "good"); resp.StatusCode != http.StatusFound {
		t.Fatalf("link sign-in = %d", resp.StatusCode)
	}
	if _, _, ok := session("/ui/"); ok {
		t.Fatal("a link sign-in gave the UI a token")
	}
	// The UI's sign-in.
	resp, _ := e.get(t, e.srv.URL+"/oauth/start?return="+url.QueryEscape("/ui/rules/x"))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp, _ = e.get(t, e.srv.URL+"/oauth/callback?code=good&state="+url.QueryEscape(loc.Query().Get("state")))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/rules/x" {
		t.Fatalf("ui callback = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	login, access, ok := session("/ui/")
	if !ok || login != "alice" {
		t.Fatalf("ui session = %q %v", login, ok)
	}
	// Repo access is the person's GitHub permission, with their token.
	if ok, err := access(t.Context(), "acme/app"); err != nil || !ok {
		t.Errorf("acme/app = %v, %v", ok, err)
	}
	if ok, _ := access(t.Context(), "acme/other"); ok {
		t.Error("an unreadable repo was readable")
	}
	// The cookie goes to /ui/ only, and holds no readable token.
	u, _ := url.Parse(e.srv.URL + "/f/")
	for _, c := range e.client.Jar.Cookies(u) {
		if c.Name == uiTokenCookie {
			t.Error("the UI token cookie is sent outside /ui/")
		}
	}
	u, _ = url.Parse(e.srv.URL + "/ui/")
	for _, c := range e.client.Jar.Cookies(u) {
		if strings.Contains(c.Value, "gho_user") {
			t.Error("the token is readable in a cookie")
		}
	}
	// A tampered cookie is no session.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+"/ui/", nil)
	for _, c := range e.client.Jar.Cookies(u) {
		if c.Name == uiTokenCookie {
			c.Value = c.Value[:len(c.Value)-2] + "AA"
		}
		req.AddCookie(c)
	}
	if _, _, ok := e.s.UISession(req); ok {
		t.Error("a tampered UI cookie was accepted")
	}
	for _, ret := range []string{"//evil.example/ui/", "/ui/\\evil"} {
		if resp, _ := e.get(t, e.srv.URL+"/oauth/start?return="+url.QueryEscape(ret)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("return %q = %d", ret, resp.StatusCode)
		}
	}
}

// GitHub sign-in is for members of the allowed orgs.
func TestGitHubAllowedOrgs(t *testing.T) {
	e := setup(t)
	e.ghLogin = "bob"
	if resp := e.signIn(t, e.link(8812, "fixed"), "good"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-member = %d", resp.StatusCode)
	}
	if _, err := New(Config{
		Links: e.links, SessionKey: []byte(strings.Repeat("s", 32)), PublicURL: "https://x",
		GitHub: &GitHubAuth{ClientID: "c", ClientSecret: "s"}, Lookup: func(context.Context, int64) (Finding, error) { return Finding{}, nil },
		Enqueue: func(context.Context, store.FeedbackLinkArgs) error { return nil },
	}); err == nil {
		t.Error("GitHub sign-in with neither allowed orgs nor allow-everyone was accepted")
	}
	if _, err := New(Config{
		Links: e.links, SessionKey: []byte(strings.Repeat("s", 32)), PublicURL: "https://x",
		Lookup: func(context.Context, int64) (Finding, error) { return Finding{}, nil }, Enqueue: func(context.Context, store.FeedbackLinkArgs) error { return nil },
	}); err == nil {
		t.Error("no sign-in source was accepted")
	}
}
