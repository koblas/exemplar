package setup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/koblas/exemplar/internal/envcfg"
)

// fakeGitHub answers the manifest conversion (each code once) and the App's
// installation listing.
type fakeGitHub struct {
	t         *testing.T
	pem       string
	mu        sync.Mutex
	used      map[string]bool
	installed bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/app-manifests/"):
		code := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app-manifests/"), "/conversions")
		if f.used[code] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
			return
		}
		f.used[code] = true
		pemJSON, err := json.Marshal(f.pem)
		if err != nil {
			f.t.Error(err)
		}
		fmt.Fprintf(w, `{"id": 4242, "slug": "exemplar-acme", "html_url": "https://github.com/apps/exemplar-acme",
			"client_id": "Iv23liAcme", "client_secret": "client-secret-value", "webhook_secret": "webhook-secret-value", "pem": %s}`, pemJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			f.t.Error("installations listed without the new App's JWT")
		}
		if !f.installed {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id": 77, "account": {"login": "acme"}}]`))
	default:
		http.NotFound(w, r)
	}
}

func newTestKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func newSetup(t *testing.T) (*httptest.Server, *fakeGitHub) {
	t.Helper()
	fake := &fakeGitHub{t: t, pem: newTestKey(t), used: map[string]bool{}}
	gh := httptest.NewServer(fake)
	t.Cleanup(gh.Close)
	urls, err := envcfg.PublicURLs(func(k string) string {
		return map[string]string{"EXEMPLAR_API_URL": "https://exemplar.acme.com"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{
		URLs: urls, ConfigRepo: "acme/exemplar-config", GitHubWeb: "https://github.com/", GitHubAPI: gh.URL + "/",
		Base: "http://localhost:8081", Namespace: "exemplar", HTTP: gh.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return srv, fake
}

// browser keeps cookies and doesn't follow redirects.
func browser(t *testing.T) *http.Client {
	t.Helper()
	jar := &jarOf{cookies: map[string]*http.Cookie{}}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type jarOf struct {
	mu      sync.Mutex
	cookies map[string]*http.Cookie
}

func (j *jarOf) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c
	}
}

func (j *jarOf) Cookies(*url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.cookies))
	for _, c := range j.cookies {
		out = append(out, c)
	}
	return out
}

// page is a fetched response, its body read and closed.
type page struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, c *http.Client, method, u string, form url.Values) page {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return page{resp.StatusCode, resp.Header, string(b)}
}

func get(t *testing.T, c *http.Client, u string) page {
	t.Helper()
	return do(t, c, http.MethodGet, u, nil)
}

var (
	actionRe   = regexp.MustCompile(`action="(https://github\.com/[^"]+)"`)
	manifestRe = regexp.MustCompile(`name="manifest" value="([^"]+)"`)
	formRe     = regexp.MustCompile(`name="form" value="([0-9a-f]+)"`)
)

