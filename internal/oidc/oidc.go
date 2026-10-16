// Package oidc signs people in to exemplar-api with an OpenID Connect
// provider (Okta, Auth0, Azure AD, Keycloak, Google, …), configured the
// way Coder configures OIDC (coder.com/docs/admin/users/oidc-auth):
//
//	EXEMPLAR_OIDC_ISSUER_URL            the provider's issuer; its discovery document is read at startup
//	EXEMPLAR_OIDC_CLIENT_ID             the web client
//	EXEMPLAR_OIDC_CLIENT_SECRET_FILE    its secret
//	EXEMPLAR_OIDC_CLI_CLIENT_ID         a public (native) client for `exemplar login --oidc` (optional)
//	EXEMPLAR_OIDC_EMAIL_DOMAIN          comma-separated domains an email must be in (optional)
//	EXEMPLAR_OIDC_SCOPES                default openid,profile,email
//	EXEMPLAR_OIDC_EMAIL_FIELD           default email
//	EXEMPLAR_OIDC_USERNAME_FIELD        default preferred_username (else the email's local part)
//	EXEMPLAR_OIDC_GROUP_FIELD           default groups
//	EXEMPLAR_OIDC_ALLOWED_GROUPS        comma-separated; sign-in needs one of them (optional)
//	EXEMPLAR_OIDC_GROUP_REPOS           JSON {"<group>": ["owner/name", "owner/*", "*"]}
//	EXEMPLAR_OIDC_IGNORE_EMAIL_VERIFIED treat every email as verified
//	EXEMPLAR_OIDC_IGNORE_USERINFO       use ID token claims only
//	EXEMPLAR_OIDC_AUTH_URL_PARAMS       JSON of extra authorization parameters
//	EXEMPLAR_OIDC_SIGN_IN_TEXT, EXEMPLAR_OIDC_ICON_URL  the sign-in button
//
// With OIDC, repo access comes from the person's groups: the repos the
// groups map to in EXEMPLAR_OIDC_GROUP_REPOS (not GitHub permissions).
// The username stands for the person where Exemplar compares people with
// GitHub (feedback roles, rule-owner teams), so map the username field to
// a claim holding the GitHub username where the IdP has one.
//
// The code flow uses state, nonce and PKCE (S256); the ID token is
// verified against the provider's JWKS (internal/svcauth).
package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/svcauth"
)

// Config is a provider's settings.
type Config struct {
	IssuerURL, ClientID, ClientSecret string
	// CLIClientID is a public client the CLI signs in with (device flow);
	// its ID tokens are accepted by /v1/evaluate too.
	CLIClientID   string
	EmailDomains  []string
	Scopes        []string
	EmailField    string
	UsernameField string
	GroupField    string
	AllowedGroups []string
	// GroupRepos maps an IdP group to the repos its members can read.
	GroupRepos          map[string][]string
	IgnoreEmailVerified bool
	IgnoreUserInfo      bool
	AuthURLParams       map[string]string
	SignInText, IconURL string
	HTTP                *http.Client
}

// Discovery is the provider's OpenID configuration.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	DeviceEndpoint        string `json:"device_authorization_endpoint"`
}

// Provider is a configured, discovered provider.
type Provider struct {
	cfg       Config
	disc      Discovery
	verifiers []*svcauth.Verifier // the web client's, then the CLI's
}

var repoPatternRE = regexp.MustCompile(`^(\*|[A-Za-z0-9_.-]+/(\*|[A-Za-z0-9_.-]+))$`)

// Discover fetches issuer's discovery document.
func Discover(ctx context.Context, c *http.Client, issuer string) (Discovery, error) {
	var d Discovery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return d, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return d, fmt.Errorf("oidc: discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return d, fmt.Errorf("oidc: discovery: %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return d, fmt.Errorf("oidc: discovery: %w", err)
	}
	// The issuer must be the one configured (OpenID Connect Discovery §4.3).
	if strings.TrimSuffix(d.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
		return d, fmt.Errorf("oidc: discovery names issuer %q, not %q", d.Issuer, issuer)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return d, errors.New("oidc: discovery lacks the authorization, token or JWKS endpoint")
	}
	return d, nil
}

