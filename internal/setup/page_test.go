package setup

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
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
	titleFinish       = "Save the Secret and restart exemplar-api"
	labelCreate       = "Create the App on GitHub"
	labelCheckInstall = "Check the installation"
	labelCreateExport = "Create the exports app on GitHub"
	labelBack         = "Back to setup"
)

// failTitles is each error page's heading.
var failTitles = map[failure]string{
	failSpentState:   "This link from GitHub was already used or has expired",
	failOtherBrowser: "This browser didn't start that registration",
	failConversion:   "GitHub created the App but didn't hand over its key",
	failKey:          "The new App's private key didn't load",
	failStaleForm:    "This page is from before exemplar-api restarted",
	failNotReady:     "There's no Secret yet",
	failManifest:     "Setup couldn't build the App manifest",
	failOtherHost:    "Setup doesn't answer on this address",
}

// Every error page renders, with its heading, a next action and a way back.
func TestFailurePagesRender(t *testing.T) {
	for f, title := range failTitles {
		var b strings.Builder
		if err := pages.ExecuteTemplate(&b, string(f), failPage{Base: "http://localhost:8081"}); err != nil {
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
	c := browser(t)

	p := get(t, c, srv.URL+"/setup")
	if got := currentStep(t, p); got != titleCreate {
		t.Errorf("first step = %q", got)
	}
	mustContain(t, p, labelCreate, "https://exemplar.acme.com/webhook", "EXEMPLAR_API_URL")
	mustNotContain(t, p, labelCheckInstall, labelCreateExport, "rollout restart", "only in this exemplar-api pod's memory",
		"isn't an organization", "organization or a personal account")

	createReview(t, srv.URL, c)
	p = get(t, c, srv.URL+"/setup")
	if got := currentStep(t, p); got != titleInstall {
		t.Errorf("after create = %q", got)
	}
	mustContain(t, p, "Install exemplar-acme on acme", labelCheckInstall, "only in this exemplar-api pod's memory")
	mustNotContain(t, p, labelCreate+"</button>", labelCreateExport, "rollout restart", "kubectl apply")
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
	mustContain(t, p, "Installed on acme (installation 77)", "kubectl apply -f exemplar-github-app.yaml",
		"kubectl -n exemplar rollout restart deployment/exemplar-api", "Enable Device Flow", labelCreateExport,
		"https://exemplar.acme.com/ui/setup")
	mustNotContain(t, p, labelCheckInstall)
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
	mustContain(t, p, "kubectl apply -f exemplar-exports-app.yaml")
	mustNotContain(t, p, labelCreateExport)
	if p := get(t, c, srv.URL+"/setup/exemplar-exports-app.yaml"); p.status != http.StatusOK || !strings.Contains(p.body, "name: exemplar-exports-app") {
		t.Errorf("exports secret = %d\n%s", p.status, p.body)
	}
}

// Install errors show in place, with the install link, and don't stick.
func TestInstallErrorsInline(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t)
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
	c := browser(t)

	// Other host's browser: the state is real, the cookie isn't there.
	state := mustState(t, get(t, c, srv.URL+"/setup").body)
	failurePage(t, get(t, browser(t), srv.URL+"/setup/created?code=c1&state="+state), http.StatusBadRequest, failOtherBrowser)
	failurePage(t, get(t, c, srv.URL+"/setup/created?code=c1&state="+state), http.StatusBadRequest, failSpentState)

	// GitHub refuses the code: two browsers started, and the fake spends
	// each code once.
	other := browser(t)
	state = mustState(t, get(t, c, srv.URL+"/setup").body)
	otherState := mustState(t, get(t, other, srv.URL+"/setup").body)
	if p := get(t, other, srv.URL+"/setup/created?code=c2&state="+otherState); p.status != http.StatusSeeOther {
		t.Fatalf("created = %d", p.status)
	}
	failurePage(t, get(t, c, srv.URL+"/setup/created?code=c2&state="+state), http.StatusBadGateway, failConversion)

	failurePage(t, do(t, c, http.MethodPost, srv.URL+"/setup/installation", url.Values{"form": {"earlier"}}),
		http.StatusForbidden, failStaleForm)
	failurePage(t, get(t, c, srv.URL+"/setup/exemplar-github-app.yaml"), http.StatusNotFound, failNotReady)
	if p := get(t, c, srv.URL+"/setup/nothing.yaml"); p.status != http.StatusNotFound || strings.Contains(p.text(), failTitles[failNotReady]) {
		t.Errorf("unknown file = %d:\n%s", p.status, p.body)
	}
}

func TestNoInstallFormBeforeApp(t *testing.T) {
	srv, _ := newSetup(t)
	c := browser(t)
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
	c := browser(t)
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

// After exemplar-api restarts mid-flow, an old page's Secret link says the
// App's key is gone and where to delete the App.
func TestSecretAfterRestart(t *testing.T) {
	srv, _ := newSetup(t) // a fresh process: nothing created
	failurePage(t, get(t, browser(t), srv.URL+"/setup/exemplar-github-app.yaml"), http.StatusNotFound, failNotReady)
	mustContain(t, get(t, browser(t), srv.URL+"/setup/exemplar-github-app.yaml"), "restarted since")
}
