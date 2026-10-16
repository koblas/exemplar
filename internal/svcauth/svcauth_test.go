package svcauth

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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type tenant struct {
	key     *rsa.PrivateKey
	kid     string
	fetches atomic.Int32
	srv     *httptest.Server
}

func newTenant(t *testing.T) *tenant {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tn := &tenant{key: key, kid: "k1"}
	tn.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tn.fetches.Add(1)
		pub := tn.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": tn.kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	}))
	t.Cleanup(tn.srv.Close)
	return tn
}

func (tn *tenant) sign(t *testing.T, hdr, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	in := enc(hdr) + "." + enc(claims)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, tn.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	tn := newTenant(t)
	v := NewVerifier("https://acme.us.auth0.com/", "https://exemplar.example/api", tn.srv.URL)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	v.Now, v.HTTP = func() time.Time { return now }, tn.srv.Client()
	type claims struct {
		Sub   string `json:"sub"`
		Scope string `json:"scope"`
	}
	verify := func(tok string) (claims, error) {
		var c claims
		err := v.Verify(t.Context(), tok, &c)
		return c, err
	}
	hdr := map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"}
	good := func() map[string]any {
		return map[string]any{
			"iss": "https://acme.us.auth0.com/", "sub": "ci-bot@clients", "aud": []string{"https://exemplar.example/api", "x"},
			"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "scope": "evaluate:acme/app", "permissions": []string{"evaluate:tools/*"},
		}
	}
	c, err := verify(tn.sign(t, hdr, good()))
	if err != nil || c.Sub != "ci-bot@clients" || c.Scope != "evaluate:acme/app" {
		t.Fatalf("verify = %+v, %v", c, err)
	}
	if Issuer(tn.sign(t, hdr, good())) != "https://acme.us.auth0.com/" || Issuer("x") != "" {
		t.Error("Issuer")
	}
	pc := Claims{Permissions: []string{"evaluate:acme/app", "evaluate:tools/*"}}
	for repo, want := range map[string]bool{"acme/app": true, "ACME/App": true, "acme/other": false, "tools/lint": true, "other/x": false} {
		if pc.CanEvaluate(repo) != want {
			t.Errorf("CanEvaluate(%s) != %v", repo, want)
		}
	}
	if !(Claims{Permissions: []string{"evaluate:*"}}).CanEvaluate("any/repo") {
		t.Error("evaluate:* refused")
	}

	bad := map[string]func() string{
		"wrong issuer":   func() string { c := good(); c["iss"] = "https://evil.auth0.com/"; return tn.sign(t, hdr, c) },
		"wrong audience": func() string { c := good(); c["aud"] = "https://other"; return tn.sign(t, hdr, c) },
		"expired":        func() string { c := good(); c["exp"] = now.Add(-2 * time.Minute).Unix(); return tn.sign(t, hdr, c) },
		"no exp":         func() string { c := good(); delete(c, "exp"); return tn.sign(t, hdr, c) },
		"not yet":        func() string { c := good(); c["nbf"] = now.Add(5 * time.Minute).Unix(); return tn.sign(t, hdr, c) },
		"no subject":     func() string { c := good(); c["sub"] = ""; return tn.sign(t, hdr, c) },
		"HS256":          func() string { return tn.sign(t, map[string]any{"alg": "HS256", "kid": "k1"}, good()) },
		"alg none":       func() string { return tn.sign(t, map[string]any{"alg": "none", "kid": "k1"}, good()) },
		"tampered": func() string {
			tok := tn.sign(t, hdr, good())
			c := good()
			c["permissions"] = []string{"evaluate:*"}
			b, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(tok, ".")
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString(b) + "." + parts[2]
		},
		"not a JWT": func() string { return "gho_abc" },
	}
	for name, tok := range bad {
		if _, err := verify(tok()); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Expiry within the leeway is accepted.
	c2 := good()
	c2["exp"] = now.Add(-30 * time.Second).Unix()
	if _, err := verify(tn.sign(t, hdr, c2)); err != nil {
		t.Errorf("within leeway: %v", err)
	}

	// An unknown key is not refetched within a minute of the last fetch.
	before := tn.fetches.Load()
	other := map[string]any{"alg": "RS256", "kid": "k2"}
	for range 3 {
		if _, err := verify(tn.sign(t, other, good())); !errors.Is(err, ErrInvalid) {
			t.Errorf("unknown key = %v", err)
		}
	}
	if n := tn.fetches.Load() - before; n != 0 {
		t.Errorf("JWKS fetches for an unknown key within a minute = %d", n)
	}
	// Rotation: the tenant now signs with k2; after a minute it is fetched,
	// once.
	tn.kid = "k2"
	now = now.Add(2 * time.Minute)
	c3 := good()
	c3["exp"] = now.Add(time.Hour).Unix()
	for range 2 {
		if _, err := verify(tn.sign(t, other, c3)); err != nil {
			t.Errorf("rotated key: %v", err)
		}
	}
	if n := tn.fetches.Load() - before; n != 1 {
		t.Errorf("JWKS fetches after rotation = %d", n)
	}

	if !LooksLikeJWT("a.b.c") || LooksLikeJWT("gho_x") {
		t.Error("LooksLikeJWT")
	}
}
