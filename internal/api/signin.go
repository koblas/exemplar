package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/oidc"
)

// Sign-in sources, configured the way Coder configures them
// (coder.com/docs/admin/users/github-auth and .../oidc-auth):
//
//   - GitHub OAuth, EXEMPLAR_OAUTH2_GITHUB_*: the client, the orgs whose
//     members may sign in (ALLOWED_ORGS) or ALLOW_EVERYONE, and
//     ENTERPRISE_BASE_URL for GHES. Repo access is the person's GitHub
//     permission, checked with their user token.
//   - OpenID Connect, EXEMPLAR_OIDC_* (internal/oidc). Repo access comes
//     from the person's IdP groups, mapped to repos by
//     EXEMPLAR_OIDC_GROUP_REPOS.
//
// Either or both may be on; /login offers those that are.
//
// Connect GitHub: with both on, an OIDC sign-in is followed by connecting
// the person's GitHub account, once (identity_links, by the IdP's issuer
// and subject; one GitHub account per OIDC identity). From then on their
// GitHub login is who they are to Exemplar (feedback roles, rule-owner
// teams, actions, budgets), while repo access stays their OIDC groups'.
// EXEMPLAR_OIDC_GITHUB_LINK is required (the default with both on),
// optional (they may continue without, as their OIDC username) or off.
// EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false keeps GitHub for connecting only.
// /ui/connect-github connects a different account. Every session
// has the signed-in username in the session cookie, and, for the web UI,
// what decides repo access sealed (AES-GCM, key derived from the session
// key) in an HttpOnly cookie sent only to /ui/: the GitHub token, or the
// OIDC repo patterns. Neither reaches the database or a log.

// GitHubAuth is the GitHub OAuth sign-in source.
type GitHubAuth struct {
	ClientID, ClientSecret string
	// AllowedOrgs are the orgs whose members may sign in.
	AllowedOrgs []string
	// AllowEveryone lets any GitHub user sign in.
	AllowEveryone bool
	// SignInDisabled keeps GitHub for connecting to OIDC sign-ins only.
	SignInDisabled bool
}

// GitHub link modes (EXEMPLAR_OIDC_GITHUB_LINK).
const (
	LinkRequired = "required"
	LinkOptional = "optional"
	LinkOff      = "off"
)

// ErrLinkedElsewhere is a GitHub account already connected to another
// OIDC identity.
var ErrLinkedElsewhere = errors.New("api: the GitHub account is connected to another sign-in")

var (
	errSessionTooLarge = errors.New("api: the UI session is too large for a cookie (too many repo patterns)")
	errBadSealed       = errors.New("api: bad sealed value")
)

// Identities stores GitHub accounts connected to OIDC identities.
type Identities interface {
	// GitHubLink returns the login connected to an OIDC identity.
	GitHubLink(ctx context.Context, issuer, subject string) (login string, ok bool, err error)
	// LinkGitHub connects one; ErrLinkedElsewhere when the GitHub account
	// is another identity's.
	LinkGitHub(ctx context.Context, issuer, subject, email string, githubID int64, login string) error
}

// Providers.
const (
	providerGitHub = "github"
	providerOIDC   = "oidc"
)

// uiSession is what the UI cookie holds.
type uiSession struct {
	Provider string   `json:"p"`
	Token    string   `json:"t,omitempty"` // GitHub
	Repos    []string `json:"r,omitempty"` // OIDC repo patterns
	// The OIDC identity, to connect a different GitHub account.
	Issuer   string `json:"i,omitempty"`
	Subject  string `json:"s,omitempty"`
	Email    string `json:"e,omitempty"`
	Username string `json:"u,omitempty"`
}

const (
	oidcStateCookie = "exemplar_oidc"
	linkCookie      = "exemplar_link"
)

// pendingLink is an OIDC sign-in waiting for GitHub to be connected.
type pendingLink struct {
	ID     uiSession `json:"ID"`
	Return string    `json:"Return"`
	Exp    int64     `json:"Exp"`
}

// linking reports whether OIDC sign-ins connect GitHub.
func (s *Server) linking() bool {
	return s.cfg.OIDC != nil && s.cfg.GitHub != nil && s.cfg.Identities != nil && s.cfg.GitHubLink != LinkOff
}

