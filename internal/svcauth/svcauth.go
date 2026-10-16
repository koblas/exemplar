// Package svcauth authenticates service callers of exemplar-api (spec §13
// IN-5: a signed service JWT per calling system, PRD FR-S7). Each source
// (internal/auth0, internal/ghactions) verifies RS256 JWTs from one issuer
// against its JWKS with this package's Verifier, and maps the token to
// Claims: who the caller is and which repos it may evaluate. Multi picks
// the source by the token's issuer. Only the standard library is used.
package svcauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Leeway is the clock skew allowed on exp and nbf.
const Leeway = time.Minute

// refreshEvery bounds JWKS fetches when a token names an unknown key.
const refreshEvery = time.Minute

// ErrInvalid is a token that does not verify.
var ErrInvalid = errors.New("svcauth: invalid token")

// Claims are what a verified token says about its caller.
type Claims struct {
	// Subject names the caller: "svc:<sub>" for a service, the username
	// for a person.
	Subject     string
	Permissions []string
	// User is set for a person's token (an OIDC sign-in), so the call is
	// attributed as caller=cli rather than agent.
	User bool
	// Issuer and Sub identify a person's OIDC account (User only), for
	// the GitHub account connected to it.
	Issuer, Sub string
}

// CanEvaluate reports whether the claims allow evaluating repo:
// `evaluate:<owner>/<repo>`, `evaluate:<owner>/*` or `evaluate:*`.
func (c Claims) CanEvaluate(repo string) bool {
	owner, _, _ := strings.Cut(repo, "/")
	for _, p := range c.Permissions {
		switch {
		case p == "evaluate:*", strings.EqualFold(p, "evaluate:"+owner+"/*"), strings.EqualFold(p, "evaluate:"+repo):
			return true
		}
	}
	return false
}

// LooksLikeJWT reports whether token has a JWT's three parts (GitHub
// user tokens do not).
func LooksLikeJWT(token string) bool { return strings.Count(token, ".") == 2 }

// Verifier checks RS256 JWTs from one issuer for one audience.
type Verifier struct {
	Issuer   string
	Audience string
	JWKSURL  string
	HTTP     *http.Client
	Now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewVerifier returns a Verifier with default HTTP and clock.
func NewVerifier(issuer, audience, jwksURL string) *Verifier {
	return &Verifier{Issuer: issuer, Audience: audience, JWKSURL: jwksURL, HTTP: &http.Client{Timeout: 10 * time.Second}, Now: time.Now}
}

// Registered are the claims Verify checks itself.
type Registered struct {
	Iss string          `json:"iss"`
	Sub string          `json:"sub"`
	Aud json.RawMessage `json:"aud"`
	Exp *float64        `json:"exp"`
	Nbf *float64        `json:"nbf"`
}

// Verify checks token's signature, issuer, audience, expiry and subject,
// and decodes its claims into out.
func (v *Verifier) Verify(ctx context.Context, token string, out any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrInvalid
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &hdr); err != nil || hdr.Alg != "RS256" || hdr.Kid == "" {
		return fmt.Errorf("%w: header", ErrInvalid)
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: signature encoding", ErrInvalid)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("%w: signature", ErrInvalid)
	}
	var c Registered
	if err := decodePart(parts[1], &c); err != nil {
		return fmt.Errorf("%w: claims", ErrInvalid)
	}
	now := v.Now()
	switch {
	case c.Iss != v.Issuer:
		return fmt.Errorf("%w: issuer", ErrInvalid)
	case !audience(c.Aud, v.Audience):
		return fmt.Errorf("%w: audience", ErrInvalid)
	case c.Exp == nil || now.After(time.Unix(int64(*c.Exp), 0).Add(Leeway)):
		return fmt.Errorf("%w: expired", ErrInvalid)
	case c.Nbf != nil && now.Add(Leeway).Before(time.Unix(int64(*c.Nbf), 0)):
		return fmt.Errorf("%w: not yet valid", ErrInvalid)
	case c.Sub == "":
		return fmt.Errorf("%w: no subject", ErrInvalid)
	}
	if err := decodePart(parts[1], out); err != nil {
		return fmt.Errorf("%w: claims", ErrInvalid)
	}
	return nil
}

// Issuer reads a token's iss claim without verifying it, to choose the
// source that will.
func Issuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if decodePart(parts[1], &c) != nil {
		return ""
	}
	return c.Iss
}

func decodePart(s string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func audience(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && slices.Contains(many, want)
}

// key returns the signing key kid, fetching the JWKS when it is unknown
// (at most once a minute, so bad tokens cannot hammer the issuer).
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if !v.fetched.IsZero() && v.Now().Sub(v.fetched) < refreshEvery {
		return nil, fmt.Errorf("%w: unknown key", ErrInvalid)
	}
	keys, err := v.fetch(ctx)
	v.fetched = v.Now()
	if err != nil {
		return nil, err
	}
	v.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key", ErrInvalid)
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("svcauth: fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("svcauth: fetch JWKS: %s", resp.Status)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("svcauth: decode JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 {
			continue
		}
		keys[k.Kid] = pub
	}
	return keys, nil
}

// Source verifies one issuer's tokens.
type Source interface {
	Issuer() string
	Verify(ctx context.Context, token string) (Claims, error)
}

// Multi verifies a token with the sources for its issuer. One issuer can
// have several (an Auth0 tenant issues both service access tokens and
// OIDC ID tokens, told apart by audience): the first that accepts wins.
type Multi []Source

// Verify implements api.Services.
func (m Multi) Verify(ctx context.Context, token string) (Claims, error) {
	iss := Issuer(token)
	err := fmt.Errorf("%w: no source for issuer %q", ErrInvalid, iss)
	for _, s := range m {
		if iss == "" || s.Issuer() != iss {
			continue
		}
		c, verr := s.Verify(ctx, token)
		if verr == nil {
			return c, nil
		}
		err = verr
	}
	return Claims{}, err
}
