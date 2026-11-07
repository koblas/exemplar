package setup

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The page checks GitHub on every load: an install done elsewhere moves
// it on without a button, a missing config repo or an undone install moves
// it back, and a GitHub outage keeps the last known state with a notice.
func TestInstallReconciled(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t, srv)
	createReview(t, srv.URL, c)
	set := func(f func()) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		f()
	}

	// Not installed yet: the install step, with no error on a plain load.
	p := get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleInstall {
		t.Fatalf("before install = %q", currentStep(t, p))
	}
	mustNotContain(t, p, "GitHub lists no installation")

	// Installed without exemplar-config: still the install step, saying
	// what's missing and linking the installation's settings.
	set(func() { fake.installed, fake.noConfig = true, true })
	p = get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleInstall {
		t.Fatalf("without the config repo = %q", currentStep(t, p))
	}
	mustContain(t, p, "is installed on acme, but its repositories", labelCheckInstall)
	if !strings.Contains(p.body, `href="https://github.com/organizations/acme/settings/installations/77" target="_blank"`) {
		t.Errorf("no link to the installation's settings:\n%s", p.body)
	}
	if p := checkInstall(t, srv.URL, c); p.status != http.StatusConflict {
		t.Errorf("check without the config repo = %d", p.status)
	}
	if got := srv.secret(t, secretApp)[keyInstallation]; got != "" {
		t.Errorf("installation-id %q recorded without the config repo", got)
	}

	// Fixed on GitHub: a reload is enough.
	set(func() { fake.noConfig = false })
	if p := get(t, c, srv.URL+"/setup"); currentStep(t, p) != titleFinish {
		t.Fatalf("after adding the config repo = %q", currentStep(t, p))
	}

	// GitHub down: the last known state stands, and the page says so.
	set(func() { fake.listDown = true })
	p = get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleFinish {
		t.Errorf("GitHub down = %q", currentStep(t, p))
	}
	mustContain(t, p, "GitHub didn't answer this page's checks")
	set(func() { fake.listDown = false })

	if got := srv.secret(t, secretApp)[keyInstallation]; got != "77" {
		t.Errorf("installation-id = %q after a reload", got)
	}

	// Uninstalled since: back to the install step, and forgotten.
	set(func() { fake.installed = false })
	if p := get(t, c, srv.URL+"/setup"); currentStep(t, p) != titleInstall {
		t.Errorf("after uninstall = %q", currentStep(t, p))
	}
	if got := srv.secret(t, secretApp)[keyInstallation]; got != "" {
		t.Errorf("installation-id %q kept after uninstall", got)
	}
}

// installed is a signed-in browser on the last step: the App created and
// installed with the config repo.
func installed(t *testing.T, srv *testServer, fake *fakeGitHub) *http.Client {
	t.Helper()
	c := browser(t, srv)
	fake.mu.Lock()
	fake.installed = true
	fake.mu.Unlock()
	createReview(t, srv.URL, c)
	if p := get(t, c, srv.URL+"/setup"); currentStep(t, p) != titleFinish {
		t.Fatalf("installed = %q", currentStep(t, p))
	}
	return c
}

// finish presses Finish with the models choice made elsewhere.
func finish(t *testing.T, srv *testServer, c *http.Client) page {
	t.Helper()
	form := formRe.FindStringSubmatch(get(t, c, srv.URL+"/setup").body)
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/finish", url.Values{"form": {form[1]}, "models": {modelsOwn}}); p.status != http.StatusSeeOther {
		t.Fatalf("finish = %d", p.status)
	}
	return get(t, c, srv.URL+"/setup")
}

var linkRe = regexp.MustCompile(`<a href="(https?://[^"]+)"([^>]*)>`)

