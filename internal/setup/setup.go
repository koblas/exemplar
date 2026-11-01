// Package setup is exemplar-api's first-run setup: it registers the GitHub
// App from a manifest and hands the operator the exemplar-github-app Secret
// that every other workload waits for.
//
// It runs only while exemplar-api has no App (EXEMPLAR_GH_APP_ID unset), on
// a listener bound to the pod's loopback that no Service, tunnel or Ingress
// routes to. The operator reaches it with kubectl port-forward, so being
// able to port-forward into the namespace is the authorization: nobody who
// finds the public host name first can take the App's private key.
//
// The flow (https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest):
//
//  1. GET /setup renders a form that posts the App manifest (permissions,
//     events, webhook and callback URLs) to GitHub with a one-time state,
//     also set as a cookie.
//  2. GitHub creates the App and redirects to /setup/created with a code.
//     The handler checks the state against the cookie, spends it, and
//     exchanges the code for the App's id, key and secrets.
//  3. The operator installs the App on the config repo's owner (an
//     organization or a user account; the manifest form posts to whichever
//     one's App settings); POST /setup/installation finds the installation.
//  4. The page offers the Secret as YAML. The operator applies it and
//     restarts exemplar-api, which then leaves setup mode.
//
// The page shows those as three numbered steps (create, install, save and
// restart), the first unfinished one open. It doesn't update live: the CSP
// allows no script, every action posts or redirects back to GET /setup, and
// a reload shows the current state. Errors that leave the flow in place
// (the App not installed yet) show inline on their step and are not kept,
// so a reload clears them; the rest get a page naming the next action.
// The pages and every string they show are in pages/.
//
// Credentials stay in this process's memory only, and never reach a log.
// Requests must name the setup URL's host (or 127.0.0.1 on its port): a
// page on another site can't use DNS rebinding to reach the port-forward
// and read the key.
package setup

import (
	"context"
	"crypto/rand"
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

	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/gh"
)

// Kinds of App setup registers.
const (
	KindReview  = "review"  // exemplar-github-app: reviews, config PRs, sign-in
	KindExports = "exports" // exemplar-exports-app: rule exports, optional
)

const (
	stateCookie = "exemplar_setup_state"
	stateTTL    = time.Hour // GitHub's codes last an hour too
	maxStates   = 64
)

var (
	errConfig = errors.New("setup: config")
	errState  = errors.New("setup: unknown or spent state")
)

// Config is what setup needs to describe the App.
type Config struct {
	// URLs are the public addresses the App registers.
	URLs envcfg.URLs
	// ConfigRepo is exemplar-config (owner/name); the App belongs to its owner.
	ConfigRepo string
	// GitHubWeb and GitHubAPI are https://github.com/ and
	// https://api.github.com/, or a GHES host's.
	GitHubWeb, GitHubAPI string
	// Base is where the operator's browser reaches this handler: GitHub
	// redirects there after creating the App.
	Base string
	// Namespace is the installation's namespace, for the Secret.
	Namespace string
	// HTTP calls GitHub (default http.DefaultClient).
	HTTP *http.Client
	Log  *slog.Logger
}

// app is one App's progress through the flow.
type app struct {
	created      *gh.CreatedApp
	installation int64
}

// Handler serves the setup pages.
type Handler struct {
	cfg  Config
	org  string
	form string // token for this process's forms

	mu       sync.Mutex
	states   map[string]pendingState
	apps     map[string]*app
	appsPath string // where the owner registers Apps, once looked up
}

type pendingState struct {
	kind    string
	expires time.Time
}

// New checks cfg.
func New(cfg Config) (*Handler, error) {
	org := envcfg.Owner(cfg.ConfigRepo)
	if org == "" || cfg.URLs.API == "" || cfg.Base == "" || cfg.Namespace == "" {
		return nil, fmt.Errorf("%w: config repo, public URLs, base and namespace are required", errConfig)
	}
	if cfg.GitHubWeb == "" {
		cfg.GitHubWeb = "https://github.com/"
	}
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = "https://api.github.com/"
	}
	cfg.GitHubWeb = strings.TrimSuffix(cfg.GitHubWeb, "/") + "/"
	cfg.Base = strings.TrimSuffix(cfg.Base, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Handler{
		cfg: cfg, org: org, form: random(),
		states: map[string]pendingState{}, apps: map[string]*app{},
	}, nil
}

// Routes serves /setup and its steps.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /setup", h.status)
	mux.HandleFunc("GET /setup/created", h.created)
	mux.HandleFunc("POST /setup/installation", h.findInstallation)
	mux.HandleFunc("GET /setup/{file}", h.secret)
	mux.Handle("GET /", http.RedirectHandler("/setup", http.StatusFound))
	return h.secure(h.local(mux))
}