// validReturn accepts only this server's feedback links and UI pages.
func validReturn(ret string) bool {
	return (strings.HasPrefix(ret, "/f/") || strings.HasPrefix(ret, "/ui/")) && !strings.HasPrefix(ret, "//") && !strings.ContainsAny(ret, "\\\r\n")
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in · Exemplar</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:24rem;margin:4rem auto;padding:0 1rem}` +
	`a.b{display:flex;gap:.5rem;align-items:center;justify-content:center;border:1px solid #8886;border-radius:6px;padding:.6rem;margin:.75rem ` +
	`0;text-decoration:none;color:inherit}img{width:20px;height:20px}</style>
</head><body><h1>Sign in to Exemplar</h1>
{{if .GitHub}}<a class="b" href="/oauth/start?return={{.Return}}">Sign in with GitHub</a>{{end}}
{{if .OIDC}}<a class="b" href="/oidc/start?return={{.Return}}">{{if .Icon}}<img src="{{.Icon}}" alt="">{{end}}Sign in with {{.OIDCText}}</a>{{end}}
</body></html>
`))

// loginPage offers the configured sources; with one, it goes straight there.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	ret := r.URL.Query().Get("return")
	if !validReturn(ret) {
		http.Error(w, "bad return path", http.StatusBadRequest)
		return
	}
	q := url.QueryEscape(ret)
	github := s.cfg.GitHub != nil && !s.cfg.GitHub.SignInDisabled
	switch {
	case s.cfg.OIDC == nil:
		http.Redirect(w, r, "/oauth/start?return="+q, http.StatusFound)
		return
	case !github:
		http.Redirect(w, r, "/oidc/start?return="+q, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// html/template escapes the return path inside the hrefs.
	_ = loginPage.Execute(w, map[string]any{
		"GitHub": github, "OIDC": true, "Return": ret,
		// safeIcon admits only an operator-configured https URL with no quote,
		// angle bracket or space, so it cannot break out of the src attribute.
		"OIDCText": s.cfg.OIDC.SignInText(), "Icon": template.URL(safeIcon(s.cfg.OIDC.IconURL())), //nolint:gosec // G203: safeIcon validates the URL (see above)
	})
}

// safeIcon accepts only an https icon URL.
func safeIcon(u string) string {
	if strings.HasPrefix(u, "https://") && !strings.ContainsAny(u, "\"'<> ") {
		return u
	}
	return ""
}

// githubAllowed applies EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS.
func (s *Server) githubAllowed(ctx context.Context, token, login string) (bool, error) {
	if s.cfg.GitHub.AllowEveryone {
		return true, nil
	}
	ok, err := s.cfg.Users.InOrgs(ctx, token, login, s.cfg.GitHub.AllowedOrgs)
	if err != nil {
		return false, fmt.Errorf("check org membership: %w", err)
	}
	return ok, nil
}

// startSession sets the session cookie and, when ui (or always for OIDC,
// whose access needs no token), the sealed UI cookie.
func (s *Server) startSession(w http.ResponseWriter, login string, ui uiSession, forUI bool) error {
	exp := strconv.FormatInt(s.cfg.Now().Add(sessionTTL).Unix(), 10)
	enc := base64.RawURLEncoding.EncodeToString([]byte(login))
	s.setCookie(w, sessionCookie, enc+"."+exp+"."+s.mac("session", enc, exp), "/", sessionTTL)
	if !forUI && ui.Provider == providerGitHub {
		return nil
	}
	data, err := json.Marshal(ui)
	if err != nil {
		return fmt.Errorf("encode ui session: %w", err)
	}
	sealed, err := s.seal(login, data)
	if err != nil {
		return err
	}
	if len(sealed) > 3800 {
		return errSessionTooLarge
	}
	s.setCookie(w, uiTokenCookie, sealed, "/ui/", sessionTTL)
	return nil
}

// oidcStart sends the person to the OIDC provider, with state, nonce and
// a PKCE verifier kept in a sealed cookie for the callback.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	ret := r.URL.Query().Get("return")
	if !validReturn(ret) {
		http.Error(w, "bad return path", http.StatusBadRequest)
		return
	}
	st := oidcState{Return: ret, Exp: s.cfg.Now().Add(stateTTL).Unix()}
	for _, p := range []*string{&st.State, &st.Nonce, &st.Verifier} {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		*p = base64.RawURLEncoding.EncodeToString(b)
	}
	data, err := json.Marshal(st)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sealed, err := s.seal("oidc-state", data)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, oidcStateCookie, sealed, "/oidc/", stateTTL)
	http.Redirect(w, r, s.cfg.OIDC.AuthCodeURL(s.cfg.PublicURL+"/oidc/callback", st.State, st.Nonce, st.Verifier), http.StatusFound)
}