// New validates cfg and discovers the provider.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	switch {
	case !strings.HasPrefix(cfg.IssuerURL, "https://") && !strings.HasPrefix(cfg.IssuerURL, "http://127.0.0.1"):
		return nil, errors.New("oidc: the issuer URL must be https")
	case cfg.ClientID == "" || cfg.ClientSecret == "":
		return nil, errors.New("oidc: the client id and secret are required")
	case len(cfg.GroupRepos) == 0:
		return nil, errors.New("oidc: EXEMPLAR_OIDC_GROUP_REPOS must map at least one group to repos: OIDC sign-ins get repo access only from it")
	}
	for g, pats := range cfg.GroupRepos {
		for _, p := range pats {
			if !repoPatternRE.MatchString(p) {
				return nil, fmt.Errorf("oidc: group %q: repo pattern %q must be owner/name, owner/* or *", g, p)
			}
		}
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	if !slices.Contains(cfg.Scopes, "openid") {
		cfg.Scopes = append([]string{"openid"}, cfg.Scopes...)
	}
	if cfg.EmailField == "" {
		cfg.EmailField = "email"
	}
	if cfg.UsernameField == "" {
		cfg.UsernameField = "preferred_username"
	}
	if cfg.GroupField == "" {
		cfg.GroupField = "groups"
	}
	if cfg.SignInText == "" {
		cfg.SignInText = "OpenID Connect"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	for i := range cfg.EmailDomains {
		cfg.EmailDomains[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cfg.EmailDomains[i]), "@"))
	}
	d, err := Discover(ctx, cfg.HTTP, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	p := &Provider{cfg: cfg, disc: d}
	for _, id := range []string{cfg.ClientID, cfg.CLIClientID} {
		if id != "" {
			v := svcauth.NewVerifier(d.Issuer, id, d.JWKSURI)
			v.HTTP = cfg.HTTP
			p.verifiers = append(p.verifiers, v)
		}
	}
	return p, nil
}

// Discovery is what was discovered.
func (p *Provider) Discovery() Discovery { return p.disc }

// SignInText and IconURL are the sign-in button's.
func (p *Provider) SignInText() string { return p.cfg.SignInText }

// IconURL is the sign-in button's icon.
func (p *Provider) IconURL() string { return p.cfg.IconURL }

// Challenge is the PKCE S256 challenge for verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthCodeURL is where a browser signs in.
func (p *Provider) AuthCodeURL(redirect, state, nonce, pkceVerifier string) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {p.cfg.ClientID}, "redirect_uri": {redirect},
		"scope": {strings.Join(p.cfg.Scopes, " ")}, "state": {state}, "nonce": {nonce},
		"code_challenge": {Challenge(pkceVerifier)}, "code_challenge_method": {"S256"},
	}
	for k, v := range p.cfg.AuthURLParams {
		if q.Get(k) == "" {
			q.Set(k, v)
		}
	}
	sep := "?"
	if strings.Contains(p.disc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return p.disc.AuthorizationEndpoint + sep + q.Encode()
}

// Identity is a signed-in person.
type Identity struct {
	// Issuer and Subject identify the account at the provider.
	Issuer, Subject string
	Username        string
	Email           string
	Groups          []string
	// Repos are the repo patterns their groups map to.
	Repos []string
}

// CanRead reports whether the identity may see repo.
func (id Identity) CanRead(repo string) bool { return id.Claims().CanEvaluate(repo) }

// Claims are the identity as service-token claims (for /v1/evaluate).
func (id Identity) Claims() svcauth.Claims {
	perms := make([]string, 0, len(id.Repos))
	for _, r := range id.Repos {
		perms = append(perms, "evaluate:"+r)
	}
	return svcauth.Claims{Subject: id.Username, Permissions: perms, User: true, Issuer: id.Issuer, Sub: id.Subject}
}

