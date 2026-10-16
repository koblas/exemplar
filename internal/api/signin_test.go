package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/oidc"
	"github.com/koblas/exemplar/internal/store"
)

// fakeIDP is an OpenID provider for the sign-in flow.
type fakeIDP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	challenge string
	claims    map[string]any
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIDP{key: key}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := p.srv.URL
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": b, "authorization_endpoint": b + "/authorize", "token_endpoint": b + "/token", "jwks_uri": b + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "kid": "k", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good" || oidc.Challenge(r.Form.Get("code_verifier")) != p.challenge {
				_, _ = w.Write([]byte(`{"error": "invalid_grant"}`))
				return
			}
			enc := func(v any) string { j, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(j) }
			in := enc(map[string]string{"alg": "RS256", "kid": "k"}) + "." + enc(p.claims)
			sum := sha256.Sum256([]byte(in))
			sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": in + "." + base64.RawURLEncoding.EncodeToString(sig)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// memIdentities is an in-memory Identities.
type memIdentities struct{ links map[string][2]string } // issuer+subject -> {login, github id}

func (m *memIdentities) GitHubLink(_ context.Context, issuer, subject string) (string, bool, error) {
	l, ok := m.links[issuer+" "+subject]
	return l[0], ok, nil
}

func (m *memIdentities) LinkGitHub(_ context.Context, issuer, subject, _ string, id int64, login string) error {
	for k, l := range m.links {
		if l[1] == strconv.FormatInt(id, 10) && k != issuer+" "+subject {
			return ErrLinkedElsewhere
		}
	}
	m.links[issuer+" "+subject] = [2]string{login, strconv.FormatInt(id, 10)}
	return nil
}

// fakeGitHub answers the OAuth calls for one GitHub user.
func fakeGitHub(t *testing.T, login string, id *int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token": "gho_x"}`))
		case "/api/user":
			fmt.Fprintf(w, `{"login": %q, "id": %d, "type": "User"}`, login, *id)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func oidcServer(t *testing.T, idp *fakeIDP, github bool) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	return oidcServerWith(t, idp, github, nil, nil)
}