type oidcState struct {
	State    string `json:"State"`
	Nonce    string `json:"Nonce"`
	Verifier string `json:"Verifier"`
	Return   string `json:"Return"`
	Exp      int64  `json:"Exp"`
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { s.render(w, http.StatusForbidden, view{Heading: "Sign-in failed", Message: msg}) }
	if s.cfg.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie(oidcStateCookie)
	var st oidcState
	if err == nil {
		var data []byte
		if data, err = s.open("oidc-state", c.Value); err == nil {
			err = json.Unmarshal(data, &st)
		}
	}
	if err != nil || st.State == "" || !hmac.Equal([]byte(st.State), []byte(r.URL.Query().Get("state"))) {
		fail("The sign-in did not start here, or took too long. Try again.")
		return
	}
	if s.cfg.Now().Unix() > st.Exp || !validReturn(st.Return) {
		fail("The sign-in took too long. Try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail("The identity provider refused the sign-in.")
		return
	}
	id, err := s.cfg.OIDC.Exchange(r.Context(), r.URL.Query().Get("code"), s.cfg.PublicURL+"/oidc/callback", st.Nonce, st.Verifier)
	var denied oidc.DeniedError
	switch {
	case errors.As(err, &denied):
		fail(denied.Why)
		return
	case err != nil:
		s.cfg.Log.Warn("oidc sign-in", "err", err)
		fail("The identity provider did not confirm who you are. Try again.")
		return
	}
	s.setCookie(w, oidcStateCookie, "", "/oidc/", -time.Second)
	ui := uiSession{Provider: providerOIDC, Repos: id.Repos, Issuer: id.Issuer, Subject: id.Subject, Email: id.Email, Username: id.Username}
	login := oidc.UnlinkedLogin(id.Username)
	if s.linking() {
		linked, ok, err := s.cfg.Identities.GitHubLink(r.Context(), id.Issuer, id.Subject)
		if err != nil {
			s.cfg.Log.Error("oidc sign-in: connected GitHub account not read", "err", err)
			fail("Sign-in could not be completed. Try again.")
			return
		}
		if !ok {
			// Connect GitHub first; the sign-in waits in a sealed cookie.
			s.pendLink(w, r, ui, st.Return)
			return
		}
		login = linked
	}
	if err := s.startSession(w, login, ui, true); err != nil {
		s.cfg.Log.Warn("oidc session", "err", err)
		fail("Sign-in could not be completed: " + err.Error())
		return
	}
	http.Redirect(w, r, st.Return, http.StatusFound)
}

var connectPage = template.Must(template.New("connect").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Connect GitHub · Exemplar</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:28rem;margin:4rem auto;padding:0 1rem}a.b{display:block;text-align:center;border:1px solid ` +
	`#8886;border-radius:6px;padding:.6rem;margin:.75rem 0;text-decoration:none;color:inherit}</style>
</head><body><h1>Connect your GitHub account</h1>
<p>Signed in as {{.Username}}. Exemplar compares people with GitHub logins and teams: who wrote or reviewed a pull request, and who owns a rule. Connect the GitHub account ` +
	`you use for code review. You do this once.</p>
<a class="b" href="/connect/github">Connect GitHub</a>
{{if .Optional}}<a class="b" href="/connect/skip">Continue without</a>{{end}}
</body></html>
`))

// pendLink keeps the OIDC sign-in and asks for GitHub: straight to GitHub
// when connecting is required, or a page that offers to skip.
func (s *Server) pendLink(w http.ResponseWriter, r *http.Request, ui uiSession, ret string) {
	data, err := json.Marshal(pendingLink{ID: ui, Return: ret, Exp: s.cfg.Now().Add(stateTTL).Unix()})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sealed, err := s.seal("oidc-link", data)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, linkCookie, sealed, "/", stateTTL)
	if s.cfg.GitHubLink == LinkOptional {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = connectPage.Execute(w, map[string]any{"Username": ui.Username, "Optional": true})
		return
	}
	s.githubAuthorize(w, r, ret, modeLink)
}

// pending reads the waiting OIDC sign-in.
func (s *Server) pending(r *http.Request) (pendingLink, bool) {
	c, err := r.Cookie(linkCookie)
	if err != nil {
		return pendingLink{}, false
	}
	data, err := s.open("oidc-link", c.Value)
	var p pendingLink
	if err != nil || json.Unmarshal(data, &p) != nil || s.cfg.Now().Unix() > p.Exp || !validReturn(p.Return) || p.ID.Subject == "" {
		return pendingLink{}, false
	}
	return p, true
}

func (s *Server) connectGitHub(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pending(r)
	if !s.linking() || !ok {
		s.render(w, http.StatusForbidden, view{Heading: "Sign in first", Message: "Your sign-in expired. Sign in again."})
		return
	}
	s.githubAuthorize(w, r, p.Return, modeLink)
}

func (s *Server) connectSkip(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pending(r)
	if !s.linking() || !ok || s.cfg.GitHubLink != LinkOptional {
		s.render(w, http.StatusForbidden, view{Heading: "Sign in first", Message: "Connecting GitHub is required here, or your sign-in expired."})
		return
	}
	s.setCookie(w, linkCookie, "", "/", -time.Second)
	if err := s.startSession(w, oidc.UnlinkedLogin(p.ID.Username), p.ID, true); err != nil {
		s.render(w, http.StatusForbidden, view{Heading: "Sign-in failed", Message: "Sign-in could not be completed: " + err.Error()})
		return
	}
	http.Redirect(w, r, p.Return, http.StatusFound)
}

// Relink connects a different GitHub account to the signed-in OIDC
// identity (GET /ui/connect-github, mounted beside the UI).
func (s *Server) Relink(w http.ResponseWriter, r *http.Request) {
	login := s.login(r)
	c, err := r.Cookie(uiTokenCookie)
	var ui uiSession
	if err == nil {
		var data []byte
		if data, err = s.open(login, c.Value); err == nil {
			err = json.Unmarshal(data, &ui)
		}
	}
	if login == "" || err != nil || ui.Provider != providerOIDC || !s.linking() {
		http.Redirect(w, r, "/login?return="+url.QueryEscape("/ui/"), http.StatusFound)
		return
	}
	s.pendLink(w, r, ui, "/ui/")
}

// finishLink connects the GitHub account to the waiting OIDC sign-in and
// starts the session as the GitHub login.
func (s *Server) finishLink(w http.ResponseWriter, r *http.Request, login string, githubID int64) {
	fail := func(msg string) {
		s.render(w, http.StatusForbidden, view{Heading: "Could not connect GitHub", Message: msg})
	}
	p, ok := s.pending(r)
	if !s.linking() || !ok {
		fail("Your sign-in expired. Sign in again.")
		return
	}
	err := s.cfg.Identities.LinkGitHub(r.Context(), p.ID.Issuer, p.ID.Subject, p.ID.Email, githubID, login)
	switch {
	case errors.Is(err, ErrLinkedElsewhere):
		fail("That GitHub account is already connected to another sign-in.")
		return
	case err != nil:
		s.cfg.Log.Error("connect github", "err", err)
		fail("It could not be saved. Try again.")
		return
	}
	s.cfg.Log.Info("github connected", "issuer", p.ID.Issuer, "username", p.ID.Username, "github", login)
	s.setCookie(w, linkCookie, "", "/", -time.Second)
	if err := s.startSession(w, login, p.ID, true); err != nil {
		fail("Sign-in could not be completed: " + err.Error())
		return
	}
	http.Redirect(w, r, p.Return, http.StatusFound)
}

// uiKey is the AES key sealed cookies use.
func (s *Server) uiKey() []byte {
	m := hmac.New(sha256.New, s.cfg.SessionKey)
	m.Write([]byte("ui-token-cookie"))
	return m.Sum(nil)
}

// seal encrypts plain for a cookie, bound to aad (the login, or a purpose).
func (s *Server) seal(aad string, plain []byte) (string, error) {
	block, err := aes.NewCipher(s.uiKey())
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, []byte(aad))), nil
}

func (s *Server) open(aad, value string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode sealed value: %w", err)
	}
	block, err := aes.NewCipher(s.uiKey())
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return nil, errBadSealed
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("open sealed value: %w", err)
	}
	return plain, nil
}

// Access reports whether the viewer can read a repo.
type Access func(ctx context.Context, repo string) (bool, error)

// UISession returns the web UI's viewer: the signed-in login and how to
// check their repo access (their GitHub permission, or their OIDC groups'
// repos), or ok false when the session is missing or expired.
func (s *Server) UISession(r *http.Request) (string, Access, bool) {
	login := s.login(r)
	c, err := r.Cookie(uiTokenCookie)
	if login == "" || err != nil {
		return "", nil, false
	}
	data, err := s.open(login, c.Value)
	if err != nil {
		return "", nil, false
	}
	var ui uiSession
	if json.Unmarshal(data, &ui) != nil {
		return "", nil, false
	}
	switch {
	case ui.Provider == providerGitHub && s.cfg.GitHub != nil && ui.Token != "":
		token := ui.Token
		return login, func(ctx context.Context, repo string) (bool, error) { return s.cfg.Users.CanRead(ctx, token, repo) }, true
	case ui.Provider == providerOIDC && s.cfg.OIDC != nil:
		claims := oidc.Identity{Repos: ui.Repos}.Claims()
		return login, func(_ context.Context, repo string) (bool, error) { return claims.CanEvaluate(repo), nil }, true
	}
	return "", nil, false
}

// SignOut clears the session and the UI cookie.
func (s *Server) SignOut(w http.ResponseWriter) {
	s.setCookie(w, sessionCookie, "", "/", -time.Second)
	s.setCookie(w, uiTokenCookie, "", "/ui/", -time.Second)
}
