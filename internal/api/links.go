// Package api is exemplar-api's HTTP surface: the signed feedback
// links, sign-in, and /v1/evaluate.
//
//	GET  /f/{finding}/{reason}?s=SIG   confirmation page (signs in first)
//	POST /f/{finding}/{reason}         records the feedback
//	GET  /login                        the sign-in sources (signin.go)
//	GET  /oauth/start, /oauth/callback GitHub OAuth
//	GET  /oidc/start, /oidc/callback   OpenID Connect
//
// A GET never records anything: mail and chat link previews fetch links
// on their own. Recording needs the confirmation form's POST, with a token
// bound to the signed-in login, the finding and the reason. Identity comes
// from sign-in, never from the link. The server holds no installation
// credentials: it enqueues a feedback_link job and the worker derives the
// role and records the event.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/ghuser"
	"github.com/koblas/exemplar/internal/oidc"
	"github.com/koblas/exemplar/internal/store"
)

// Finding is what the confirmation page shows: where, never the text.
type Finding struct {
	RuleID string
	Repo   string
	PR     int
}

// Config wires the link server.
type Config struct {
	Links *feedbacklink.Signer
	// SessionKey signs session, state and form cookies; at least 32 bytes.
	SessionKey []byte
	// PublicURL is this server's https base, for the OAuth redirect.
	PublicURL string
	// GitHub and OIDC are the sign-in sources (signin.go); at least one.
	GitHub *GitHubAuth
	OIDC   *oidc.Provider
	// Users checks GitHub user tokens (default: ghuser on GitHubAPI).
	Users *ghuser.Checker
	// Identities stores GitHub accounts connected to OIDC sign-ins, and
	// GitHubLink says whether connecting is required (the default with
	// both sources), optional or off.
	Identities Identities
	GitHubLink string
	// GitHubWeb and GitHubAPI are https://github.com/ and
	// https://api.github.com/ (GHES: https://<host>/ and https://<host>/api/v3/).
	GitHubWeb, GitHubAPI string
	// Lookup finds a finding; an error is shown as "not found".
	Lookup func(ctx context.Context, id int64) (Finding, error)
	// Enqueue records the confirmed feedback (a River insert).
	Enqueue func(ctx context.Context, a store.FeedbackLinkArgs) error
	HTTP    *http.Client
	Log     *slog.Logger
	Now     func() time.Time
}

// Session lifetime and the OAuth round trip's window.
const (
	sessionTTL = 8 * time.Hour
	stateTTL   = 10 * time.Minute
	maxForm    = 8 << 10
	maxText    = 1000
)

const (
	sessionCookie = "exemplar_session"
	stateCookie   = "exemplar_oauth"
	uiTokenCookie = "exemplar_ui" //nolint:gosec // G101: a cookie name, not a credential
)

// Server serves the links.
type Server struct{ cfg Config }

