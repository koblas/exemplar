// Package setup is exemplar-api's first-run setup: it registers the GitHub
// App from a manifest and writes the Secrets (exemplar-github-app and
// whichever others are missing) that every other workload waits for.
//
// It runs only while exemplar-api has no App (EXEMPLAR_GH_APP_ID unset), at
// /setup on the public URL. The operator signs in with a bootstrap token
// that setup writes to the exemplar-setup Secret, so being able to read
// Secrets in the namespace is the authorization: nobody who finds the
// public host name first can take the App's private key.
//
// The flow (https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest):
//
//  1. GET /setup renders a form that posts the App manifest (permissions,
//     events, webhook and callback URLs) to GitHub with a signed state,
//     also set as a cookie.
//  2. GitHub creates the App and redirects to /setup/created with a code.
//     The handler checks the state against the cookie and exchanges the
//     code for the App's id, key and secrets, which go straight into
//     exemplar-github-app.
//  3. The operator installs the App on the config repo's owner (an
//     organization or a user account; the manifest form posts to whichever
//     one's App settings); each load looks for the installation and records
//     it in the Secret.
//  4. The operator picks the models (an Anthropic key, written to
//     exemplar-providers, or their own Secret) and presses Finish. Every
//     exemplar-api pod sees setup complete in the Secrets and leaves setup
//     mode in place (Installed), without a restart.
//
// Prepare writes, for the in-cluster Postgres, its password and URL before
// anyone signs in, so the database starts during steps 1–3. Finish writes
// the root key last: every other workload mounts it, so they start only
// once the App and the models are in place.
//
// The page shows three numbered steps (create, install, finish), the first
// unfinished one open. It doesn't update live: the CSP allows no script,
// every action posts or redirects back to GET /setup, and a reload shows
// the current state; only the closing "finishing" page refreshes itself.
// Errors that leave the flow in place (the App not installed yet) show
// inline on their step and are not kept, so a reload clears them; the rest
// get a page naming the next action. The pages and every string they show
// are in pages/.
//
// State lives in the Secrets (state.go), never in this process, so any pod
// answers any request. Credentials never reach a log.
package setup

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/gh"
)

// Kinds of App setup registers.
const (
	KindReview  = "review"  // exemplar-github-app: reviews, config PRs, sign-in
	KindExports = "exports" // exemplar-exports-app: rule exports, optional
)

const (
	stateCookie   = "exemplar_setup_state"
	sessionCookie = "exemplar_setup_session"
	stateTTL      = time.Hour // GitHub's codes last an hour too
	sessionTTL    = 12 * time.Hour
)

// Errors on the last step, shown in place (pages/status.html).
const (
	providerKeyMissing = "key-missing"
	providerKeyInvalid = "key-invalid"
)

var (
	errConfig = errors.New("setup: config")
	errState  = errors.New("setup: unknown or expired state")
)

// Config is what setup needs to describe the App.
type Config struct {
	// URLs are the public addresses the App registers; setup answers at
	// URLs.API too, and GitHub redirects there after creating the App.
	URLs envcfg.URLs
	// ConfigRepo is exemplar-config (owner/name); the App belongs to its owner.
	ConfigRepo string
	// GitHubWeb and GitHubAPI are https://github.com/ and
	// https://api.github.com/, or a GHES host's.
	GitHubWeb, GitHubAPI string
	// Namespace is the installation's namespace, which the page's
	// commands name.
	Namespace string
	// Secrets reads and writes the installation's Secrets (KubeSecrets).
	Secrets Secrets
	// HasDatabase is whether exemplar-api's environment already has
	// EXEMPLAR_DATABASE_URL: setup then leaves the database alone.
	HasDatabase bool
	// InClusterPostgres reports whether the installation runs the
	// components/postgres database (default: its Service name resolves).
	InClusterPostgres func(context.Context) bool
	// HTTP calls GitHub (default http.DefaultClient).
	HTTP *http.Client
	Log  *slog.Logger
}

// app is one App: what GitHub created, a client signed with its key, its
// installation as last recorded, and the Secret it was read from.
type app struct {
	created       *gh.CreatedApp
	client        *gh.App
	installation  int64
	configMissing bool // installed, but without the config repo
	secret        Secret
}

