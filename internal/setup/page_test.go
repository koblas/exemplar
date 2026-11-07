package setup

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var currentRe = regexp.MustCompile(`aria-current="step"><h2>([^<]+)</h2>`)

// text is what the page says, entities decoded.
func (p page) text() string { return html.UnescapeString(p.body) }

func currentStep(t *testing.T, p page) string {
	t.Helper()
	m := currentRe.FindAllStringSubmatch(p.body, -1)
	if len(m) != 1 {
		t.Fatalf("want one current step, got %d in:\n%s", len(m), p.body)
	}
	return m[0][1]
}

func mustContain(t *testing.T, p page, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(p.text(), w) {
			t.Errorf("page doesn't say %q:\n%s", w, p.body)
		}
	}
}

func mustNotContain(t *testing.T, p page, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(p.text(), w) {
			t.Errorf("page says %q:\n%s", w, p.body)
		}
	}
}

// The pages' words, as the user reads them (pages/*.html).
const (
	titleCreate       = "Create the App on GitHub"
	titleInstall      = "Install it"
	titleFinish       = "Finish"
	labelFinish       = "Finish setup"
	labelCreate       = "Create the App on GitHub"
	labelCheckInstall = "Check the installation"
	labelCreateExport = "Create the exports app on GitHub"
	labelBack         = "Back to setup"
	titleDone         = "<h2>Setup is complete</h2>"
	labelStartOver    = "<summary>Start over</summary>"
)

// failTitles is each error page's heading.
var failTitles = map[failure]string{
	failSpentState:    "This link from GitHub was already used or has expired",
	failOtherBrowser:  "This browser didn't start that registration",
	failConversion:    "GitHub created the App but didn't hand over its key",
	failKey:           "The new App's private key didn't load",
	failStaleForm:     "This page is out of date",
	failNotReady:      "That step isn't open yet",
	failManifest:      "Setup couldn't build the App manifest",
	failSecretMissing: "The <code>exemplar-setup</code> Secret isn't in the cluster",
	failKubeAPI:       "exemplar-api couldn't reach its Secrets",
}

// Every error page renders, with its heading, a next action and a way back.
func TestFailurePagesRender(t *testing.T) {
	for f, title := range failTitles {
		var b strings.Builder
		if err := pages.ExecuteTemplate(&b, string(f), failPage{Namespace: "exemplar", Secret: "exemplar-setup"}); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		p := page{body: b.String()}
		mustContain(t, p, "<h1>"+title+"</h1>", labelBack)
		if strings.Count(p.body, "<p>") < 3 {
			t.Errorf("%s doesn't say why and what next:\n%s", f, p.body)
		}
	}
}

// failurePage checks an error page: its status, its heading, a way back,
// and the App settings link: GitHub may hold an App by then.
func failurePage(t *testing.T, p page, status int, f failure) {
	t.Helper()
	if p.status != status {
		t.Errorf("status = %d, want %d", p.status, status)
	}
	mustContain(t, p, failTitles[f], labelBack)
	if !strings.Contains(p.body, `href="https://github.com/organizations/acme/settings/apps/"`) {
		t.Errorf("no App settings link:\n%s", p.body)
	}
}

// createReview walks the review App through GitHub's redirect back.
func createReview(t *testing.T, srv string, c *http.Client) {
	t.Helper()
	state := mustState(t, get(t, c, srv+"/setup").body)
	if p := get(t, c, srv+"/setup/created?code=ok&state="+state); p.status != http.StatusSeeOther {
		t.Fatalf("created = %d", p.status)
	}
}

func checkInstall(t *testing.T, srv string, c *http.Client) page {
	t.Helper()
	form := formRe.FindStringSubmatch(get(t, c, srv+"/setup").body)
	if form == nil {
		t.Fatal("no install form")
	}
	return do(t, c, http.MethodPost, srv+"/setup/installation", url.Values{"form": {form[1]}})
}