// New validates cfg.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Links == nil || cfg.Lookup == nil || cfg.Enqueue == nil:
		return nil, errors.New("api: Links, Lookup and Enqueue are required")
	case len(cfg.SessionKey) < 32:
		return nil, errors.New("api: the session key must be at least 32 bytes")
	case cfg.GitHub == nil && cfg.OIDC == nil:
		return nil, errors.New("api: configure a sign-in source: GitHub (EXEMPLAR_OAUTH2_GITHUB_*) or OIDC (EXEMPLAR_OIDC_*)")
	case cfg.GitHub != nil && (cfg.GitHub.ClientID == "" || cfg.GitHub.ClientSecret == ""):
		return nil, errors.New("api: the GitHub OAuth client id and secret are required")
	case cfg.GitHub != nil && !cfg.GitHub.AllowEveryone && len(cfg.GitHub.AllowedOrgs) == 0:
		return nil, errors.New("api: GitHub sign-in needs allowed orgs (EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS) or EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true")
	case !strings.HasPrefix(cfg.PublicURL, "https://") && !strings.HasPrefix(cfg.PublicURL, "http://127.0.0.1"):
		return nil, errors.New("api: the public URL must be https")
	}
	cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	if cfg.GitHubWeb == "" {
		cfg.GitHubWeb = "https://github.com/"
	}
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = "https://api.github.com/"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Users == nil {
		cfg.Users = ghuser.New(cfg.GitHubAPI, cfg.HTTP)
	}
	switch cfg.GitHubLink {
	case "":
		cfg.GitHubLink = LinkRequired
	case LinkRequired, LinkOptional, LinkOff:
	default:
		return nil, fmt.Errorf("api: EXEMPLAR_OIDC_GITHUB_LINK must be required, optional or off, not %q", cfg.GitHubLink)
	}
	if cfg.GitHub != nil && cfg.GitHub.SignInDisabled && (cfg.OIDC == nil || cfg.GitHubLink == LinkOff) {
		return nil, errors.New("api: GitHub with sign-in off is only for connecting to OIDC sign-ins; turn OIDC and EXEMPLAR_OIDC_GITHUB_LINK on")
	}
	if cfg.OIDC != nil && cfg.GitHub != nil && cfg.GitHubLink != LinkOff && cfg.Identities == nil {
		return nil, errors.New("api: connecting GitHub to OIDC sign-ins needs Identities")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Server{cfg: cfg}, nil
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /f/{finding}/{reason}", s.confirm)
	mux.HandleFunc("POST /f/{finding}/{reason}", s.record)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /oauth/start", s.start)
	mux.HandleFunc("GET /oauth/callback", s.callback)
	mux.HandleFunc("GET /oidc/start", s.oidcStart)
	mux.HandleFunc("GET /oidc/callback", s.oidcCallback)
	mux.HandleFunc("GET /connect/github", s.connectGitHub)
	mux.HandleFunc("GET /connect/skip", s.connectSkip)
	return headers(mux)
}

// headers sets the security headers every page carries. The link holds
// its signature, so no Referer leaves the page.
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// mac signs parts under a purpose.
func (s *Server) mac(purpose string, parts ...string) string {
	m := hmac.New(sha256.New, s.cfg.SessionKey)
	m.Write([]byte(purpose))
	for _, p := range parts {
		m.Write([]byte{0})
		m.Write([]byte(p))
	}
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) ok(purpose, sig string, parts ...string) bool {
	return hmac.Equal([]byte(sig), []byte(s.mac(purpose, parts...)))
}

// login is the signed-in GitHub login, or "".
func (s *Server) login(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	login, rest, ok1 := strings.Cut(c.Value, ".")
	exp, sig, ok2 := strings.Cut(rest, ".")
	if !ok1 || !ok2 || !s.ok("session", sig, login, exp) {
		return ""
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || s.cfg.Now().Unix() > n {
		return ""
	}
	name, err := base64.RawURLEncoding.DecodeString(login)
	if err != nil {
		return ""
	}
	return string(name)
}

func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, ttl time.Duration) {
	// Secure follows PublicURL so plain-http local development still works;
	// HttpOnly and SameSite=Lax are always set.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is on whenever PublicURL is https (see above)
		Name: name, Value: value, Path: path, MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Exemplar feedback</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem}textarea{width:100%;min-height:6rem}button{font:inherit;padding:.4rem 1rem}</style>