// Handler serves the setup pages.
type Handler struct {
	cfg    Config
	org    string
	base   string // URLs.API without the trailing slash
	secure bool   // base is https: cookies are Secure

	mu       sync.Mutex
	token    []byte // from exemplar-setup, once Prepare has run
	signing  []byte
	appsPath string // where the owner registers Apps, once looked up

	finished     chan struct{} // closed when this pod saw setup complete
	finishedOnce sync.Once
}

// New checks cfg.
func New(cfg Config) (*Handler, error) {
	org := envcfg.Owner(cfg.ConfigRepo)
	if org == "" || cfg.URLs.API == "" || cfg.Namespace == "" || cfg.Secrets == nil {
		return nil, fmt.Errorf("%w: config repo, public URLs, namespace and Secrets are required", errConfig)
	}
	if cfg.GitHubWeb == "" {
		cfg.GitHubWeb = "https://github.com/"
	}
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = "https://api.github.com/"
	}
	cfg.GitHubWeb = strings.TrimSuffix(cfg.GitHubWeb, "/") + "/"
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.InClusterPostgres == nil {
		cfg.InClusterPostgres = postgresResolves(cfg.Namespace)
	}
	base := strings.TrimSuffix(cfg.URLs.API, "/")
	return &Handler{
		cfg: cfg, org: org, base: base, secure: strings.HasPrefix(base, "https://"),
		finished: make(chan struct{}),
	}, nil
}

// Finished is closed once a Finish on this pod completed setup; other pods
// find out from Installed.
func (h *Handler) Finished() <-chan struct{} { return h.finished }

// Routes serves /setup and its steps.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /setup", h.signedIn(h.status))
	mux.HandleFunc("POST /setup/sign-in", h.signIn)
	mux.HandleFunc("GET /setup/created", h.signedIn(h.created))
	mux.HandleFunc("POST /setup/installation", h.signedIn(h.posted(h.findInstallation)))
	mux.HandleFunc("POST /setup/finish", h.signedIn(h.posted(h.finish)))
	mux.HandleFunc("POST /setup/start-over", h.signedIn(h.posted(h.startOver)))
	mux.Handle("GET /", http.RedirectHandler("/setup", http.StatusFound))
	return h.headers(mux)
}

func (h *Handler) headers(next http.Handler) http.Handler {
	csp := "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' " + strings.TrimSuffix(h.cfg.GitHubWeb, "/") +
		"; frame-ancestors 'none'; base-uri 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", csp)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// keys returns setup's token and signing key, running Prepare first when
// this pod hasn't yet.
func (h *Handler) keys(ctx context.Context) ([]byte, []byte, error) {
	h.mu.Lock()
	token, signing := h.token, h.signing
	h.mu.Unlock()
	if signing != nil {
		return token, signing, nil
	}
	if err := h.Prepare(ctx); err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.token, h.signing, nil
}

// mac signs parts with setup's key.
func mac(key []byte, parts ...string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

// signed is value with an expiry and its MAC: what setup's cookies and
// states carry, checkable by any pod.
func signed(key []byte, purpose, value string, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	return value + "." + exp + "." + mac(key, purpose, value, exp)
}

// verify returns signed's value when s is unexpired and its MAC matches.
func verify(key []byte, purpose, s string) (string, bool) {
	value, rest, ok := strings.Cut(s, ".")
	if !ok {
		return "", false
	}
	exp, sum, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(sum), []byte(mac(key, purpose, value, exp))) {
		return "", false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > t {
		return "", false
	}
	return value, true
}

// signedIn serves next to a browser that signed in with the bootstrap
// token, and the sign-in page to any other.
func (h *Handler) signedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, key, err := h.keys(r.Context())
		if err != nil {
			h.failSecrets(w, err)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			if _, ok := verify(key, "session", c.Value); ok {
				next(w, r)
				return
			}
		}
		h.render(w, http.StatusUnauthorized, "sign-in", signInView{Namespace: h.cfg.Namespace})
	}
}

// signInView is the sign-in page; Wrong: the token didn't match.
type signInView struct {
	Namespace string
	Wrong     bool
}

func (h *Handler) signIn(w http.ResponseWriter, r *http.Request) {
	token, key, err := h.keys(r.Context())
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	given := strings.TrimSpace(r.PostFormValue("token"))
	if subtle.ConstantTimeCompare([]byte(given), token) != 1 {
		h.cfg.Log.Warn("setup: sign-in with a wrong token")
		h.render(w, http.StatusForbidden, "sign-in", signInView{Namespace: h.cfg.Namespace, Wrong: true})
		return
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure whenever the public URL is https (h.secure)
		Name: sessionCookie, Value: signed(key, "session", "operator", sessionTTL), Path: "/setup",
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds()),
	})
	h.cfg.Log.Info("setup: operator signed in")
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// formToken is the hidden field every form posts: the same on every pod.
func formToken(key []byte) string { return mac(key, "form") }