// local refuses requests for any host but the one the operator's browser
// uses through the port-forward.
func (h *Handler) local(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	if u, err := url.Parse(h.cfg.Base); err == nil && u.Host != "" {
		allowed[strings.ToLower(u.Host)] = true
		if port := u.Port(); port != "" {
			allowed["127.0.0.1:"+port] = true
			allowed["localhost:"+port] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			h.fail(w, http.StatusForbidden, failOtherHost)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) secure(next http.Handler) http.Handler {
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
		"redirect_url":             h.cfg.Base + "/setup/created",
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

// newState issues a one-time state for kind, dropping expired ones.
func (h *Handler) newState(kind string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for s, p := range h.states {
		if now.After(p.expires) {
			delete(h.states, s)
		}
	}
	if len(h.states) >= maxStates {
		for s := range h.states {
			delete(h.states, s)
			break
		}
	}
	s := random()
	h.states[s] = pendingState{kind: kind, expires: now.Add(stateTTL)}
	return s
}

// spendState returns the kind state was issued for, once.
func (h *Handler) spendState(state string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.states[state]
	if !ok || time.Now().After(p.expires) {
		return "", errState
	}
	delete(h.states, state)
	return p.kind, nil
}

// appView is one App on the page: the manifest form until GitHub created
// it, then its links and, once ready, its Secret.
type appView struct {
	Action, Manifest string
	Created          bool
	Name, AppURL     string
	InstallURL       string
	Installation     int64
	Secret           string
}

// statusView is the setup page. Current is the open step: 1 create, 2
// install, 3 save and restart.
type statusView struct {
	Org, Namespace, Form string
	URLs                 envcfg.URLs
	OwnerUnknown         bool
	Holding              bool // an App's key is only in this pod
	Current              int
	Review, Exports      appView
	InstallError         string
}

// StepClass is step n's state for the template: done, current or todo.
func (v statusView) StepClass(n int) string {
	switch {
	case n < v.Current:
		return "done"
	case n == v.Current:
		return "current"
	default:
		return "todo"
	}
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	h.renderStatus(w, r, http.StatusOK, "")
}

// renderStatus renders the setup page, with installError (installNotFound
// or installListFailed) on the install step when set.
func (h *Handler) renderStatus(w http.ResponseWriter, r *http.Request, code int, installError string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	path, known := h.settingsPath(ctx)
	v := statusView{
		Org: h.org, Namespace: h.cfg.Namespace, Form: h.form, URLs: h.cfg.URLs,
		OwnerUnknown: !known, InstallError: installError,
	}
	for _, kind := range []string{KindReview, KindExports} {
		av, ok := h.appView(w, kind, h.cfg.GitHubWeb+path)
		if !ok {
			h.fail(w, http.StatusInternalServerError, failManifest)
			return
		}
		if kind == KindReview {
			v.Review = av
		} else {
			v.Exports = av
		}
		v.Holding = v.Holding || av.Created
	}
	switch {
	case !v.Review.Created:
		v.Current = 1
	case v.Review.Installation == 0:
		v.Current = 2
	default:
		v.Current = 3
	}
	h.render(w, code, "status", v)
}

// appView describes kind's App. Before GitHub created it, that's a fresh
// one-time state for the manifest form, also set as a cookie.
func (h *Handler) appView(w http.ResponseWriter, kind, apps string) (appView, bool) {
	h.mu.Lock()
	a := h.apps[kind]
	var installation int64
	if a != nil {
		installation = a.installation
	}
	h.mu.Unlock()
	if a == nil {
		manifest, err := json.Marshal(h.Manifest(kind))
		if err != nil {
			h.cfg.Log.Error("setup: marshalling the App manifest", "kind", kind, "err", err)
			return appView{}, false
		}
		state := h.newState(kind)
		// Lax: GitHub's redirect back is a top-level GET, which carries it.
		// Not Secure: the page is plain HTTP on localhost, over port-forward.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see above
			Name: stateCookie + "_" + kind, Value: state, Path: "/setup",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(stateTTL.Seconds()),
		})
		return appView{Action: apps + "new?state=" + state, Manifest: string(manifest)}, true
	}
	v := appView{
		Created: true, Name: a.created.Slug, AppURL: apps + a.created.Slug,
		InstallURL:   strings.TrimSuffix(a.created.HTMLURL, "/") + "/installations/new",
		Installation: installation,
	}
	if installation != 0 || kind == KindExports {
		v.Secret = h.secretYAML(kind, a, installation)
	}
	return v, true
}

func (h *Handler) created(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	kind, err := h.spendState(state)
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
	h.mu.Lock()
	h.apps[kind] = &app{created: created}
	h.mu.Unlock()
	h.cfg.Log.Info("setup: GitHub App created", "kind", kind, "app_id", created.ID, "slug", created.Slug)
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: deletes the cookie set in appView
		Name: stateCookie + "_" + kind, Path: "/setup", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func (h *Handler) findInstallation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("form")), []byte(h.form)) != 1 {
		h.failApp(ctx, w, http.StatusForbidden, failStaleForm)
		return
	}
	h.mu.Lock()
	a := h.apps[KindReview]
	h.mu.Unlock()
	if a == nil {
		h.fail(w, http.StatusConflict, failNotReady)
		return
	}
	client, err := gh.NewApp(gh.AppConfig{
		AppID: a.created.ID, PrivateKey: []byte(a.created.PEM), ConfigRepo: h.cfg.ConfigRepo, BaseURL: h.cfg.GitHubAPI,
	}, h.cfg.HTTP.Transport)
	if err != nil {
		h.cfg.Log.Warn("setup: loading the new App's key failed", "err", err)
		h.failApp(ctx, w, http.StatusInternalServerError, failKey)
		return
	}
	id, err := client.OrgInstallation(ctx, h.org)
	if errors.Is(err, gh.ErrNotFound) {
		h.renderStatus(w, r, http.StatusConflict, installNotFound)
		return
	}
	if err != nil {
		h.cfg.Log.Warn("setup: listing installations failed", "err", err)
		h.renderStatus(w, r, http.StatusBadGateway, installListFailed)
		return
	}
	h.mu.Lock()
	a.installation = id
	h.mu.Unlock()
	h.cfg.Log.Info("setup: installation found", "installation_id", id)
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// secret serves exemplar-github-app.yaml or exemplar-exports-app.yaml.
func (h *Handler) secret(w http.ResponseWriter, r *http.Request) {
	kind := map[string]string{"exemplar-github-app.yaml": KindReview, "exemplar-exports-app.yaml": KindExports}[r.PathValue("file")]
	h.mu.Lock()
	a := h.apps[kind]
	var installation int64
	if a != nil {
		installation = a.installation
	}
	h.mu.Unlock()
	if kind == "" {
		http.NotFound(w, r)
		return
	}
	if a == nil || kind == KindReview && installation == 0 {
		h.failApp(r.Context(), w, http.StatusNotFound, failNotReady)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("file")+`"`)
	_, _ = w.Write([]byte(h.secretYAML(kind, a, installation)))
}

