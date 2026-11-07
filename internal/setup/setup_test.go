package setup

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
	userOwner bool // acme is a user account, not an organization
	ownerDown bool // GET /users/acme fails
	listDown  bool // GET /app/installations fails
	noConfig  bool // installed without acme/exemplar-config
	appGone   int  // GET /app answers this status: the App was deleted (0: it exists)
	goneID    int  // only this App (by id) is gone; 0: any App
	slug      string
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
		id := 4242
		if code == "exports" {
			id = 4343 // the exports app, in tests that make one with this code
		}
		fmt.Fprintf(w, `{"id": %d, "slug": "exemplar-acme", "html_url": "https://github.com/apps/exemplar-acme",
			"client_id": "Iv23liAcme", "client_secret": "client-secret-value", "webhook_secret": "webhook-secret-value", "pem": %s}`, id, pemJSON)
	case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			f.t.Error("installations listed without the new App's JWT")
		}
		if f.listDown {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !f.installed {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id": 77, "account": {"login": "acme"}}]`))
	case r.Method == http.MethodGet && r.URL.Path == "/app":
		if f.appGone != 0 && (f.goneID == 0 || jwtIssuer(r) == f.goneID) {
			w.WriteHeader(f.appGone)
			_, _ = w.Write([]byte(`{"message": "Integration not found"}`))
			return
		}
		slug := cmpOr(f.slug, "exemplar-acme")
		fmt.Fprintf(w, `{"id": 4242, "slug": %q, "html_url": "https://github.com/apps/%s"}`, slug, slug)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/exemplar-config/installation":
		if !f.installed || f.noConfig {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id": 77, "account": {"login": "acme"}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/users/acme":
		if f.ownerDown {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if f.userOwner {
			_, _ = w.Write([]byte(`{"login": "acme", "type": "User"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login": "acme", "type": "Organization"}`))
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

// testServer is setup on one exemplar-api pod, and the cluster's Secrets
// it shares with any other pod a test starts (another).
type testServer struct {
	*httptest.Server

	h    *Handler
	kube *k8sfake.Clientset
	cfg  Config
}

// emptySecrets are the Secrets deploy/k8s declares, empty, in exemplar.
func emptySecrets() []runtime.Object {
	names := []string{secretSetup, secretApp, secretExports, secretRootKey, secretPostgres, secretDatabase, secretProviders}
	out := make([]runtime.Object, 0, len(names))
	for _, name := range names {
		out = append(out, &corev1.Secret{Name: name, Namespace: "exemplar"})
	}
	return out
}

func newSetup(t *testing.T) (*testServer, *fakeGitHub) {
	t.Helper()
	return newSetupWith(t, nil)
}

// newSetupWith is newSetup with the configuration changed by edit: by
// default a fresh install with the in-cluster Postgres and every Secret
// declared empty.
func newSetupWith(t *testing.T, edit func(*Config)) (*testServer, *fakeGitHub) {
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
	kube := k8sfake.NewClientset(emptySecrets()...)
	cfg := Config{
		URLs: urls, ConfigRepo: "acme/exemplar-config", GitHubWeb: "https://github.com/", GitHubAPI: gh.URL + "/",
		Namespace: "exemplar", HTTP: gh.Client(), Secrets: KubeSecrets{Client: kube, Namespace: "exemplar"},
		InClusterPostgres: func(context.Context) bool { return true },
	}
	if edit != nil {
		edit(&cfg)
	}
	return start(t, cfg, kube), fake
}

// start serves cfg as one exemplar-api pod.
func start(t *testing.T, cfg Config, kube *k8sfake.Clientset) *testServer {
	t.Helper()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return &testServer{Server: srv, h: h, kube: kube, cfg: cfg}
}

// another is a second exemplar-api pod on the same cluster.
func (s *testServer) another(t *testing.T) *testServer {
	t.Helper()
	return start(t, s.cfg, s.kube)
}

// secret reads a Secret's data as strings.
func (s *testServer) secret(t *testing.T, name string) map[string]string {
	t.Helper()
	got, err := s.kube.CoreV1().Secrets("exemplar").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range got.Data {
		out[k] = string(v)
	}
	return out
}

// anonymous keeps cookies and doesn't follow redirects.
func anonymous(t *testing.T) *http.Client {
	t.Helper()
	jar := &jarOf{cookies: map[string]*http.Cookie{}}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// browser is anonymous, signed in to srv with the setup token.
func browser(t *testing.T, srv *testServer) *http.Client {
	t.Helper()
	c := anonymous(t)
	if p := get(t, c, srv.URL+"/setup"); p.status != http.StatusUnauthorized { // runs Prepare
		t.Fatalf("GET /setup before sign-in = %d", p.status)
	}
	token := srv.secret(t, secretSetup)[keyToken]
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/sign-in", url.Values{"token": {token}}); p.status != http.StatusSeeOther {
		t.Fatalf("sign-in = %d\n%s", p.status, p.body)
	}
	return c
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
	c := browser(t, srv)

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
	if hook["url"] != "https://exemplar.acme.com/webhook" || m["redirect_url"] != "https://exemplar.acme.com/setup/created" ||
		fmt.Sprint(m["callback_urls"]) != "[https://exemplar.acme.com/oauth/callback]" || m["public"] != false {
		t.Errorf("manifest = %v", m)
	}
	state := au.Query().Get("state")

	// Another signed-in browser, without the state cookie, can't finish
	// this registration, nor can anyone with a forged state.
	if p := get(t, browser(t, srv), srv.URL+"/setup/created?code=c1&state="+url.QueryEscape(state)); p.status != http.StatusBadRequest {
		t.Fatalf("created without the cookie = %d", p.status)
	}
	if p := get(t, c, srv.URL+"/setup/created?code=c1&state=review-x.9999999999.forged"); p.status != http.StatusBadRequest {
		t.Fatalf("forged state = %d", p.status)
	}

	if p := get(t, c, srv.URL+"/setup/created?code=c2&state="+url.QueryEscape(state)); p.status != http.StatusSeeOther {
		t.Fatalf("created = %d", p.status)
	}
	// GitHub's code works once: a replayed redirect fails there.
	if p := get(t, c, srv.URL+"/setup/created?code=c2&state="+url.QueryEscape(state)); p.status != http.StatusBadRequest && p.status != http.StatusBadGateway {
		t.Fatalf("replayed redirect = %d", p.status)
	}
	// The App's credentials are in the cluster the moment GitHub hands them over.
	app := srv.secret(t, secretApp)
	want := map[string]string{
		"app-id": "4242", "client-id": "Iv23liAcme", "client-secret": "client-secret-value",
		keyWebhookHeld: "webhook-secret-value", "private-key": fake.pem, "slug": "exemplar-acme",
	}
	for k, v := range want {
		if app[k] != v {
			t.Errorf("exemplar-github-app %s = %q, want %q", k, app[k], v)
		}
	}
	// exemplar-webhook mounts webhook-secret: held back until the release,
	// so it can't start with an App that Start over replaces.
	for _, k := range []string{keyInstallation, keyWebhookSecret} {
		if _, ok := app[k]; ok {
			t.Errorf("%s before the App is installed", k)
		}
	}

	p = get(t, c, srv.URL+"/setup")
	form := formRe.FindStringSubmatch(p.body)
	if form == nil || !strings.Contains(p.body, "installations/new") {
		t.Fatalf("no install step in:\n%s", p.body)
	}
	post := func(path string, v url.Values) int {
		return do(t, c, http.MethodPost, srv.URL+path, v).status
	}
	if code := post("/setup/installation", url.Values{"form": {"forged"}}); code != http.StatusForbidden {
		t.Fatalf("forged form = %d", code)
	}
	if code := post("/setup/installation", url.Values{"form": {form[1]}}); code != http.StatusConflict {
		t.Fatalf("before install = %d", code)
	}
	// Finish isn't open until the App is installed.
	if code := post("/setup/finish", url.Values{"form": {form[1]}, "anthropic-key": {"sk-ant-test"}}); code != http.StatusConflict {
		t.Fatalf("finish before install = %d", code)
	}
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	if code := post("/setup/installation", url.Values{"form": {form[1]}}); code != http.StatusSeeOther {
		t.Fatalf("after install = %d", code)
	}
	if got := srv.secret(t, secretApp)[keyInstallation]; got != "77" {
		t.Errorf("installation-id = %q, want 77", got)
	}
	if in, err := srv.h.Installed(t.Context()); err != nil || in != nil {
		t.Fatalf("Installed before Finish = %+v, %v", in, err)
	}

	if code := post("/setup/finish", url.Values{"form": {form[1]}, "anthropic-key": {" sk-ant-test "}}); code != http.StatusSeeOther {
		t.Fatalf("finish = %d", code)
	}
	if got := srv.secret(t, secretProviders)[keyAnthropic]; got != "sk-ant-test" {
		t.Errorf("exemplar-providers anthropic-api-key = %q", got)
	}
	if got := srv.secret(t, secretRootKey)[keyRootKey]; len(got) != 2*envcfg.MinRootKeyBytes {
		t.Errorf("root key after Finish = %q", got)
	}
	if app := srv.secret(t, secretApp); app[keyWebhookSecret] != "webhook-secret-value" || app[keyWebhookHeld] != "" {
		t.Errorf("after Finish: webhook-secret %q, held %q", app[keyWebhookSecret], app[keyWebhookHeld])
	}
	p = get(t, c, srv.URL+"/setup")
	mustContain(t, p, titleDone, `<meta http-equiv="refresh" content="5">`)
	select {
	case <-srv.h.Finished():
	default:
		t.Error("Finished not closed after the finishing page")
	}
	in, err := srv.h.Installed(t.Context())
	if err != nil || in == nil {
		t.Fatalf("Installed = %v, %v", in, err)
	}
	db := srv.secret(t, secretDatabase)[keyDatabaseURL]
	if in.AppID != 4242 || in.ClientID != "Iv23liAcme" || string(in.ClientSecret) != "client-secret-value" ||
		string(in.PrivateKey) != fake.pem || string(in.RootKey) != srv.secret(t, secretRootKey)[keyRootKey] || in.DatabaseURL != db {
		t.Errorf("Installed = %+v", in)
	}
}

// Setup answers only a browser that signed in with the token in
// exemplar-setup.
func TestSignIn(t *testing.T) {
	srv, _ := newSetup(t)
	c := anonymous(t)
	for _, path := range []string{"/setup", "/setup/created?code=c&state=s"} {
		p := get(t, c, srv.URL+path)
		if p.status != http.StatusUnauthorized {
			t.Errorf("GET %s signed out = %d", path, p.status)
		}
		mustContain(t, p, "Sign in to set up Exemplar", "kubectl -n exemplar get secret exemplar-setup -o jsonpath='{.data.token}' | base64 -d")
		mustNotContain(t, p, "That isn't the setup token", labelCreate)
	}
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/installation", url.Values{"form": {"x"}}); p.status != http.StatusUnauthorized {
		t.Errorf("POST signed out = %d", p.status)
	}
	p := do(t, c, http.MethodPost, srv.URL+"/setup/sign-in", url.Values{"token": {"guess"}})
	if p.status != http.StatusForbidden {
		t.Errorf("wrong token = %d", p.status)
	}
	mustContain(t, p, "That isn't the setup token.")

	// A forged session cookie is a signed-out browser.
	forged := anonymous(t)
	u, _ := url.Parse(srv.URL)
	forged.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "operator.9999999999.00"}})
	if p := get(t, forged, srv.URL+"/setup"); p.status != http.StatusUnauthorized {
		t.Errorf("forged session = %d", p.status)
	}

	token := srv.secret(t, secretSetup)[keyToken]
	if len(token) < 32 {
		t.Fatalf("token %q is too short", token)
	}
	p = do(t, c, http.MethodPost, srv.URL+"/setup/sign-in", url.Values{"token": {token + "\n"}})
	if p.status != http.StatusSeeOther {
		t.Fatalf("right token = %d", p.status)
	}
	if !strings.Contains(p.header.Get("Set-Cookie"), "Secure") || !strings.Contains(p.header.Get("Set-Cookie"), "HttpOnly") {
		t.Errorf("session cookie = %q", p.header.Get("Set-Cookie"))
	}
	if p := get(t, c, srv.URL+"/setup"); p.status != http.StatusOK || currentStep(t, p) != titleCreate {
		t.Errorf("signed in = %d", p.status)
	}
	// The session works on every pod.
	if p := get(t, c, srv.another(t).URL+"/setup"); p.status != http.StatusOK {
		t.Errorf("signed in, another pod = %d", p.status)
	}
}

// Prepare writes the token, the root key and the in-cluster database's
// password and URL once, whichever pod runs it, and keeps them after.
func TestPrepare(t *testing.T) {
	srv, _ := newSetup(t)
	if err := srv.h.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	setup, pg, db := srv.secret(t, secretSetup), srv.secret(t, secretPostgres), srv.secret(t, secretDatabase)
	if setup[keyToken] == "" || setup[keySigning] == "" || pg[keyPassword] == "" {
		t.Fatalf("after Prepare: setup %v, postgres %v", setup, pg)
	}
	// The other workloads wait for the root key: not before Finish.
	if root := srv.secret(t, secretRootKey); len(root) != 0 {
		t.Errorf("root key before Finish: %v", root)
	}
	if want := "postgres://exemplar:" + pg[keyPassword] + "@exemplar-postgres.exemplar.svc:5432/exemplar"; db[keyDatabaseURL] != want {
		t.Errorf("database url = %q, want %q", db[keyDatabaseURL], want)
	}
	if err := srv.another(t).h.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if srv.secret(t, secretSetup)[keyToken] != setup[keyToken] || srv.secret(t, secretPostgres)[keyPassword] != pg[keyPassword] {
		t.Error("a second Prepare changed what the first wrote")
	}

	for name, edit := range map[string]func(*Config){
		"database in the environment": func(c *Config) { c.HasDatabase = true },
		"external database":           func(c *Config) { c.InClusterPostgres = func(context.Context) bool { return false } },
	} {
		srv, _ := newSetupWith(t, edit)
		if err := srv.h.Prepare(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(srv.secret(t, secretPostgres)) != 0 || len(srv.secret(t, secretDatabase)) != 0 {
			t.Errorf("%s: Prepare wrote the database Secrets", name)
		}
	}
}

// Two pods racing: the loser of a write reads the winner's values.
func TestPrepareConflict(t *testing.T) {
	srv, _ := newSetup(t)
	conflicts := 1
	srv.kube.PrependReactor("patch", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if pa, ok := a.(k8stesting.PatchAction); !ok || pa.GetName() != secretSetup || conflicts == 0 {
			return false, nil, nil
		}
		conflicts--
		// Another pod's write lands first.
		_, err := srv.kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), "exemplar", secretSetup)
		if err != nil {
			t.Error(err)
		}
		if err := srv.kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), &corev1.Secret{
			Name: secretSetup, Namespace: "exemplar",
			Data: map[string][]byte{keyToken: []byte("winner"), keySigning: []byte("winner-key")},
		}, "exemplar"); err != nil {
			t.Error(err)
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, secretSetup, errors.New("changed"))
	})
	if err := srv.h.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := srv.secret(t, secretSetup)[keyToken]; got != "winner" {
		t.Errorf("token = %q, want the other pod's", got)
	}
	c := anonymous(t)
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/sign-in", url.Values{"token": {"winner"}}); p.status != http.StatusSeeOther {
		t.Errorf("sign-in with the winner's token = %d", p.status)
	}
}