// DeniedError is a sign-in the settings refuse; its message is safe to show.
type DeniedError struct{ Why string }

func (e DeniedError) Error() string { return e.Why }

// Exchange completes a browser sign-in: it trades the code, verifies the
// ID token and its nonce, merges userinfo, and applies the settings.
func (p *Provider) Exchange(ctx context.Context, code, redirect, nonce, pkceVerifier string) (Identity, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {p.cfg.ClientID}, "client_secret": {p.cfg.ClientSecret}, "code_verifier": {pkceVerifier},
	}
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := p.post(ctx, p.disc.TokenEndpoint, form, &tok); err != nil {
		return Identity{}, err
	}
	if tok.IDToken == "" {
		return Identity{}, fmt.Errorf("oidc: no ID token (%s)", tok.Error)
	}
	claims := map[string]any{}
	if err := p.verifiers[0].Verify(ctx, tok.IDToken, &claims); err != nil {
		return Identity{}, err
	}
	if n, _ := claims["nonce"].(string); nonce == "" || n != nonce {
		return Identity{}, errors.New("oidc: the ID token's nonce does not match")
	}
	if !p.cfg.IgnoreUserInfo && p.disc.UserinfoEndpoint != "" && tok.AccessToken != "" {
		info, err := p.userinfo(ctx, tok.AccessToken)
		if err != nil {
			return Identity{}, err
		}
		// The userinfo subject must be the ID token's (OIDC Core §5.3.2).
		if info["sub"] != claims["sub"] {
			return Identity{}, errors.New("oidc: userinfo is for another subject")
		}
		for k, v := range info {
			if _, ok := claims[k]; !ok {
				claims[k] = v
			}
		}
	}
	return p.identity(claims)
}

// Issuer implements svcauth.Source.
func (p *Provider) Issuer() string { return p.disc.Issuer }

// Verify implements svcauth.Source: a person's ID token (from the CLI's
// sign-in, or the web client's) for /v1/evaluate.
func (p *Provider) Verify(ctx context.Context, token string) (svcauth.Claims, error) {
	var err error
	for _, v := range p.verifiers {
		claims := map[string]any{}
		if err = v.Verify(ctx, token, &claims); err != nil {
			continue
		}
		id, err := p.identity(claims)
		if err != nil {
			return svcauth.Claims{}, fmt.Errorf("%w: %w", svcauth.ErrInvalid, err)
		}
		return id.Claims(), nil
	}
	return svcauth.Claims{}, err
}

var usernameRE = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// identity applies the claim fields, email checks and group mapping.
func (p *Provider) identity(claims map[string]any) (Identity, error) {
	str := func(k string) string { s, _ := claims[k].(string); return strings.TrimSpace(s) }
	id := Identity{Issuer: p.disc.Issuer, Subject: str("sub"), Email: str(p.cfg.EmailField), Username: str(p.cfg.UsernameField)}
	if id.Subject == "" {
		return Identity{}, DeniedError{"Your sign-in has no subject."}
	}
	if id.Email == "" && strings.Contains(id.Username, "@") {
		id.Email = id.Username
	}
	if id.Email == "" || !strings.Contains(id.Email, "@") {
		return Identity{}, DeniedError{"Your sign-in has no email address."}
	}
	if v, ok := claims["email_verified"].(bool); ok && !v && !p.cfg.IgnoreEmailVerified {
		return Identity{}, DeniedError{"Your email address is not verified with the identity provider."}
	}
	_, domain, _ := strings.Cut(strings.ToLower(id.Email), "@")
	if len(p.cfg.EmailDomains) > 0 && !slices.Contains(p.cfg.EmailDomains, domain) {
		return Identity{}, DeniedError{"Your email domain is not allowed to sign in."}
	}
	if id.Username == "" || strings.Contains(id.Username, "@") {
		id.Username, _, _ = strings.Cut(id.Email, "@")
	}
	id.Username = strings.Trim(usernameRE.ReplaceAllString(id.Username, "-"), "-")
	if id.Username == "" {
		return Identity{}, DeniedError{"Your sign-in has no usable username."}
	}
	switch g := claims[p.cfg.GroupField].(type) {
	case string:
		id.Groups = []string{g}
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				id.Groups = append(id.Groups, s)
			}
		}
	}
	if len(p.cfg.AllowedGroups) > 0 && !slices.ContainsFunc(id.Groups, func(g string) bool { return slices.Contains(p.cfg.AllowedGroups, g) }) {
		return Identity{}, DeniedError{"You are not in a group allowed to sign in."}
	}
	for _, g := range id.Groups {
		id.Repos = append(id.Repos, p.cfg.GroupRepos[g]...)
	}
	slices.Sort(id.Repos)
	id.Repos = slices.Compact(id.Repos)
	return id, nil
}