func TestManifestFlow(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t)

	p := get(t, c, srv.URL+"/setup")
	if p.status != http.StatusOK || p.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /setup = %d, %v", p.status, p.header)
	}
	if !strings.Contains(p.header.Get("Content-Security-Policy"), "form-action 'self' https://github.com") {
		t.Errorf("CSP %q doesn't allow posting the manifest to GitHub", p.header.Get("Content-Security-Policy"))
	}
	action := actionRe.FindStringSubmatch(p.body)
	manifest := manifestRe.FindStringSubmatch(p.body)
	if action == nil || manifest == nil {
		t.Fatalf("no manifest form in:\n%s", p.body)
	}
	au, _ := url.Parse(html.UnescapeString(action[1]))
	if au.Path != "/organizations/acme/settings/apps/new" {
		t.Errorf("manifest posts to %s", au)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(manifest[1])), &m); err != nil {
		t.Fatal(err)
	}
	hook, _ := m["hook_attributes"].(map[string]any)
	if hook["url"] != "https://exemplar.acme.com/webhook" || m["redirect_url"] != "http://localhost:8081/setup/created" ||
		fmt.Sprint(m["callback_urls"]) != "[https://exemplar.acme.com/oauth/callback]" || m["public"] != false {
		t.Errorf("manifest = %v", m)
	}
	state := au.Query().Get("state")

	// Another browser, without the cookie, can't finish this setup.
	if p := get(t, browser(t), srv.URL+"/setup/created?code=c1&state="+state); p.status != http.StatusBadRequest {
		t.Fatalf("created without the cookie = %d", p.status)
	}
	// That attempt spent the state.
	if p := get(t, c, srv.URL+"/setup/created?code=c1&state="+state); p.status != http.StatusBadRequest {
		t.Fatalf("spent state = %d", p.status)
	}

	state = mustState(t, get(t, c, srv.URL+"/setup").body)
	if p := get(t, c, srv.URL+"/setup/created?code=c2&state="+state); p.status != http.StatusSeeOther {
		t.Fatalf("created = %d", p.status)
	}
	if p := get(t, c, srv.URL+"/setup/created?code=c2&state="+state); p.status != http.StatusBadRequest {
		t.Fatalf("replayed redirect = %d", p.status)
	}

	// No Secret until the App is installed.
	if p := get(t, c, srv.URL+"/setup/exemplar-github-app.yaml"); p.status != http.StatusNotFound {
		t.Fatalf("secret before install = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	form := formRe.FindStringSubmatch(p.body)
	if form == nil || !strings.Contains(p.body, "installations/new") {
		t.Fatalf("no install step in:\n%s", p.body)
	}
	post := func(token string) int {
		return do(t, c, http.MethodPost, srv.URL+"/setup/installation", url.Values{"form": {token}}).status
	}
	if code := post("forged"); code != http.StatusForbidden {
		t.Fatalf("forged form = %d", code)
	}
	if code := post(form[1]); code != http.StatusConflict {
		t.Fatalf("before install = %d", code)
	}
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	if code := post(form[1]); code != http.StatusSeeOther {
		t.Fatalf("after install = %d", code)
	}

	p = get(t, c, srv.URL+"/setup/exemplar-github-app.yaml")
	if p.status != http.StatusOK {
		t.Fatalf("secret = %d", p.status)
	}
	body := p.body
	var secret struct {
		Kind     string            `yaml:"kind"`
		Metadata map[string]any    `yaml:"metadata"`
		Data     map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal([]byte(body), &secret); err != nil {
		t.Fatalf("secret YAML: %v\n%s", err, body)
	}
	want := map[string]string{
		"app-id": "4242", "installation-id": "77", "client-id": "Iv23liAcme",
		"client-secret": "client-secret-value", "webhook-secret": "webhook-secret-value",
	}
	for k, v := range want {
		if secret.Data[k] != v {
			t.Errorf("%s = %q, want %q", k, secret.Data[k], v)
		}
	}
	if secret.Kind != "Secret" || secret.Metadata["name"] != "exemplar-github-app" || secret.Metadata["namespace"] != "exemplar" ||
		secret.Data["private-key"] != fake.pem {
		t.Errorf("secret = %+v", secret)
	}
}

func mustState(t *testing.T, page string) string {
	t.Helper()
	a := actionRe.FindStringSubmatch(page)
	if a == nil {
		t.Fatalf("no manifest form in:\n%s", page)
	}
	u, _ := url.Parse(html.UnescapeString(a[1]))
	return u.Query().Get("state")
}

func TestExportsManifest(t *testing.T) {
	h, err := New(Config{
		URLs:       envcfg.URLs{API: "https://e.acme.com", Webhook: "https://e.acme.com/webhook", Callback: "https://e.acme.com/oauth/callback"},
		ConfigRepo: "acme/exemplar-config", Base: "http://localhost:8081", Namespace: "exemplar",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := h.Manifest(KindExports)
	if _, ok := m["default_events"]; ok {
		t.Error("the exports app subscribes to no events")
	}
	if hook, _ := m["hook_attributes"].(map[string]any); hook["active"] != false {
		t.Error("the exports app's webhook is off")
	}
	if fmt.Sprint(m["default_permissions"]) != "map[contents:write metadata:read pull_requests:write]" {
		t.Errorf("exports permissions = %v", m["default_permissions"])
	}
}

func TestNewRequiresConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with no config")
	}
}
