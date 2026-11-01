package setup

import (
	"net/http"
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
	mustNotContain(t, p, "belongs to another App", "is applied")
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
	mustNotContain(t, p, "is applied")

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