// Each stage opens one step, shows what the earlier ones did, and offers
// nothing from the later ones.
func TestPageSteps(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t, srv)

	p := get(t, c, srv.URL+"/setup")
	if got := currentStep(t, p); got != titleCreate {
		t.Errorf("first step = %q", got)
	}
	mustContain(t, p, labelCreate, "https://exemplar.acme.com/webhook", "EXEMPLAR_API_URL")
	mustNotContain(t, p, labelCheckInstall, labelCreateExport, labelFinish, "rollout restart", "port-forward",
		"isn't an organization", "organization or a personal account")

	createReview(t, srv.URL, c)
	p = get(t, c, srv.URL+"/setup")
	if got := currentStep(t, p); got != titleInstall {
		t.Errorf("after create = %q", got)
	}
	mustContain(t, p, "Install exemplar-acme on acme", labelCheckInstall, "Its key is saved in <code>exemplar-github-app</code>.")
	mustNotContain(t, p, labelCreate+"</button>", labelCreateExport, labelFinish, "rollout restart", "kubectl apply")
	if !strings.Contains(p.body, `href="https://github.com/apps/exemplar-acme/installations/new"`) {
		t.Errorf("no install link:\n%s", p.body)
	}

	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	if p := checkInstall(t, srv.URL, c); p.status != http.StatusSeeOther {
		t.Fatalf("check installation = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	if got := currentStep(t, p); got != titleFinish {
		t.Errorf("after install = %q", got)
	}
	mustContain(t, p, "(installation 77)", labelFinish, "Enable Device Flow", labelCreateExport, "Setup saves it in <code>exemplar-providers</code>.")
	mustNotContain(t, p, labelCheckInstall, titleDone, "rollout restart", "kubectl apply", "Download",
		"has no in-cluster Postgres") // the in-cluster database: Prepare wrote its URL
	if strings.Contains(p.body, "<details open>") {
		t.Error("rule exports open before anyone asked for them")
	}

	// The exports app, from the last step.
	state := mustState(t, p.body)
	if p := get(t, c, srv.URL+"/setup/created?code=exports&state="+state); p.status != http.StatusSeeOther {
		t.Fatalf("exports created = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	if got := currentStep(t, p); got != titleFinish {
		t.Errorf("after exports = %q", got)
	}
	mustContain(t, p, "Setup saved its key in <code>exemplar-exports-app</code>.")
	mustNotContain(t, p, labelCreateExport)
	if got := srv.secret(t, secretExports); got[keyAppID] != "4343" || got[keyPrivateKey] != fake.pem || got[keyClientSecret] != "" {
		t.Errorf("exemplar-exports-app = %v", got)
	}
	if got := srv.secret(t, secretApp)[keyAppID]; got != "4242" {
		t.Errorf("the exports app replaced the review app: app-id %q", got)
	}

	// Finish: the finishing page, every step done, nothing left to press.
	form := formRe.FindStringSubmatch(p.body)
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/finish", url.Values{"form": {form[1]}, "models": {modelsOwn}}); p.status != http.StatusSeeOther {
		t.Fatalf("finish = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	mustContain(t, p, titleDone, "https://exemplar.acme.com/ui/setup", `<meta http-equiv="refresh" content="5">`)
	mustNotContain(t, p, labelFinish, labelStartOver, labelCheckInstall, `aria-current="step"`)
	if strings.Count(p.body, `class="done"`) != 3 {
		t.Errorf("not every step reads done:\n%s", p.body)
	}
}

// Without an in-cluster Postgres, the last step asks for exemplar-database
// and setup doesn't finish until it's there.
func TestExternalDatabase(t *testing.T) {
	srv, fake := newSetupWith(t, func(c *Config) { c.InClusterPostgres = func(context.Context) bool { return false } })
	c := browser(t, srv)
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	createReview(t, srv.URL, c)
	p := get(t, c, srv.URL+"/setup")
	mustContain(t, p, "This installation has no in-cluster Postgres", "Check again", labelFinish)
	form := formRe.FindStringSubmatch(p.body)
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/finish", url.Values{"form": {form[1]}, "anthropic-key": {"sk-ant"}}); p.status != http.StatusSeeOther {
		t.Fatalf("finish = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	mustNotContain(t, p, titleDone)
	mustContain(t, p, "This installation has no in-cluster Postgres")
	if in, _ := srv.h.Installed(t.Context()); in != nil {
		t.Fatal("Installed without a database")
	}

	s, err := srv.kube.CoreV1().Secrets("exemplar").Get(t.Context(), secretDatabase, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.Data = map[string][]byte{keyDatabaseURL: []byte("postgres://db.example/exemplar")}
	if _, err := srv.kube.CoreV1().Secrets("exemplar").Update(t.Context(), s, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	mustContain(t, get(t, c, srv.URL+"/setup"), titleDone)
	if in, _ := srv.h.Installed(t.Context()); in == nil || in.DatabaseURL != "postgres://db.example/exemplar" {
		t.Errorf("Installed = %+v", in)
	}
}

// The models field: missing and malformed keys show in place and write
// nothing.
func TestProviderKey(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t, srv)
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	createReview(t, srv.URL, c)
	form := formRe.FindStringSubmatch(get(t, c, srv.URL+"/setup").body)
	for key, want := range map[string]string{
		"":              "Paste your Anthropic API key, or choose that you've created exemplar-providers yourself.",
		"sk ant":        "That doesn't look like an API key",
		"sk-ant-\u00e9": "That doesn't look like an API key",
	} {
		p := do(t, c, http.MethodPost, srv.URL+"/setup/finish", url.Values{"form": {form[1]}, "anthropic-key": {key}})
		if p.status != http.StatusUnprocessableEntity || currentStep(t, p) != titleFinish {
			t.Errorf("key %q = %d", key, p.status)
		}
		mustContain(t, p, want)
		if strings.Contains(p.body, key) && key != "" {
			t.Errorf("key %q echoed back", key)
		}
	}
	if len(srv.secret(t, secretProviders)) != 0 {
		t.Error("a rejected key reached exemplar-providers")
	}
}

// Install errors show in place, with the install link, and don't stick.
func TestInstallErrorsInline(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t, srv)
	createReview(t, srv.URL, c)

	p := checkInstall(t, srv.URL, c)
	if p.status != http.StatusConflict || currentStep(t, p) != titleInstall {
		t.Fatalf("not installed = %d", p.status)
	}
	mustContain(t, p, "GitHub lists no installation of exemplar-acme on acme.", "Install it on acme (the account that owns exemplar-config)", labelCheckInstall)
	if !strings.Contains(p.body, `href="https://github.com/apps/exemplar-acme/installations/new"`) {
		t.Errorf("not-installed error without the install link:\n%s", p.body)
	}
	mustNotContain(t, get(t, c, srv.URL+"/setup"), "GitHub lists no installation")

	fake.mu.Lock()
	fake.listDown = true
	fake.mu.Unlock()
	p = checkInstall(t, srv.URL, c)
	if p.status != http.StatusBadGateway || currentStep(t, p) != titleInstall {
		t.Fatalf("GitHub down = %d", p.status)
	}
	mustContain(t, p, "GitHub didn't answer when setup asked for the App's installations.", labelCheckInstall)
}

func TestErrorPages(t *testing.T) {
	srv, _ := newSetup(t)
	c := browser(t, srv)

	// Other host's browser: the state is real, the cookie isn't there.
	state := mustState(t, get(t, c, srv.URL+"/setup").body)
	failurePage(t, get(t, browser(t, srv), srv.URL+"/setup/created?code=c1&state="+state), http.StatusBadRequest, failOtherBrowser)
	failurePage(t, get(t, c, srv.URL+"/setup/created?code=c1&state=review-0.1.00"), http.StatusBadRequest, failSpentState)

	// GitHub refuses the code: two browsers started, and the fake spends
	// each code once.
	other := browser(t, srv)
	state = mustState(t, get(t, c, srv.URL+"/setup").body)
	otherState := mustState(t, get(t, other, srv.URL+"/setup").body)
	if p := get(t, other, srv.URL+"/setup/created?code=c2&state="+otherState); p.status != http.StatusSeeOther {
		t.Fatalf("created = %d", p.status)
	}
	failurePage(t, get(t, c, srv.URL+"/setup/created?code=c2&state="+state), http.StatusBadGateway, failConversion)

	failurePage(t, do(t, c, http.MethodPost, srv.URL+"/setup/installation", url.Values{"form": {"earlier"}}),
		http.StatusForbidden, failStaleForm)
	// The App exists (the other browser), but isn't installed yet.
	form := formRe.FindStringSubmatch(get(t, c, srv.URL+"/setup").body)
	p := do(t, c, http.MethodPost, srv.URL+"/setup/finish", url.Values{"form": {form[1]}, "models": {modelsOwn}})
	if p.status != http.StatusConflict {
		t.Errorf("finish before install = %d", p.status)
	}
	mustContain(t, p, failTitles[failNotReady], labelBack)
}

func TestNoInstallFormBeforeApp(t *testing.T) {
	srv, _ := newSetup(t)
	c := browser(t, srv)
	form := formRe.FindStringSubmatch(get(t, c, srv.URL+"/setup").body)
	if form != nil {
		t.Fatal("install form before the App exists")
	}
}

// When GitHub won't say what kind of account owns exemplar-config, the
// page says it assumed an organization, and asks again on the next load.
func TestOwnerUnknown(t *testing.T) {
	srv, fake := newSetup(t)
	fake.mu.Lock()
	fake.ownerDown = true
	fake.mu.Unlock()
	c := browser(t, srv)
	p := get(t, c, srv.URL+"/setup")
	mustContain(t, p, "GitHub didn't say whether acme is an organization or a personal account")
	if currentStep(t, p) != titleCreate {
		t.Error("the notice replaced the create step")
	}

	fake.mu.Lock()
	fake.ownerDown, fake.userOwner = false, true
	fake.mu.Unlock()
	p = get(t, c, srv.URL+"/setup")
	mustNotContain(t, p, "organization or a personal account")
	if a := actionRe.FindStringSubmatch(p.body); a == nil || !strings.Contains(a[1], "github.com/settings/apps/new") {
		t.Errorf("after GitHub answered, the form still posts to %v", a)
	}
}

// A form whose token no longer matches setup's key (exemplar-setup was
// emptied and written again) says to open setup again.
func TestStaleForm(t *testing.T) {
	srv, _ := newSetup(t)
	p := do(t, browser(t, srv), http.MethodPost, srv.URL+"/setup/start-over", url.Values{"form": {"from-an-old-page"}})
	failurePage(t, p, http.StatusForbidden, failStaleForm)
	mustContain(t, p, "Open setup again")
}