</head><body>
<h1>{{.Heading}}</h1>
{{if .Finding}}<p>Rule <code>{{.Finding.RuleID}}</code> on <a href="{{.PRLink}}">{{.Finding.Repo}}#{{.Finding.PR}}</a>.</p>{{end}}
{{if .Form}}<form method="post">
<input type="hidden" name="s" value="{{.Sig}}"><input type="hidden" name="t" value="{{.Token}}">
<p><label for="text">Anything to add? (optional)</label><br><textarea id="text" name="text" maxlength="1000"></textarea></p>
<p><button type="submit">Record: {{.Label}}</button></p>
</form>{{end}}
{{if .Message}}<p>{{.Message}}</p>{{end}}
</body></html>
`))

type view struct {
	Heading, Label, Sig, Token, Message, PRLink string
	Finding                                     *Finding
	Form                                        bool
}

func (s *Server) render(w http.ResponseWriter, code int, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := page.Execute(w, v); err != nil {
		s.cfg.Log.Warn("render", "err", err)
	}
}

func label(reason string) string {
	for _, r := range feedbacklink.Reasons {
		if r.Reason == reason {
			return r.Label
		}
	}
	return reason
}

// link verifies the request's link and finds its finding.
func (s *Server) link(w http.ResponseWriter, r *http.Request, sig string) (int64, string, *Finding, bool) {
	reason := r.PathValue("reason")
	id, err := s.cfg.Links.Verify(r.PathValue("finding"), reason, sig)
	if err != nil {
		s.render(w, http.StatusNotFound, view{Heading: "This link isn't valid", Message: "It may have been changed or copied incompletely."})
		return 0, "", nil, false
	}
	f, err := s.cfg.Lookup(r.Context(), id)
	if err != nil {
		s.render(w, http.StatusNotFound, view{Heading: "Finding not found", Message: "It may have expired under the retention policy."})
		return 0, "", nil, false
	}
	return id, reason, &f, true
}

func (s *Server) prLink(f *Finding) string {
	return fmt.Sprintf("%s%s/pull/%d", s.cfg.GitHubWeb, f.Repo, f.PR)
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	sig := r.URL.Query().Get("s")
	id, reason, f, ok := s.link(w, r, sig)
	if !ok {
		return
	}
	login := s.login(r)
	if login == "" {
		http.Redirect(w, r, "/login?return="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	s.render(w, http.StatusOK, view{
		Heading: "Feedback: " + label(reason), Label: label(reason), Finding: f, PRLink: s.prLink(f),
		Form: true, Sig: sig, Token: s.mac("form", login, strconv.FormatInt(id, 10), reason),
	})
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, reason, f, ok := s.link(w, r, r.PostForm.Get("s"))
	if !ok {
		return
	}
	login := s.login(r)
	if login == "" || !s.ok("form", r.PostForm.Get("t"), login, strconv.FormatInt(id, 10), reason) {
		s.render(w, http.StatusForbidden, view{Heading: "Please open the link again", Message: "Your sign-in expired or the form did not come from this page."})
		return
	}
	text := strings.TrimSpace(r.PostForm.Get("text"))
	if rs := []rune(text); len(rs) > maxText {
		text = string(rs[:maxText])
	}
	if err := s.cfg.Enqueue(r.Context(), store.FeedbackLinkArgs{FindingID: id, Reason: reason, Actor: login, At: s.cfg.Now().UTC(), Text: text}); err != nil {
		s.cfg.Log.Error("feedback not enqueued", "finding", id, "err", err)
		s.render(w, http.StatusServiceUnavailable, view{Heading: "Not recorded", Message: "Something went wrong on our side. Please try again in a minute."})
		return
	}
	s.render(w, http.StatusOK, view{
		Heading: "Recorded: " + label(reason), Finding: f, PRLink: s.prLink(f),
		Message: "Thanks. Rule owners see feedback in their weekly digest.",
	})
}

// start sends the person to GitHub to sign in, remembering where to return.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	ret := r.URL.Query().Get("return")
	if s.cfg.GitHub == nil || s.cfg.GitHub.SignInDisabled {
		http.NotFound(w, r)
		return
	}
	if !validReturn(ret) {
		http.Error(w, "bad return path", http.StatusBadRequest)
		return
	}
	s.githubAuthorize(w, r, ret, modeSignIn)
}

// GitHub OAuth purposes, carried in the state.
const (
	modeSignIn = "signin"
	modeLink   = "link" // connect GitHub to an OIDC sign-in
)

// githubAuthorize sends the person to GitHub, remembering where to
// return and why.
func (s *Server) githubAuthorize(w http.ResponseWriter, r *http.Request, ret, mode string) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	n := base64.RawURLEncoding.EncodeToString(nonce)
	exp := strconv.FormatInt(s.cfg.Now().Add(stateTTL).Unix(), 10)
	enc := base64.RawURLEncoding.EncodeToString([]byte(ret))
	state := enc + "." + exp + "." + mode + "." + s.mac("state", enc, exp, mode, n)
	s.setCookie(w, stateCookie, n, "/oauth/", stateTTL)
	q := url.Values{"client_id": {s.cfg.GitHub.ClientID}, "redirect_uri": {s.cfg.PublicURL + "/oauth/callback"}, "state": {state}, "allow_signup": {"false"}}
	http.Redirect(w, r, s.cfg.GitHubWeb+"login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		s.render(w, http.StatusForbidden, view{Heading: "Sign-in failed", Message: msg})
	}
	if s.cfg.GitHub == nil {
		http.NotFound(w, r)
		return
	}
	nonce, err := r.Cookie(stateCookie)
	parts := strings.Split(r.URL.Query().Get("state"), ".")
	if err != nil || len(parts) != 4 || !s.ok("state", parts[3], parts[0], parts[1], parts[2], nonce.Value) {
		fail("The sign-in did not start here, or took too long. Open the link again.")
		return
	}
	if exp, err := strconv.ParseInt(parts[1], 10, 64); err != nil || s.cfg.Now().Unix() > exp {
		fail("The sign-in took too long. Open the link again.")
		return
	}
	retb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || !validReturn(string(retb)) {
		fail("Open the link again.")
		return
	}
	mode := parts[2]
	if mode != modeLink && (mode != modeSignIn || s.cfg.GitHub.SignInDisabled) {
		fail("Open the link again.")
		return
	}
	login, ghID, token, err := s.exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.cfg.Log.Warn("github sign-in", "err", err)
		fail("GitHub did not confirm who you are. Open the link again.")
		return
	}
	if ok, err := s.githubAllowed(r.Context(), token, login); err != nil {
		s.cfg.Log.Warn("github sign-in: org membership not checked", "err", err)
		fail("Your organization membership could not be checked. Try again.")
		return
	} else if !ok {
		fail("Your GitHub account is not in an organization allowed to sign in.")
		return
	}
	s.setCookie(w, stateCookie, "", "/oauth/", -time.Second)
	if mode == modeLink {
		s.finishLink(w, r, login, ghID)
		return
	}
	if err := s.startSession(w, login, uiSession{Provider: providerGitHub, Token: token}, strings.HasPrefix(string(retb), "/ui/")); err != nil {
		fail("Sign-in could not be completed. Try again.")
		return
	}
	// retb is from the HMAC-checked state and passed validReturn: a same-origin /f/ or /ui/ path.
	http.Redirect(w, r, string(retb), http.StatusFound) //nolint:gosec // G710: validReturn allows only local paths
}

// exchange trades the OAuth code for a user token and reads the login and
// account id.
func (s *Server) exchange(ctx context.Context, code string) (string, int64, string, error) {
	if code == "" {
		return "", 0, "", errors.New("no code")
	}
	form := url.Values{
		"client_id": {s.cfg.GitHub.ClientID}, "client_secret": {s.cfg.GitHub.ClientSecret}, "code": {code},
		"redirect_uri": {s.cfg.PublicURL + "/oauth/callback"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.GitHubWeb+"login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := s.do(req, &tok); err != nil {
		return "", 0, "", err
	}
	if tok.AccessToken == "" {
		return "", 0, "", fmt.Errorf("token exchange: %s", tok.Error)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.GitHubAPI+"user", nil)
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	var u struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"`
	}
	if err := s.do(req, &u); err != nil {
		return "", 0, "", err
	}
	if u.Login == "" || !strings.EqualFold(u.Type, "User") {
		return "", 0, "", errors.New("not a user account")
	}
	return u.Login, u.ID, tok.AccessToken, nil
}

func (s *Server) do(req *http.Request, out any) error {
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, resp.Status)
	}
	return json.Unmarshal(body, out)
}