// Links out of setup open in a new tab, so this page and its state stay
// where they are; only the create forms leave it, since GitHub comes back.
func TestLinksOpenNewTab(t *testing.T) {
	srv, fake := newSetup(t)
	c := installed(t, srv, fake)
	pages := []page{get(t, c, srv.URL+"/setup"), get(t, c, srv.URL+"/setup/created?code=x&state=y"), finish(t, srv, c)}
	n := 0
	for _, p := range pages {
		for _, m := range linkRe.FindAllStringSubmatch(p.body, -1) {
			n++
			if !strings.Contains(m[2], `target="_blank"`) || !strings.Contains(m[2], `rel="noopener"`) {
				t.Errorf("%s opens in this tab", m[1])
			}
		}
	}
	if n < 4 {
		t.Errorf("only %d outside links checked", n)
	}
}

// The exports app must never reach exemplar-config; the page checks.
func TestExportsOnConfig(t *testing.T) {
	srv, fake := newSetup(t)
	c := installed(t, srv, fake)
	state := mustState(t, get(t, c, srv.URL+"/setup").body)
	if p := get(t, c, srv.URL+"/setup/created?code=exports&state="+state); p.status != http.StatusSeeOther {
		t.Fatalf("exports created = %d", p.status)
	}
	// The fake answers the config repo's installation for any App, as if
	// the exports app were installed there too.
	mustContain(t, get(t, c, srv.URL+"/setup"), "where it could\nrewrite the rules", "Remove exemplar-config from its repositories")
	fake.mu.Lock()
	fake.noConfig = true
	fake.mu.Unlock()
	p := get(t, c, srv.URL+"/setup")
	mustNotContain(t, p, "where it could")
}

// An App deleted (or renamed) on GitHub after setup created it: the page
// follows a rename, and for a deleted one goes back to step 1 offering to
// forget it.
func TestAppDeletedOrRenamed(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized} {
		srv, fake := newSetup(t)
		c := browser(t, srv)
		createReview(t, srv.URL, c)

		fake.mu.Lock()
		fake.slug = "exemplar-2-koblas"
		fake.mu.Unlock()
		p := get(t, c, srv.URL+"/setup")
		mustContain(t, p, "Install exemplar-2-koblas on acme")
		if !strings.Contains(p.body, `href="https://github.com/apps/exemplar-2-koblas/installations/new"`) {
			t.Errorf("install link doesn't follow the rename:\n%s", p.body)
		}

		fake.mu.Lock()
		fake.appGone = status
		fake.mu.Unlock()
		p = get(t, c, srv.URL+"/setup")
		if currentStep(t, p) != titleCreate {
			t.Fatalf("%d: after delete = %q", status, currentStep(t, p))
		}
		// Setup doesn't forget on its own (a 401 can be this pod's clock):
		// it says what happened and offers to forget.
		mustContain(t, p, "GitHub no longer knows exemplar-2-koblas", "Forget exemplar-2-koblas and start over")
		mustNotContain(t, p, labelCreate+"</button>", "<summary>Start over</summary>", "only in this exemplar-api pod's memory")
		form := formRe.FindStringSubmatch(p.body)
		if r := do(t, c, http.MethodPost, srv.URL+"/setup/start-over", map[string][]string{"form": {form[1]}}); r.status != http.StatusSeeOther {
			t.Fatalf("start over = %d", r.status)
		}

		// Created again: a fresh state, a working flow.
		fake.mu.Lock()
		fake.appGone, fake.slug = 0, ""
		fake.mu.Unlock()
		p = get(t, c, srv.URL+"/setup?started-over")
		if currentStep(t, p) != titleCreate {
			t.Fatalf("after start over = %q", currentStep(t, p))
		}
		mustNotContain(t, p, "GitHub no longer knows")
		state := mustState(t, p.body)
		if p := get(t, c, srv.URL+"/setup/created?code=again&state="+state); p.status != http.StatusSeeOther {
			t.Fatalf("created again = %d", p.status)
		}
		p = get(t, c, srv.URL+"/setup")
		if currentStep(t, p) != titleInstall {
			t.Errorf("after creating again = %q", currentStep(t, p))
		}
		mustNotContain(t, p, "GitHub no longer knows")
	}
}