// A pod that restarts, or another pod, picks up where setup was: the
// App is in its Secret, and a state one pod issued works on the other.
func TestResumeOnAnotherPod(t *testing.T) {
	a, fake := newSetup(t)
	c := browser(t, a)
	b := a.another(t)

	state := mustState(t, get(t, c, a.URL+"/setup").body)
	if p := get(t, c, b.URL+"/setup/created?code=c1&state="+url.QueryEscape(state)); p.status != http.StatusSeeOther {
		t.Fatalf("GitHub's redirect to the other pod = %d", p.status)
	}
	p := get(t, c, b.URL+"/setup")
	if currentStep(t, p) != titleInstall {
		t.Errorf("other pod, after create = %q", currentStep(t, p))
	}
	fresh := a.another(t) // a restarted pod: nothing in memory
	p = get(t, c, fresh.URL+"/setup")
	if currentStep(t, p) != titleInstall {
		t.Errorf("restarted pod = %q", currentStep(t, p))
	}
	form := formRe.FindStringSubmatch(p.body)
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	if p := do(t, c, http.MethodPost, a.URL+"/setup/installation", url.Values{"form": {form[1]}}); p.status != http.StatusSeeOther {
		t.Fatalf("form from one pod, posted to another = %d", p.status)
	}
	if p := do(t, c, http.MethodPost, b.URL+"/setup/finish", url.Values{"form": {form[1]}, "models": {modelsOwn}}); p.status != http.StatusSeeOther {
		t.Fatalf("finish = %d", p.status)
	}
	if got := a.secret(t, secretSetup)[keyProviders]; got != modelsOwn {
		t.Errorf("providers = %q", got)
	}
	// Every pod sees setup complete, not just the one that finished it.
	for _, s := range []*testServer{a, b, fresh} {
		if in, err := s.h.Installed(t.Context()); err != nil || in == nil {
			t.Errorf("Installed = %v, %v", in, err)
		}
	}
}

