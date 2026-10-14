package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// idp is a fake OpenID provider: discovery, JWKS, a token endpoint that
// checks PKCE, and userinfo.
type idp struct {
	t         *testing.T
	srv       *httptest.Server
	key       *rsa.PrivateKey
	challenge string         // the code_challenge of the last authorization
	claims    map[string]any // the next ID token's claims
	userinfo  map[string]any
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{t: t, key: key}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) serve(w http.ResponseWriter, r *http.Request) {
	base := p.srv.URL
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"userinfo_endpoint": base + "/userinfo", "jwks_uri": base + "/jwks", "device_authorization_endpoint": base + "/device"})
	case "/jwks":
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())}}})
	case "/token":
		_ = r.ParseForm()
		if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "shh" || Challenge(r.Form.Get("code_verifier")) != p.challenge {
			_, _ = w.Write([]byte(`{"error": "invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": p.sign(p.claims), "access_token": "at"})
	case "/userinfo":
		if r.Header.Get("Authorization") != "Bearer at" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(p.userinfo)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *idp) sign(claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	in := enc(map[string]string{"alg": "RS256", "kid": "k1"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (p *idp) idClaims(aud, nonce string) map[string]any {
	return map[string]any{"iss": p.srv.URL, "sub": "u1", "aud": aud, "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce,
		"email": "dana@acme.example", "email_verified": true, "preferred_username": "dana-acme"}
}

func newProvider(t *testing.T, p *idp, mut func(*Config)) *Provider {
	t.Helper()
	cfg := Config{IssuerURL: p.srv.URL, ClientID: "web", ClientSecret: "shh", CLIClientID: "cli", EmailDomains: []string{"acme.example"},
		GroupRepos: map[string][]string{"eng": {"acme/*"}, "sec": {"acme/secret"}}, AuthURLParams: map[string]string{"prompt": "consent"}, HTTP: p.srv.Client()}
	if mut != nil {
		mut(&cfg)
	}
	prov, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return prov
}

// signIn runs the code flow and returns the identity.
func signIn(t *testing.T, p *idp, prov *Provider, verifier, sentVerifier string) (Identity, error) {
	t.Helper()
	u, _ := url.Parse(prov.AuthCodeURL("https://exemplar/oidc/callback", "st", "n1", verifier))
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile email" || q.Get("prompt") != "consent" || q.Get("nonce") != "n1" {
		t.Fatalf("auth URL = %s", u)
	}
	p.challenge = q.Get("code_challenge")
	return prov.Exchange(t.Context(), "good", "https://exemplar/oidc/callback", "n1", sentVerifier)
}

func TestExchange(t *testing.T) {
	p := newIDP(t)
	prov := newProvider(t, p, nil)
	p.claims, p.userinfo = p.idClaims("web", "n1"), map[string]any{"sub": "u1", "groups": []any{"eng", "other"}}
	id, err := signIn(t, p, prov, "v1", "v1")
	if err != nil || id.Username != "dana-acme" || id.Email != "dana@acme.example" || strings.Join(id.Repos, ",") != "acme/*" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if !id.CanRead("acme/app") || id.CanRead("other/app") || !id.Claims().User {
		t.Errorf("access = %+v", id.Claims())
	}

	for _, c := range []struct {
		name string
		mut  func(claims, info map[string]any)
		want string
	}{
		{"other domain", func(c, _ map[string]any) { c["email"] = "eve@evil.example" }, "domain"},
		{"unverified", func(c, _ map[string]any) { c["email_verified"] = false }, "not verified"},
		{"no email", func(c, _ map[string]any) { delete(c, "email"); c["preferred_username"] = "dana" }, "no email"},
		{"wrong nonce", func(c, _ map[string]any) { c["nonce"] = "n2" }, "nonce"},
		{"wrong audience", func(c, _ map[string]any) { c["aud"] = "someone-else" }, "audience"},
		{"userinfo for another subject", func(_, i map[string]any) { i["sub"] = "u2" }, "another subject"},
	} {
		p.claims, p.userinfo = p.idClaims("web", "n1"), map[string]any{"sub": "u1", "groups": []any{"eng"}}
		c.mut(p.claims, p.userinfo)
		if _, err := signIn(t, p, prov, "v1", "v1"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// PKCE: a verifier that does not match the challenge gets no token.
	p.claims = p.idClaims("web", "n1")
	if _, err := signIn(t, p, prov, "v1", "stolen"); err == nil {
		t.Error("a mismatched PKCE verifier signed in")
	}

	// Username falls back to the email's local part; groups from the ID
	// token; allowed groups enforced; email verification can be ignored.
	strict := newProvider(t, p, func(c *Config) {
		c.AllowedGroups, c.IgnoreEmailVerified, c.IgnoreUserInfo = []string{"sec"}, true, true
	})
	p.claims = p.idClaims("web", "n1")
	delete(p.claims, "preferred_username")
	p.claims["email_verified"], p.claims["groups"] = false, []any{"eng"}
	if _, err := signIn(t, p, strict, "v", "v"); !errors.As(err, new(ErrDenied)) || !strings.Contains(err.Error(), "group") {
		t.Errorf("not in an allowed group = %v", err)
	}
	p.claims["groups"] = "sec"
	if id, err := signIn(t, p, strict, "v", "v"); err != nil || id.Username != "dana" || strings.Join(id.Repos, ",") != "acme/secret" {
		t.Errorf("fallbacks = %+v, %v", id, err)
	}
}

func TestVerifyCLIToken(t *testing.T) {
	p := newIDP(t)
	prov := newProvider(t, p, nil)
	c := p.idClaims("cli", "")
	c["groups"] = []any{"sec"}
	got, err := prov.Verify(t.Context(), p.sign(c))
	if err != nil || got.Subject != "dana-acme" || !got.User || !got.CanEvaluate("acme/secret") || got.CanEvaluate("acme/app") {
		t.Fatalf("cli token = %+v, %v", got, err)
	}
	c["aud"] = "unknown"
	if _, err := prov.Verify(t.Context(), p.sign(c)); err == nil {
		t.Error("a token for another client verified")
	}
	if prov.Issuer() != p.srv.URL || prov.Discovery().DeviceEndpoint != p.srv.URL+"/device" {
		t.Errorf("discovery = %+v", prov.Discovery())
	}
}

func TestConfig(t *testing.T) {
	p := newIDP(t)
	for name, mut := range map[string]func(*Config){
		"no group repos": func(c *Config) { c.GroupRepos = nil },
		"bad pattern":    func(c *Config) { c.GroupRepos = map[string][]string{"eng": {"acme"}} },
		"no secret":      func(c *Config) { c.ClientSecret = "" },
		"http issuer":    func(c *Config) { c.IssuerURL = "http://idp.example" },
		"wrong issuer":   func(c *Config) { c.IssuerURL = p.srv.URL + "/other" },
	} {
		cfg := Config{IssuerURL: p.srv.URL, ClientID: "web", ClientSecret: "shh", GroupRepos: map[string][]string{"eng": {"acme/*"}}, HTTP: p.srv.Client()}
		mut(&cfg)
		if _, err := New(t.Context(), cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	env := map[string]string{"EXEMPLAR_OIDC_ISSUER_URL": "https://idp.example", "EXEMPLAR_OIDC_CLIENT_ID": "web", "EXEMPLAR_OIDC_CLIENT_SECRET_FILE": "/s",
		"EXEMPLAR_OIDC_EMAIL_DOMAIN": "acme.example, acme.io", "EXEMPLAR_OIDC_GROUP_REPOS": `{"eng": ["acme/*"]}`, "EXEMPLAR_OIDC_IGNORE_USERINFO": "true",
		"EXEMPLAR_OIDC_SCOPES": "openid,email,offline_access", "EXEMPLAR_OIDC_AUTH_URL_PARAMS": `{"access_type": "offline"}`}
	cfg, ok, err := FromEnv(func(k string) string { return env[k] }, func(string) ([]byte, error) { return []byte("shh\n"), nil })
	if err != nil || !ok || cfg.ClientSecret != "shh" || len(cfg.EmailDomains) != 2 || cfg.GroupRepos["eng"][0] != "acme/*" || !cfg.IgnoreUserInfo ||
		len(cfg.Scopes) != 3 || cfg.AuthURLParams["access_type"] != "offline" {
		t.Errorf("FromEnv = %+v, %v, %v", cfg, ok, err)
	}
	env["EXEMPLAR_OIDC_GROUP_REPOS"] = "{"
	if _, _, err := FromEnv(func(k string) string { return env[k] }, func(string) ([]byte, error) { return nil, nil }); err == nil {
		t.Error("bad JSON accepted")
	}
	if _, ok, _ := FromEnv(func(string) string { return "" }, nil); ok {
		t.Error("OIDC on without an issuer")
	}
}