// Start over forgets the Apps, from any step, and says where to delete
// them on GitHub.
func TestStartOver(t *testing.T) {
	srv, fake := newSetup(t)
	c := installed(t, srv, fake)
	p := get(t, c, srv.URL+"/setup")
	mustContain(t, p, "Start over", "Forget this App and start over")
	if !strings.Contains(p.body, `href="https://github.com/organizations/acme/settings/apps/exemplar-acme/advanced" target="_blank"`) {
		t.Errorf("no link to delete the App:\n%s", p.body)
	}
	form := formRe.FindStringSubmatch(p.body)
	if r := do(t, c, http.MethodPost, srv.URL+"/setup/start-over", map[string][]string{"form": {"forged"}}); r.status != http.StatusForbidden {
		t.Errorf("forged start over = %d", r.status)
	}
	r := do(t, c, http.MethodPost, srv.URL+"/setup/start-over", map[string][]string{"form": {form[1]}})
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/setup?started-over" {
		t.Fatalf("start over = %d %v", r.status, r.header)
	}
	p = get(t, c, srv.URL+"/setup?started-over")
	if currentStep(t, p) != titleCreate {
		t.Fatalf("after start over = %q", currentStep(t, p))
	}
	mustContain(t, p, "Setup forgot the App it created", labelCreate)
	if !strings.Contains(p.body, `href="https://github.com/organizations/acme/settings/apps/" target="_blank"`) {
		t.Errorf("no App settings link:\n%s", p.body)
	}
	mustNotContain(t, get(t, c, srv.URL+"/setup"), "Setup forgot the App")
}

// A deleted exports app is called out inside rule exports, and forgetting
// it leaves the review app alone.
func TestExportsAppDeleted(t *testing.T) {
	srv, fake := newSetup(t)
	c := installed(t, srv, fake)
	state := mustState(t, get(t, c, srv.URL+"/setup").body)
	if p := get(t, c, srv.URL+"/setup/created?code=exports&state="+state); p.status != http.StatusSeeOther {
		t.Fatalf("exports created = %d", p.status)
	}
	fake.mu.Lock()
	fake.appGone, fake.goneID = http.StatusNotFound, 4343
	fake.mu.Unlock()
	p := get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleFinish {
		t.Fatalf("exports gone moved the page to %q", currentStep(t, p))
	}
	mustContain(t, p, "Forget exemplar-acme</button>")
	form := formRe.FindStringSubmatch(p.body)
	r := do(t, c, http.MethodPost, srv.URL+"/setup/start-over", map[string][]string{"form": {form[1]}, "kind": {KindExports}})
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/setup" {
		t.Fatalf("forget exports = %d %v", r.status, r.header)
	}
	fake.mu.Lock()
	fake.appGone = 0
	fake.mu.Unlock()
	p = get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleFinish {
		t.Errorf("after forgetting exports = %q", currentStep(t, p))
	}
	mustContain(t, p, labelCreateExport)
}

// GitHub fails silently for missing org roles; the page says which.
func TestRoleHints(t *testing.T) {
	srv, _ := newSetup(t)
	c := browser(t, srv)
	mustContain(t, get(t, c, srv.URL+"/setup"), "GitHub App manager role", "answers the button with a 404 page")
	createReview(t, srv.URL, c)
	mustContain(t, checkInstall(t, srv.URL, c), "GitHub sent its owners a request to approve the installation")

	user, ufake := newSetup(t)
	ufake.mu.Lock()
	ufake.userOwner = true
	ufake.mu.Unlock()
	uc := browser(t, user)
	mustNotContain(t, get(t, uc, user.URL+"/setup"), "GitHub App manager role")
	createReview(t, user.URL, uc)
	mustNotContain(t, checkInstall(t, user.URL, uc), "request to approve")
}