// posted checks a form's token before next.
func (h *Handler) posted(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, key, err := h.keys(r.Context())
		if err != nil {
			h.failSecrets(w, err)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.PostFormValue("form")), []byte(formToken(key))) != 1 {
			h.failApp(r.Context(), w, http.StatusForbidden, failStaleForm)
			return
		}
		next(w, r)
	}
}

// settingsPath is where the config repo's owner registers GitHub Apps: an
// organization's settings, or a user's own. GitHub tells which the owner
// is; while it can't, the page assumes an organization, says so (the bool is
// false), and asks again on the next load.
func (h *Handler) settingsPath(ctx context.Context) (string, bool) {
	h.mu.Lock()
	p := h.appsPath
	h.mu.Unlock()
	if p != "" {
		return p, true
	}
	p = "organizations/" + h.org + "/settings/apps/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.cfg.GitHubAPI+"users/"+url.PathEscape(h.org), nil)
	if err != nil {
		return p, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.cfg.HTTP.Do(req)
	if err != nil {
		h.cfg.Log.Warn("setup: looking up the config repo's owner failed; assuming an organization", "owner", h.org, "err", err)
		return p, false
	}
	defer resp.Body.Close()
	var owner struct {
		Type string `json:"type"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&owner) != nil {
		h.cfg.Log.Warn("setup: looking up the config repo's owner failed; assuming an organization", "owner", h.org, "status", resp.StatusCode)
		return p, false
	}
	if strings.EqualFold(owner.Type, "User") {
		p = "settings/apps/"
	}
	h.mu.Lock()
	h.appsPath = p
	h.mu.Unlock()
	return p, true
}

// appsURL is the owner's App settings page, where a half-registered App is
// deleted.
func (h *Handler) appsURL(ctx context.Context) string {
	p, _ := h.settingsPath(ctx)
	return h.cfg.GitHubWeb + p
}

// Manifest is the App manifest GitHub registers for kind.
func (h *Handler) Manifest(kind string) map[string]any {
	m := map[string]any{
		"url":                      h.cfg.URLs.API,
		"redirect_url":             h.base + "/setup/created",
		"public":                   false,
		"request_oauth_on_install": false,
		"setup_on_update":          false,
	}
	if kind == KindExports {
		// Writes rule-export PRs on opted-in repos; no events, no sign-in.
		m["name"] = "Exemplar exports (" + h.org + ")"
		m["hook_attributes"] = map[string]any{"url": h.cfg.URLs.Webhook, "active": false}
		m["default_permissions"] = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
		return m
	}
	m["name"] = "Exemplar (" + h.org + ")"
	m["hook_attributes"] = map[string]any{"url": h.cfg.URLs.Webhook, "active": true}
	m["callback_urls"] = []string{h.cfg.URLs.Callback}
	// docs/guides/install/production.md lists them. Contents write is for exemplar-config only: the
	// worker mints tokens down-scoped per job.
	m["default_permissions"] = map[string]string{
		"metadata": "read", "contents": "write", "pull_requests": "write", "checks": "write",
		"security_events": "write", "issues": "write", "administration": "read", "members": "read",
	}
	m["default_events"] = []string{
		"pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread",
		"issue_comment", "check_run", "code_scanning_alert", "push",
	}
	return m
}

// appView is one App on the page: the manifest form until GitHub created
// it, then its links.
type appView struct {
	Action, Manifest string
	Created          bool
	Name, AppURL     string
	InstallURL       string
	Installation     int64
	InstallationURL  string // set for the exports app when ExportsOnConfig
}

// statusView is the setup page. Current is the open step: 1 create, 2
// install, 3 finish.
type statusView struct {
	Org, Namespace, Form string
	URLs                 envcfg.URLs
	OwnerUnknown         bool
	Current              int
	Review, Exports      appView
	// Database is the last step's database state: dbReady or dbExternal.
	Database string
	// Finishing: every step is done and exemplar-api is leaving setup mode.
	Finishing bool
	// InstallationURL is where the installation's repositories change.
	InstallationURL string
	// GitHubUnreachable: GitHub didn't answer this load's checks, so the
	// page shows the last known state.
	GitHubUnreachable bool
	// ExportsOnConfig: the exports app is installed on exemplar-config,
	// which it must never write.
	ExportsOnConfig bool
	// ReviewGone and ExportsGone name an App GitHub no longer knows; the
	// page offers to forget it. StartedOver: Start over was just pressed.
	ReviewGone, ExportsGone string
	StartedOver             bool
	// AppsURL is the owner's App settings, where an App is deleted.
	AppsURL string
	// OwnerIsOrg: the config repo's owner is an organization (as far as
	// GitHub said), whose roles the hints on steps 1 and 2 name.
	OwnerIsOrg bool
	// Errors shown in place and not kept: InstallError on the install
	// step (reconcile.go), ProviderError on the last
	// (providerKeyMissing, providerKeyInvalid).
	InstallError, ProviderError string
}

// StepClass is step n's state for the template: done, current or todo.
func (v statusView) StepClass(n int) string {
	switch {
	case v.Finishing || n < v.Current:
		return "done"
	case n == v.Current:
		return "current"
	default:
		return "todo"
	}
}

// pageErrors are the in-place errors a render shows.
type pageErrors struct{ install, provider string }

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	h.renderStatus(w, r, http.StatusOK, pageErrors{})
}

// renderStatus renders the setup page, with errs in place on their steps.
func (h *Handler) renderStatus(w http.ResponseWriter, r *http.Request, code int, errs pageErrors) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, key, err := h.keys(ctx)
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	snap, err := h.load(ctx)
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	review, err := h.appFrom(snap.app)
	if err != nil {
		h.cfg.Log.Warn("setup: reading the App's Secret", "err", err)
		h.failApp(ctx, w, http.StatusInternalServerError, failKey)
		return
	}
	exports, err := h.appFrom(snap.exports)
	if err != nil {
		h.cfg.Log.Warn("setup: reading the exports app's Secret", "err", err)
		exports = nil
	}
	path, known := h.settingsPath(ctx)
	v := statusView{
		Org: h.org, Namespace: h.cfg.Namespace, Form: formToken(key), URLs: h.cfg.URLs,
		OwnerUnknown: !known, InstallError: errs.install, ProviderError: errs.provider,
		AppsURL: h.cfg.GitHubWeb + path, StartedOver: r.URL.Query().Has("started-over"),
		OwnerIsOrg: strings.HasPrefix(path, "organizations/"), Database: h.databaseState(snap),
	}
	gone, appsAnswered := h.reconcileApps(ctx, map[string]*app{KindReview: review, KindExports: exports})
	v.ReviewGone, v.ExportsGone = gone[KindReview], gone[KindExports]
	v.GitHubUnreachable = !appsAnswered
	if review != nil {
		id, state, answered := h.reconcileInstall(ctx, review)
		v.GitHubUnreachable = v.GitHubUnreachable || !answered && errs.install == ""
		if v.InstallError == "" {
			v.InstallError = state
		}
		if id != 0 {
			v.InstallationURL = h.installationURL(ctx, id)
		}
	}
	apps := h.cfg.GitHubWeb + path
	v.Review = h.appView(w, key, KindReview, review, apps)
	v.Exports = h.appView(w, key, KindExports, exports, apps)
	switch {
	case review == nil || v.ReviewGone != "":
		v.Current = 1
	case review.installation == 0 || review.configMissing:
		v.Current = 2
	default:
		v.Current = 3
		v.Finishing = h.installed(snap, review) != nil
		if exports != nil {
			if id, err := exports.client.ConfigInstallation(ctx); err == nil {
				v.ExportsOnConfig, v.Exports.InstallationURL = true, h.installationURL(ctx, id)
			}
		}
	}
	if v.Finishing {
		h.finishedOnce.Do(func() { close(h.finished) })
	}
	h.render(w, code, "status", v)
}

// appView describes kind's App. Before GitHub created it, that's a fresh
// signed state for the manifest form, also set as a cookie.
func (h *Handler) appView(w http.ResponseWriter, key []byte, kind string, a *app, apps string) appView {
	if a == nil {
		manifest, err := json.Marshal(h.Manifest(kind))
		if err != nil { // a map of strings, lists and maps: can't fail
			h.cfg.Log.Error("setup: marshalling the App manifest", "kind", kind, "err", err)
		}
		state := signed(key, "state", kind+"-"+random(), stateTTL)
		// Lax: GitHub's redirect back is a top-level GET, which carries it.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure whenever the public URL is https (h.secure)
			Name: stateCookie + "_" + kind, Value: state, Path: "/setup",
			HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(stateTTL.Seconds()),
		})
		return appView{Action: apps + "new?state=" + url.QueryEscape(state), Manifest: string(manifest)}
	}
	return appView{
		Created: true, Name: a.created.Slug, AppURL: apps + a.created.Slug,
		InstallURL:   strings.TrimSuffix(a.created.HTMLURL, "/") + "/installations/new",
		Installation: a.installation,
	}
}

// stateKind checks a manifest state and returns the kind it was issued
// for.
func stateKind(key []byte, state string) (string, error) {
	v, ok := verify(key, "state", state)
	if !ok {
		return "", errState
	}
	kind, _, _ := strings.Cut(v, "-")
	if kind != KindReview && kind != KindExports {
		return "", errState
	}
	return kind, nil
}

func (h *Handler) created(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_, key, err := h.keys(ctx)
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	kind, err := stateKind(key, state)
	if err != nil || code == "" {
		h.failApp(ctx, w, http.StatusBadRequest, failSpentState)
		return
	}
	c, err := r.Cookie(stateCookie + "_" + kind)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		h.failApp(ctx, w, http.StatusBadRequest, failOtherBrowser)
		return
	}
	created, err := gh.CompleteManifest(ctx, h.cfg.GitHubAPI, h.cfg.HTTP, code)
	if err != nil {
		h.cfg.Log.Warn("setup: completing the App manifest failed", "kind", kind, "err", err)
		h.failApp(ctx, w, http.StatusBadGateway, failConversion)
		return
	}
	if _, err := gh.NewApp(gh.AppConfig{
		AppID: created.ID, PrivateKey: []byte(created.PEM), ConfigRepo: h.cfg.ConfigRepo, BaseURL: h.cfg.GitHubAPI,
	}, h.cfg.HTTP.Transport); err != nil {
		h.cfg.Log.Warn("setup: loading the new App's key failed", "kind", kind, "err", err)
		h.failApp(ctx, w, http.StatusBadGateway, failKey)
		return
	}
	name := secretApp
	if kind == KindExports {
		name = secretExports
	}
	data := appData(kind, created)
	if _, err := h.ensure(ctx, name, func(s Secret) map[string][]byte {
		if s.String(keyAppID) == strconv.FormatInt(created.ID, 10) {
			return nil // a retried redirect
		}
		return data
	}); err != nil {
		// GitHub holds an App whose key setup couldn't keep: the page
		// links where to delete it.
		h.cfg.Log.Error("setup: saving the new App's Secret failed", "kind", kind, "app_id", created.ID, "err", err)
		h.failSecretsApp(ctx, w, err)
		return
	}
	h.cfg.Log.Info("setup: GitHub App created", "kind", kind, "app_id", created.ID, "slug", created.Slug)
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: deletes the cookie set in appView
		Name: stateCookie + "_" + kind, Path: "/setup", MaxAge: -1, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func (h *Handler) findInstallation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s, err := h.getOptional(ctx, secretApp)
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	a, err := h.appFrom(s)
	if err != nil || a == nil {
		h.fail(w, http.StatusConflict, failNotReady)
		return
	}
	id, state, answered := h.reconcileInstall(ctx, a)
	switch {
	case !answered:
		h.renderStatus(w, r, http.StatusBadGateway, pageErrors{install: installListFailed})
	case state != "":
		h.renderStatus(w, r, http.StatusConflict, pageErrors{install: state})
	case id == 0:
		h.renderStatus(w, r, http.StatusConflict, pageErrors{install: installNotFound})
	default:
		h.cfg.Log.Info("setup: installation found", "installation_id", id)
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	}
}

// finish records the models choice: an Anthropic key goes into
// exemplar-providers and is never kept or logged anywhere else.
func (h *Handler) finish(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s, err := h.getOptional(ctx, secretApp)
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	if a, err := h.appFrom(s); err != nil || a == nil || a.installation == 0 {
		h.fail(w, http.StatusConflict, failNotReady)
		return
	}
	if r.PostFormValue("models") == modelsOwn {
		_, err = h.ensure(ctx, secretSetup, func(Secret) map[string][]byte {
			return map[string][]byte{keyProviders: []byte(modelsOwn)}
		})
	} else {
		k := strings.TrimSpace(r.PostFormValue("anthropic-key"))
		switch {
		case k == "":
			h.renderStatus(w, r, http.StatusUnprocessableEntity, pageErrors{provider: providerKeyMissing})
			return
		case strings.IndexFunc(k, func(c rune) bool { return c > unicode.MaxASCII || !unicode.IsPrint(c) || c == ' ' }) >= 0:
			h.renderStatus(w, r, http.StatusUnprocessableEntity, pageErrors{provider: providerKeyInvalid})
			return
		}
		_, err = h.ensure(ctx, secretProviders, func(Secret) map[string][]byte {
			return map[string][]byte{keyAnthropic: []byte(k)}
		})
	}
	if err == nil {
		err = h.writeRootKey(ctx) // last: the other workloads start once it's there
	}
	if err != nil {
		h.failSecrets(w, err)
		return
	}
	h.cfg.Log.Info("setup: models chosen", "own", r.PostFormValue("models") == modelsOwn)
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// startOver forgets the Apps setup created (only the exports app with
// kind=exports) by emptying their Secrets, so setup offers to create them
// again. The Apps stay on GitHub; the page links where to delete them.
func (h *Handler) startOver(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	names := []string{secretApp, secretExports}
	only := r.PostFormValue("kind") == KindExports
	if only {
		names = names[1:]
	}
	for _, name := range names {
		s, err := h.getOptional(ctx, name)
		if err == nil && len(s.Data) > 0 {
			_, err = h.ensure(ctx, name, func(s Secret) map[string][]byte {
				if len(s.Data) == 0 {
					return nil
				}
				return cleared(s)
			})
		}
		if err != nil {
			h.failSecrets(w, err)
			return
		}
		h.cfg.Log.Info("setup: forgot an App", "secret", name, "slug", s.String(keySlug))
	}
	if only {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/setup?started-over", http.StatusSeeOther)
}

// failPage is what an error page shows besides its own text.
type failPage struct {
	AppsURL   string // the owner's App settings, when GitHub may hold an App
	Namespace string
	Secret    string // fail-secret-missing: the Secret the cluster lacks
}

func (h *Handler) fail(w http.ResponseWriter, status int, f failure) {
	h.render(w, status, string(f), failPage{Namespace: h.cfg.Namespace})
}

// failApp is fail for errors after GitHub may have created an App: the page
// links the owner's App settings, where it's deleted.
func (h *Handler) failApp(ctx context.Context, w http.ResponseWriter, status int, f failure) {
	h.render(w, status, string(f), failPage{AppsURL: h.appsURL(ctx), Namespace: h.cfg.Namespace})
}

// failSecrets is the page for a Secret setup couldn't read or write.
func (h *Handler) failSecrets(w http.ResponseWriter, err error) {
	h.render(w, http.StatusServiceUnavailable, string(secretsFailure(h.cfg.Log, err)), failPage{Namespace: h.cfg.Namespace, Secret: missingName(err)})
}

// failSecretsApp is failSecrets after GitHub created an App setup couldn't
// save.
func (h *Handler) failSecretsApp(ctx context.Context, w http.ResponseWriter, err error) {
	h.render(w, http.StatusServiceUnavailable, string(secretsFailure(h.cfg.Log, err)),
		failPage{AppsURL: h.appsURL(ctx), Namespace: h.cfg.Namespace, Secret: missingName(err)})
}

// secretsFailure picks the page for a Secrets error and logs the cause.
func secretsFailure(log *slog.Logger, err error) failure {
	if errors.Is(err, errSecretMissing) {
		return failSecretMissing
	}
	log.Warn("setup: the Kubernetes API refused or didn't answer", "err", err)
	return failKubeAPI
}

// missingName is the Secret an errSecretMissing names.
func missingName(err error) string {
	if !errors.Is(err, errSecretMissing) {
		return ""
	}
	_, name, _ := strings.Cut(err.Error(), errSecretMissing.Error()+": ")
	name, _, _ = strings.Cut(name, "\n")
	return name
}

func (h *Handler) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		h.cfg.Log.Warn("setup: render", "page", name, "err", err)
	}
}

func random() string { return randomHex(24) }

// randomHex is n random bytes, hex-encoded.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