// secretYAML is the Secret kind's workloads read (deploy/k8s/core/settings.yaml).
func (h *Handler) secretYAML(kind string, a *app, installation int64) string {
	name := "exemplar-github-app"
	data := [][2]string{{"app-id", strconv.FormatInt(a.created.ID, 10)}}
	if kind == KindExports {
		name = "exemplar-exports-app"
	} else {
		data = append(data,
			[2]string{"installation-id", strconv.FormatInt(installation, 10)},
			[2]string{"client-id", a.created.ClientID},
			[2]string{"client-secret", a.created.ClientSecret},
			[2]string{"webhook-secret", a.created.WebhookSecret})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\n  labels: {app.kubernetes.io/part-of: exemplar}\ntype: Opaque\nstringData:\n", name, h.cfg.Namespace)
	for _, kv := range data {
		fmt.Fprintf(&b, "  %s: %s\n", kv[0], strconv.Quote(kv[1])) // ids and hex: plain ASCII
	}
	b.WriteString("  private-key: |\n")
	for line := range strings.SplitSeq(strings.TrimRight(a.created.PEM, "\n"), "\n") {
		fmt.Fprintf(&b, "    %s\n", line)
	}
	return b.String()
}

// failPage is what an error page shows besides its own text.
type failPage struct {
	AppsURL string // the owner's App settings, when GitHub may hold an App
	Base    string // where setup answers
}

func (h *Handler) fail(w http.ResponseWriter, status int, f failure) {
	h.render(w, status, string(f), failPage{Base: h.cfg.Base})
}

// failApp is fail for errors after GitHub may have created an App: the page
// links the owner's App settings, where it's deleted.
func (h *Handler) failApp(ctx context.Context, w http.ResponseWriter, status int, f failure) {
	h.render(w, status, string(f), failPage{AppsURL: h.appsURL(ctx), Base: h.cfg.Base})
}

func (h *Handler) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		h.cfg.Log.Warn("setup: render", "page", name, "err", err)
	}
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