func (p *Provider) userinfo(ctx context.Context, accessToken string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.disc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: userinfo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: userinfo: %s", resp.Status)
	}
	out := map[string]any{}
	return out, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
}

func (p *Provider) post(ctx context.Context, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("oidc: token: %s", resp.Status)
	}
	return nil
}

// FromEnv reads the EXEMPLAR_OIDC_* settings; ok is false when OIDC is
// not configured (no issuer).
func FromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, bool, error) {
	cfg := Config{
		IssuerURL: getenv("EXEMPLAR_OIDC_ISSUER_URL"), ClientID: getenv("EXEMPLAR_OIDC_CLIENT_ID"), CLIClientID: getenv("EXEMPLAR_OIDC_CLI_CLIENT_ID"),
		EmailField: getenv("EXEMPLAR_OIDC_EMAIL_FIELD"), UsernameField: getenv("EXEMPLAR_OIDC_USERNAME_FIELD"), GroupField: getenv("EXEMPLAR_OIDC_GROUP_FIELD"),
		IgnoreEmailVerified: getenv("EXEMPLAR_OIDC_IGNORE_EMAIL_VERIFIED") == "true", IgnoreUserInfo: getenv("EXEMPLAR_OIDC_IGNORE_USERINFO") == "true",
		SignInText: getenv("EXEMPLAR_OIDC_SIGN_IN_TEXT"), IconURL: getenv("EXEMPLAR_OIDC_ICON_URL"),
	}
	if cfg.IssuerURL == "" {
		return cfg, false, nil
	}
	if f := getenv("EXEMPLAR_OIDC_CLIENT_SECRET_FILE"); f != "" {
		b, err := readFile(f)
		if err != nil {
			return cfg, false, fmt.Errorf("read EXEMPLAR_OIDC_CLIENT_SECRET_FILE: %w", err)
		}
		cfg.ClientSecret = strings.TrimSpace(string(b))
	}
	list := func(k string) []string {
		var out []string
		for s := range strings.SplitSeq(getenv(k), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	cfg.EmailDomains, cfg.Scopes, cfg.AllowedGroups = list("EXEMPLAR_OIDC_EMAIL_DOMAIN"), list("EXEMPLAR_OIDC_SCOPES"), list("EXEMPLAR_OIDC_ALLOWED_GROUPS")
	for _, j := range []struct {
		key string
		out any
	}{{"EXEMPLAR_OIDC_GROUP_REPOS", &cfg.GroupRepos}, {"EXEMPLAR_OIDC_AUTH_URL_PARAMS", &cfg.AuthURLParams}} {
		if v := getenv(j.key); v != "" {
			if err := json.Unmarshal([]byte(v), j.out); err != nil {
				return cfg, false, fmt.Errorf("%s must be JSON: %w", j.key, err)
			}
		}
	}
	return cfg, true, nil
}