// The Secrets setup can't read or write get a page naming the cause.
func TestSecretsUnavailable(t *testing.T) {
	srv, _ := newSetup(t)
	if err := srv.kube.CoreV1().Secrets("exemplar").Delete(t.Context(), secretSetup, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	p := get(t, anonymous(t), srv.URL+"/setup")
	if p.status != http.StatusServiceUnavailable {
		t.Errorf("Secret missing = %d", p.status)
	}
	mustContain(t, p, "The <code>exemplar-setup</code> Secret isn't in the cluster", "kubectl apply -k", labelBack)

	down, _ := newSetup(t)
	down.kube.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, secretSetup, errors.New("no role"))
	})
	p = get(t, anonymous(t), down.URL+"/setup")
	if p.status != http.StatusServiceUnavailable {
		t.Errorf("API refused = %d", p.status)
	}
	mustContain(t, p, "exemplar-api couldn't reach its Secrets", "exemplar-api-setup", labelBack)
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

// A config repo under a personal account registers the App in the user's
// own settings: GitHub has no organizations/<user> page.
func TestUserOwnedConfigRepo(t *testing.T) {
	srv, fake := newSetup(t)
	fake.mu.Lock()
	fake.userOwner = true
	fake.mu.Unlock()
	c := browser(t, srv)
	p := get(t, c, srv.URL+"/setup")
	action := actionRe.FindStringSubmatch(p.body)
	if action == nil {
		t.Fatalf("no manifest form in:\n%s", p.body)
	}
	au, _ := url.Parse(html.UnescapeString(action[1]))
	if au.Path != "/settings/apps/new" {
		t.Errorf("manifest posts to %s, want the user's own App settings", au)
	}
	state := au.Query().Get("state")
	if p := get(t, c, srv.URL+"/setup/created?code=c1&state="+url.QueryEscape(state)); p.status != http.StatusSeeOther {
		t.Fatalf("created = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	if !strings.Contains(p.body, `href="https://github.com/settings/apps/exemplar-acme"`) {
		t.Errorf("App settings link isn't the user's:\n%s", p.body)
	}
}

func TestExportsManifest(t *testing.T) {
	h, err := New(Config{
		URLs:       envcfg.URLs{API: "https://e.acme.com", Webhook: "https://e.acme.com/webhook", Callback: "https://e.acme.com/oauth/callback"},
		ConfigRepo: "acme/exemplar-config", Namespace: "exemplar", Secrets: KubeSecrets{},
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

// jwtIssuer is the App id in a request's JWT, unverified.
func jwtIssuer(r *http.Request) int {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		Iss json.RawMessage `json:"iss"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.Trim(string(claims.Iss), `"`))
	return n
}
