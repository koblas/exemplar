package setup

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The page checks GitHub on every load: an install done elsewhere moves
// it on without a button, a missing config repo or an undone install moves
// it back, and a GitHub outage keeps the last known state with a notice.
func TestInstallReconciled(t *testing.T) {
	srv, fake := newSetup(t)
	c := browser(t)
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
	if p := downloadSecrets(t, srv.URL, c, map[string][]string{"models": {modelsOwn}}); p.status != http.StatusConflict {
		t.Errorf("secrets without the config repo = %d", p.status)
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

	// Uninstalled since: back to the install step.
	set(func() { fake.installed = false })
	if p := get(t, c, srv.URL+"/setup"); currentStep(t, p) != titleInstall {
		t.Errorf("after uninstall = %q", currentStep(t, p))
	}
}

// The last step watches exemplar-github-app's mount: the restart shows
// only once this App's Secret is applied, and another App's is called out.
func TestAppSecretReconciled(t *testing.T) {
	dir := t.TempDir()
	srv, fake := newSetupWith(t, func(c *Config) { c.AppSecretDir = dir })
	c := installed(t, srv.URL, fake)
	key := filepath.Join(dir, "private-key")

	p := get(t, c, srv.URL+"/setup")
	mustContain(t, p, "Check again", "Download exemplar-secrets.yaml")
	mustNotContain(t, p, "belongs to another App", "is applied", titleDone)
	// No dead end if the kubelet never shows the mount: a way to check by
	// hand, and the restart behind it.
	if _, hatch, ok := strings.Cut(p.body, "Applied it, and this page still doesn't show it?"); !ok ||
		!strings.Contains(hatch, "kubectl -n exemplar get secret exemplar-github-app") ||
		!strings.Contains(hatch, "rollout restart deployment/exemplar-api") {
		t.Errorf("no way forward without the mount:\n%s", p.body)
	} else if strings.Count(p.body, "rollout restart") != 1 {
		t.Error("the restart shows outside the by-hand check before the Secret is applied")
	}

	if err := os.WriteFile(key, []byte(newTestKey(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	p = get(t, c, srv.URL+"/setup")
	mustContain(t, p, "belongs to another App", "Download exemplar-secrets.yaml")
	mustNotContain(t, p, "is applied", titleDone)

	if err := os.WriteFile(key, []byte(fake.pem), 0o600); err != nil {
		t.Fatal(err)
	}
	p = get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleFinish {
		t.Fatalf("applied = %q", currentStep(t, p))
	}
	mustContain(t, p, "exemplar-secrets.yaml is applied", "kubectl -n exemplar rollout restart deployment/exemplar-api",
		"https://exemplar.acme.com/ui/setup", "Download the file again")
	mustNotContain(t, p, "Check again", "belongs to another App", "Until you apply the Secrets file")
	doneState(t, p)
	// Start over stays, collapsed under the steps, and still works.
	mustContain(t, p, "open <b>Start over</b> at the bottom of this page", labelStartOver, "Forget this App and start over")
	if !strings.Contains(p.body, `<details><summary>Start over</summary>`) ||
		strings.Index(p.body, labelStartOver) < strings.Index(p.body, "</ol>") {
		t.Error("Start over is open or above the steps in the done state")
	}
	if p := do(t, c, http.MethodPost, srv.URL+"/setup/start-over", url.Values{"form": {formRe.FindStringSubmatch(p.body)[1]}}); p.status != http.StatusSeeOther {
		t.Fatalf("start over = %d", p.status)
	}
	p = get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleCreate {
		t.Errorf("after start over = %q", currentStep(t, p))
	}
	mustNotContain(t, p, titleDone)
}

// doneState checks the done state: the banner leads, names the restart
// and /ui/setup, every step reads done, and the restart shows once.
func doneState(t *testing.T, p page) {
	t.Helper()
	mustContain(t, p, titleDone, "nothing on this page is left to do",
		"kubectl -n exemplar rollout restart deployment/exemplar-api", "https://exemplar.acme.com/ui/setup")
	banner, steps, ok := strings.Cut(p.body, `<ol class="steps">`)
	if !ok || !strings.Contains(banner, titleDone) || !strings.Contains(banner, "rollout restart") {
		t.Errorf("the banner isn't above the steps, with the restart:\n%s", p.body)
	}
	if strings.Contains(steps, "rollout restart") {
		t.Error("the restart shows twice")
	}
	if strings.Contains(p.body, `class="current"`) || strings.Contains(p.body, `class="todo"`) {
		t.Errorf("a step doesn't read done:\n%s", p.body)
	}
}

var linkRe = regexp.MustCompile(`<a href="(https?://[^"]+)"([^>]*)>`)

// Links out of setup open in a new tab, so this page and its state stay
// where they are; only the create forms leave it, since GitHub comes back.
func TestLinksOpenNewTab(t *testing.T) {
	dir := t.TempDir()
	srv, fake := newSetupWith(t, func(c *Config) { c.AppSecretDir = dir })
	c := installed(t, srv.URL, fake)
	if err := os.WriteFile(filepath.Join(dir, "private-key"), []byte(fake.pem), 0o600); err != nil {
		t.Fatal(err)
	}
	pages := []page{get(t, c, srv.URL+"/setup"), get(t, browser(t), srv.URL+"/setup/created?code=x&state=y")}
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
	c := installed(t, srv.URL, fake)
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
		c := browser(t)
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
	c := installed(t, srv.URL, fake)
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
	c := installed(t, srv.URL, fake)
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

// A setup pod holding no App that sees an applied App Secret (it restarted
// after the apply, or it's the other replica) offers the restart, not a
// second App; with no database yet, it holds the restart back.
func TestAppliedSecretOnNewPod(t *testing.T) {
	appDir, dbDir := t.TempDir(), t.TempDir()
	dbFile := filepath.Join(dbDir, "url")
	srv, _ := newSetupWith(t, func(c *Config) {
		c.AppSecretDir, c.DatabaseFile = appDir, dbFile
		c.InClusterPostgres = func(context.Context) bool { return false }
	})
	c := browser(t)
	for name, body := range map[string]string{"app-id": "4242\n", "private-key": newTestKey(t)} {
		if err := os.WriteFile(filepath.Join(appDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	p := get(t, c, srv.URL+"/setup")
	if currentStep(t, p) != titleCreate {
		t.Fatalf("step = %q", currentStep(t, p))
	}
	mustContain(t, p, "This cluster already has the Secret for App 4242", "Create a different App instead",
		"Before the restart, create <code>exemplar-database</code>", "Check again")
	mustNotContain(t, p, "rollout restart", "only in this exemplar-api pod's memory", titleDone)
	if mustState(t, p.body) == "" {
		t.Error("no way to create a different App")
	}

	if err := os.WriteFile(dbFile, []byte("postgres://db.internal/exemplar"), 0o600); err != nil {
		t.Fatal(err)
	}
	p = get(t, c, srv.URL+"/setup")
	mustContain(t, p, "This cluster already has the Secret for App 4242",
		"kubectl -n exemplar rollout restart deployment/exemplar-api", "exemplar-api-crash-loops-after-setup",
		"Create a different App instead")
	mustNotContain(t, p, "Before the restart, create <code>exemplar-database</code>", labelStartOver)
	doneState(t, p)
}

// After applying the file with an external Postgres and no exemplar-database,
// step 3 doesn't offer the restart that would crash-loop exemplar-api.
func TestRestartWaitsForDatabase(t *testing.T) {
	appDir, dbFile := t.TempDir(), filepath.Join(t.TempDir(), "url")
	srv, fake := newSetupWith(t, func(c *Config) {
		c.AppSecretDir, c.DatabaseFile = appDir, dbFile
		c.InClusterPostgres = func(context.Context) bool { return false }
	})
	c := installed(t, srv.URL, fake)
	if err := os.WriteFile(filepath.Join(appDir, "private-key"), []byte(fake.pem), 0o600); err != nil {
		t.Fatal(err)
	}
	p := get(t, c, srv.URL+"/setup")
	mustContain(t, p, "exemplar-secrets.yaml is applied", "Before the restart, create <code>exemplar-database</code>")
	mustNotContain(t, p, "rollout restart", titleDone)

	if err := os.WriteFile(dbFile, []byte("postgres://db.internal/exemplar"), 0o600); err != nil {
		t.Fatal(err)
	}
	p = get(t, c, srv.URL+"/setup")
	mustContain(t, p, "kubectl -n exemplar rollout restart deployment/exemplar-api")
	mustNotContain(t, p, "Before the restart")
	doneState(t, p)
	// The file leaves out a database Secret that now exists.
	file := secretsIn(t, downloadSecrets(t, srv.URL, c, map[string][]string{"models": {modelsOwn}}).body)
	if _, ok := file["exemplar-database"]; ok {
		t.Error("the file replaces exemplar-database")
	}
}

// GitHub fails silently for missing org roles; the page says which.
func TestRoleHints(t *testing.T) {
	srv, _ := newSetup(t)
	c := browser(t)
	mustContain(t, get(t, c, srv.URL+"/setup"), "GitHub App manager role", "answers the button with a 404 page")
	createReview(t, srv.URL, c)
	mustContain(t, checkInstall(t, srv.URL, c), "GitHub sent its owners a request to approve the installation")

	user, ufake := newSetup(t)
	ufake.mu.Lock()
	ufake.userOwner = true
	ufake.mu.Unlock()
	uc := browser(t)
	mustNotContain(t, get(t, uc, user.URL+"/setup"), "GitHub App manager role")
	createReview(t, user.URL, uc)
	mustNotContain(t, checkInstall(t, user.URL, uc), "request to approve")
}