func oidcServerWith(t *testing.T, idp *fakeIDP, github bool, gh *httptest.Server, mut func(*Config)) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	prov, err := oidc.New(t.Context(), oidc.Config{
		IssuerURL: idp.srv.URL, ClientID: "web", ClientSecret: "shh", EmailDomains: []string{"acme.example"},
		GroupRepos: map[string][]string{"eng": {"acme/*"}}, IgnoreUserInfo: true, SignInText: "Okta", IconURL: "https://cdn.example/okta.svg", HTTP: idp.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	links, err := feedbacklink.New([]byte(strings.Repeat("l", 32)), "http://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := Config{
		Links: links, SessionKey: []byte(strings.Repeat("s", 32)), PublicURL: srv.URL, OIDC: prov,
		Lookup: func(context.Context, int64) (Finding, error) {
			return Finding{RuleID: "propagate-context", Repo: "acme/app", PR: 4}, nil
		},
		Enqueue: func(context.Context, store.FeedbackLinkArgs) error { return nil },
	}
	if github {
		cfg.GitHub = &GitHubAuth{ClientID: "c", ClientSecret: "s", AllowEveryone: true}
		cfg.Identities = &memIdentities{links: map[string][2]string{}}
	}
	if gh != nil {
		cfg.GitHubWeb, cfg.GitHubAPI = gh.URL+"/", gh.URL+"/api/"
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", s.Handler())
	jar, _ := cookiejar.New(nil)
	return s, srv, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestOIDCSignIn(t *testing.T) {
	idp := newFakeIDP(t)
	s, srv, c := oidcServer(t, idp, false)
	get := func(u string) result {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return result{resp.StatusCode, resp.Header}
	}
	// signIn walks /login → /oidc/start → the IdP → /oidc/callback.
	signIn := func(claims map[string]any, state string) result {
		t.Helper()
		resp := get(srv.URL + "/login?return=" + url.QueryEscape("/ui/rules/x"))
		if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/oidc/start?") {
			t.Fatalf("login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		resp = get(srv.URL + resp.Header.Get("Location"))
		auth, _ := url.Parse(resp.Header.Get("Location"))
		q := auth.Query()
		if !strings.HasPrefix(auth.String(), idp.srv.URL+"/authorize?") || q.Get("redirect_uri") != srv.URL+"/oidc/callback" || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("start = %s", auth)
		}
		idp.challenge = q.Get("code_challenge")
		claims["nonce"] = q.Get("nonce")
		idp.claims = claims
		if state == "" {
			state = q.Get("state")
		}
		return get(srv.URL + "/oidc/callback?code=good&state=" + url.QueryEscape(state))
	}
	claims := func() map[string]any {
		return map[string]any{
			"iss": idp.srv.URL, "sub": "u1", "aud": "web", "exp": time.Now().Add(time.Hour).Unix(),
			"email": "dana@acme.example", "preferred_username": "dana-acme", "groups": []any{"eng"},
		}
	}
	if resp := signIn(claims(), "forged"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("forged state = %d", resp.StatusCode)
	}
	bad := claims()
	bad["email"] = "eve@evil.example"
	if resp := signIn(bad, ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("other domain = %d", resp.StatusCode)
	}
	resp := signIn(claims(), "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/rules/x" {
		t.Fatalf("callback = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/ui/", nil)
	u, _ := url.Parse(srv.URL + "/ui/")
	for _, ck := range c.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	login, access, ok := s.UISession(req)
	if !ok || login != "dana-acme" {
		t.Fatalf("session = %q %v", login, ok)
	}
	// Repo access is the groups' mapping, not GitHub.
	if ok, _ := access(t.Context(), "acme/app"); !ok {
		t.Error("acme/app not readable for eng")
	}
	if ok, _ := access(t.Context(), "other/app"); ok {
		t.Error("other/app readable")
	}
	// GitHub sign-in is off.
	if resp := get(srv.URL + "/oauth/start?return=" + url.QueryEscape("/ui/")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("github start = %d", resp.StatusCode)
	}
}

func TestLoginPageBothSources(t *testing.T) {
	idp := newFakeIDP(t)
	_, srv, c := oidcServer(t, idp, true)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/login?return="+url.QueryEscape("/ui/"), nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	_, _ = io.Copy(&b, resp.Body)
	body := b.String()
	for _, want := range []string{"Sign in with GitHub", "Sign in with Okta", `src="https://cdn.example/okta.svg"`, `/oidc/start?return=%2fui%2f`} {
		if !strings.Contains(body, want) {
			t.Errorf("login page lacks %q:\n%s", want, body)
		}
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/login?return=https://evil.example/", nil)
	if resp, err := c.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad return = %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}
}

// flow drives a browser through the sign-in redirects.
type flow struct {
	t   *testing.T
	srv *httptest.Server
	idp *fakeIDP
	c   *http.Client
}

func (f flow) get(u string) result {
	f.t.Helper()
	if strings.HasPrefix(u, "/") {
		u = f.srv.URL + u
	}
	req, _ := http.NewRequestWithContext(f.t.Context(), http.MethodGet, u, nil)
	resp, err := f.c.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return result{resp.StatusCode, resp.Header}
}

// oidc signs in at the IdP as sub and returns the callback's response.
func (f flow) oidc(sub string) result {
	f.t.Helper()
	resp := f.get("/oidc/start?return=" + url.QueryEscape("/ui/rules/x"))
	auth, _ := url.Parse(resp.Header.Get("Location"))
	f.idp.challenge = auth.Query().Get("code_challenge")
	f.idp.claims = map[string]any{
		"iss": f.idp.srv.URL, "sub": sub, "aud": "web", "exp": time.Now().Add(time.Hour).Unix(), "nonce": auth.Query().Get("nonce"),
		"email": "dana@acme.example", "preferred_username": "dana-acme", "groups": []any{"eng"},
	}
	return f.get("/oidc/callback?code=good&state=" + url.QueryEscape(auth.Query().Get("state")))
}

// github follows a redirect to GitHub's authorize page back to the callback.
func (f flow) github(resp result) result {
	f.t.Helper()
	auth, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.Contains(auth.Path, "/login/oauth/authorize") {
		f.t.Fatalf("not sent to GitHub: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return f.get("/oauth/callback?code=good&state=" + url.QueryEscape(auth.Query().Get("state")))
}

func (f flow) session(s *Server) (string, Access, bool) {
	req := httptest.NewRequestWithContext(f.t.Context(), http.MethodGet, f.srv.URL+"/ui/", nil)
	u, _ := url.Parse(f.srv.URL + "/ui/")
	for _, ck := range f.c.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	return s.UISession(req)
}

func TestConnectGitHub(t *testing.T) {
	idp := newFakeIDP(t)
	ghID := int64(7)
	gh := fakeGitHub(t, "dana", &ghID)
	s, srv, c := oidcServerWith(t, idp, true, gh, nil)
	f := flow{t: t, srv: srv, idp: idp, c: c}

	// First OIDC sign-in: straight on to connect GitHub (required).
	resp := f.github(f.oidc("u1"))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/rules/x" {
		t.Fatalf("connect = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	login, access, ok := f.session(s)
	if !ok || login != "dana" {
		t.Fatalf("session = %q %v", login, ok)
	}
	// Repo access is still the OIDC groups', not GitHub's.
	if ok, _ := access(t.Context(), "acme/app"); !ok {
		t.Error("acme/app (eng) not readable")
	}
	if ok, _ := access(t.Context(), "other/app"); ok {
		t.Error("other/app readable")
	}

	// The next sign-in remembers the connection.
	c.Jar, _ = cookiejar.New(nil)
	resp = f.oidc("u1")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/rules/x" {
		t.Fatalf("second sign-in = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if login, _, _ := f.session(s); login != "dana" {
		t.Errorf("second session = %q", login)
	}

	// Another OIDC identity cannot take the same GitHub account.
	c.Jar, _ = cookiejar.New(nil)
	if resp := f.github(f.oidc("u2")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("same GitHub account for u2 = %d", resp.StatusCode)
	}
	if _, _, ok := f.session(s); ok {
		t.Error("a refused connection left a session")
	}
	// It can connect its own account, and u1 can switch with /ui/connect-github.
	ghID = 8
	if resp := f.github(f.oidc("u2")); resp.StatusCode != http.StatusFound {
		t.Errorf("u2's own account = %d", resp.StatusCode)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/ui/connect-github", nil)
	u, _ := url.Parse(srv.URL + "/ui/")
	for _, ck := range c.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	s.Relink(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/login/oauth/authorize") {
		t.Errorf("relink = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Without a pending OIDC sign-in, /connect/github refuses.
	c.Jar, _ = cookiejar.New(nil)
	if resp := f.get("/connect/github"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("connect without a sign-in = %d", resp.StatusCode)
	}
}

func TestConnectGitHubOptional(t *testing.T) {
	idp := newFakeIDP(t)
	ghID := int64(7)
	gh := fakeGitHub(t, "dana", &ghID)
	s, srv, c := oidcServerWith(t, idp, true, gh, func(c *Config) { c.GitHubLink = LinkOptional })
	f := flow{t: t, srv: srv, idp: idp, c: c}
	if resp := f.oidc("u1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("optional = %d (want the connect page)", resp.StatusCode)
	}
	if resp := f.get("/connect/skip"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/rules/x" {
		t.Fatalf("skip = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if login, _, ok := f.session(s); !ok || login != "dana-acme" {
		t.Errorf("skipped session = %q %v", login, ok)
	}
	// Required: skipping is refused.
	_, srv2, c2 := oidcServerWith(t, idp, true, gh, nil)
	f2 := flow{t: t, srv: srv2, idp: idp, c: c2}
	_ = f2.oidc("u3")
	if resp := f2.get("/connect/skip"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("skip when required = %d", resp.StatusCode)
	}
}

func TestGitHubConnectOnly(t *testing.T) {
	idp := newFakeIDP(t)
	ghID := int64(7)
	gh := fakeGitHub(t, "dana", &ghID)
	_, srv, c := oidcServerWith(t, idp, true, gh, func(c *Config) { c.GitHub.SignInDisabled = true })
	f := flow{t: t, srv: srv, idp: idp, c: c}
	// /login goes straight to OIDC; GitHub sign-in is not offered.
	if resp := f.get("/login?return=" + url.QueryEscape("/ui/")); !strings.HasPrefix(resp.Header.Get("Location"), "/oidc/start") {
		t.Errorf("login = %s", resp.Header.Get("Location"))
	}
	if resp := f.get("/oauth/start?return=" + url.QueryEscape("/ui/")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("github sign-in = %d", resp.StatusCode)
	}
	// Connecting still works.
	if resp := f.github(f.oidc("u1")); resp.StatusCode != http.StatusFound {
		t.Errorf("connect = %d", resp.StatusCode)
	}
	links, _ := feedbacklink.New([]byte(strings.Repeat("l", 32)), "http://127.0.0.1")
	if _, err := New(Config{
		Links: links, SessionKey: []byte(strings.Repeat("s", 32)), PublicURL: "https://x",
		GitHub: &GitHubAuth{ClientID: "c", ClientSecret: "s", AllowEveryone: true, SignInDisabled: true},
		Lookup: func(context.Context, int64) (Finding, error) { return Finding{}, nil }, Enqueue: func(context.Context, store.FeedbackLinkArgs) error { return nil },
	}); err == nil {
		t.Error("GitHub for connecting only, without OIDC, was accepted")
	}
}
