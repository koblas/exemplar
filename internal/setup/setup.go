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
	"html/template"
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
	errConfig   = errors.New("setup: config")
	errState    = errors.New("setup: unknown or spent state")
	errNotReady = errors.New("setup: create and install the App first")
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
	Now  func() time.Time
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
	if cfg.Now == nil {
		cfg.Now = time.Now
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
	return h.local(h.secure(mux))
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
			http.Error(w, "setup answers only on "+h.cfg.Base, http.StatusForbidden)
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
// is; while it can't, the page assumes an organization and asks again on
// the next load.
func (h *Handler) settingsPath(ctx context.Context) string {
	h.mu.Lock()
	p := h.appsPath
	h.mu.Unlock()
	if p != "" {
		return p
	}
	p = "organizations/" + h.org + "/settings/apps/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.cfg.GitHubAPI+"users/"+url.PathEscape(h.org), nil)
	if err != nil {
		return p
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.cfg.HTTP.Do(req)
	if err != nil {
		h.cfg.Log.Warn("setup: looking up the config repo's owner failed; assuming an organization", "owner", h.org, "err", err)
		return p
	}
	defer resp.Body.Close()
	var owner struct {
		Type string `json:"type"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&owner) != nil {
		h.cfg.Log.Warn("setup: looking up the config repo's owner failed; assuming an organization", "owner", h.org, "status", resp.StatusCode)
		return p
	}
	if strings.EqualFold(owner.Type, "User") {
		p = "settings/apps/"
	}
	h.mu.Lock()
	h.appsPath = p
	h.mu.Unlock()
	return p
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
	now := h.cfg.Now()
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
	if !ok || h.cfg.Now().After(p.expires) {
		return "", errState
	}
	delete(h.states, state)
	return p.kind, nil
}

type step struct {
	Kind, Title, Resource       string
	Action, Manifest, State     string
	Created                     bool
	AppURL, InstallURL, Name    string
	Installation                int64
	SecretYAML                  string
	Optional, ShowDeviceFlowTip bool
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	apps := h.cfg.GitHubWeb + h.settingsPath(ctx)
	steps := make([]step, 0, 2)
	for _, kind := range []string{KindReview, KindExports} {
		s := step{Kind: kind, Title: "The review app", Resource: "exemplar-github-app", ShowDeviceFlowTip: true}
		if kind == KindExports {
			s = step{Kind: kind, Title: "The exports app", Resource: "exemplar-exports-app", Optional: true}
		}
		h.mu.Lock()
		a := h.apps[kind]
		h.mu.Unlock()
		if a == nil {
			state := h.newState(kind)
			manifest, err := json.Marshal(h.Manifest(kind))
			if err != nil {
				http.Error(w, "manifest", http.StatusInternalServerError)
				return
			}
			s.Action = apps + "new?state=" + state
			s.Manifest, s.State = string(manifest), state
			// Lax: GitHub's redirect back is a top-level GET, which carries it.
			// Not Secure: the page is plain HTTP on localhost, over port-forward.
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: see above
				Name: stateCookie + "_" + kind, Value: state, Path: "/setup",
				HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(stateTTL.Seconds()),
			})
		} else {
			s.Created, s.Name = true, a.created.Slug
			s.AppURL = apps + a.created.Slug
			s.InstallURL = strings.TrimSuffix(a.created.HTMLURL, "/") + "/installations/new"
			s.Installation = a.installation
			if a.installation != 0 || kind == KindExports {
				s.SecretYAML = h.secretYAML(kind, a)
			}
		}
		steps = append(steps, s)
	}
	h.render(w, "status", map[string]any{
		"Steps": steps, "Form": h.form, "Org": h.org, "Namespace": h.cfg.Namespace, "URLs": h.cfg.URLs,
	})
}

func (h *Handler) created(w http.ResponseWriter, r *http.Request) {
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	kind, err := h.spendState(state)
	if err != nil || code == "" {
		h.fail(w, http.StatusBadRequest, "This setup link was already used or has expired. Start again from /setup.")
		return
	}
	c, err := r.Cookie(stateCookie + "_" + kind)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		h.fail(w, http.StatusBadRequest, "This browser didn't start that setup. Start again from /setup.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	created, err := gh.CompleteManifest(ctx, h.cfg.GitHubAPI, h.cfg.HTTP, code)
	if err != nil {
		h.cfg.Log.Warn("setup: completing the App manifest failed", "kind", kind, "err", err)
		h.fail(w, http.StatusBadGateway, "GitHub didn't return the new App. Its code lasts an hour and works once; start again from /setup.")
		return
	}
	h.mu.Lock()
	h.apps[kind] = &app{created: created}
	h.mu.Unlock()
	h.cfg.Log.Info("setup: GitHub App created", "kind", kind, "app_id", created.ID, "slug", created.Slug)
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: deletes the cookie set in status
		Name: stateCookie + "_" + kind, Path: "/setup", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func (h *Handler) findInstallation(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("form")), []byte(h.form)) != 1 {
		h.fail(w, http.StatusForbidden, "That form is from an earlier run of setup. Reload /setup.")
		return
	}
	h.mu.Lock()
	a := h.apps[KindReview]
	h.mu.Unlock()
	if a == nil {
		h.fail(w, http.StatusConflict, errNotReady.Error())
		return
	}
	client, err := gh.NewApp(gh.AppConfig{
		AppID: a.created.ID, PrivateKey: []byte(a.created.PEM), ConfigRepo: h.cfg.ConfigRepo, BaseURL: h.cfg.GitHubAPI,
	}, h.cfg.HTTP.Transport)
	if err != nil {
		h.fail(w, http.StatusInternalServerError, "The new App's key didn't load.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	id, err := client.OrgInstallation(ctx, h.org)
	if errors.Is(err, gh.ErrNotFound) {
		h.fail(w, http.StatusConflict, "The App isn't installed on "+h.org+" yet. Install it, then check again.")
		return
	}
	if err != nil {
		h.cfg.Log.Warn("setup: listing installations failed", "err", err)
		h.fail(w, http.StatusBadGateway, "GitHub didn't list the App's installations. Try again.")
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
	h.mu.Unlock()
	if kind == "" || a == nil || kind == KindReview && a.installation == 0 {
		h.fail(w, http.StatusNotFound, errNotReady.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("file")+`"`)
	_, _ = w.Write([]byte(h.secretYAML(kind, a)))
}

// secretYAML is the Secret kind's workloads read (deploy/k8s/core/settings.yaml).
func (h *Handler) secretYAML(kind string, a *app) string {
	name := "exemplar-github-app"
	data := [][2]string{{"app-id", strconv.FormatInt(a.created.ID, 10)}}
	if kind == KindExports {
		name = "exemplar-exports-app"
	} else {
		data = append(data,
			[2]string{"installation-id", strconv.FormatInt(a.installation, 10)},
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

func (h *Handler) fail(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	h.render(w, "error", map[string]any{"Message": msg})
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		h.cfg.Log.Warn("setup: render", "page", name, "err", err)
	}
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var pages = template.Must(template.New("").Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Exemplar setup</title><style>
body{font:15px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;color:#1f2328;background:#fff}
h1{font-size:1.4rem}h2{font-size:1.1rem;margin-top:2rem}code,pre{font:13px ui-monospace,monospace;background:#f6f8fa}
pre{padding:.75rem;overflow-x:auto}button{font:inherit;padding:.4rem .9rem;cursor:pointer}.done{color:#1a7f37}.muted{color:#59636e}
@media (prefers-color-scheme:dark){body{color:#e6edf3;background:#0d1117}code,pre{background:#161b22}.muted{color:#9198a1}}
</style></head><body><h1>Exemplar setup</h1>{{end}}
{{define "error"}}{{template "head"}}<p>{{.Message}}</p><p><a href="/setup">Back to setup</a></p></body></html>{{end}}
{{define "status"}}{{template "head"}}
<p>Exemplar registers its GitHub Apps on <b>{{.Org}}</b>. GitHub posts webhooks to <code>{{.URLs.Webhook}}</code>,
and people sign in at <code>{{.URLs.API}}</code>. If either is wrong, fix <code>EXEMPLAR_API_URL</code> (or
<code>EXEMPLAR_WEBHOOK_URL</code>) in the overlay first.</p>
<p class="muted">Keep this page open until you have applied each Secret: what it shows lives only in this pod's memory.</p>
{{range .Steps}}
<h2>{{.Title}}{{if .Optional}} <span class="muted">(optional: only for rule exports)</span>{{end}}</h2>
{{if not .Created}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>GitHub shows the App with its permissions filled in. You can change its name there.</p>
<button type="submit">Create it on GitHub</button>
</form>
{{else}}
<p class="done">Created <a href="{{.AppURL}}">{{.Name}}</a>.</p>
{{if .ShowDeviceFlowTip}}<p>For <code>exemplar login</code>, open the <a href="{{.AppURL}}">App's settings</a> and tick <b>Enable Device Flow</b>.</p>{{end}}
{{if eq .Kind "review"}}{{if not .Installation}}
<p>Now <a href="{{.InstallURL}}">install it on {{$.Org}}</a>: choose the repos Exemplar reviews, and exemplar-config.</p>
<form method="post" action="/setup/installation"><input type="hidden" name="form" value="{{$.Form}}">
<button type="submit">I've installed it</button></form>
{{else}}<p class="done">Installed (installation {{.Installation}}).</p>{{end}}
{{else}}<p>Install it <a href="{{.InstallURL}}">only on the repos that opt into exports</a>, never on exemplar-config.</p>{{end}}
{{if .SecretYAML}}
<p>Save <a href="/setup/{{.Resource}}.yaml">{{.Resource}}.yaml</a> and apply it:</p>
<pre>kubectl apply -f {{.Resource}}.yaml</pre>
{{if eq .Kind "review"}}<p>Then restart exemplar-api, which leaves setup mode. The other workloads have been waiting for this Secret
and start on their own:</p>
<pre>kubectl -n {{$.Namespace}} rollout restart deployment/exemplar-api</pre>{{end}}
<details><summary>Show the Secret</summary><pre>{{.SecretYAML}}</pre></details>
{{end}}
{{end}}
{{end}}
</body></html>{{end}}
`))
