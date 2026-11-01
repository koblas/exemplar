package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/koblas/exemplar/internal/gh"
)

// Every load of the setup page checks GitHub and the pod's mounts against
// what setup expects, so a skipped or undone step shows as the current one
// with what to do, and a reload (or the step's Check button) picks up what
// was fixed elsewhere.

var errConfigMissing = errors.New("setup: installation doesn't include the config repo")

// Install states on the install step (pages/status.html).
const (
	installNotFound      = "not-installed"  // after an explicit check only: on a load it's the step's normal state
	installListFailed    = "list-failed"    // GitHub didn't answer
	installConfigMissing = "config-missing" // installed, without exemplar-config
)

// App Secret states on the last step: what exemplar-github-app, mounted
// into this pod while it runs, holds.
const (
	secretNotApplied = "not-applied" // not mounted yet (or the kubelet hasn't caught up)
	secretApplied    = "applied"     // this App's key: restart next
	secretOther      = "other"       // another App's key, from an earlier setup run
	secretUnknown    = "unknown"     // no mount to look at (AppSecretDir unset)
)

// reconcileApps asks GitHub about each App setup created: a renamed one
// gets its new name and links; one GitHub no longer knows (deleted, its
// key revoked, or this pod's clock too far off for its JWT) is marked
// gone. Setup never forgets an App on its own, since its key exists only
// here: the page offers Start over. It returns the gone Apps' names, by
// kind, and whether GitHub answered every question.
func (h *Handler) reconcileApps(ctx context.Context) (map[string]string, bool) {
	gone, answered := map[string]string{}, true
	for _, kind := range []string{KindReview, KindExports} {
		h.mu.Lock()
		a := h.apps[kind]
		h.mu.Unlock()
		if a == nil {
			continue
		}
		slug, htmlURL, err := a.client.Describe(ctx)
		h.mu.Lock()
		switch {
		case errors.Is(err, gh.ErrNotFound):
			gone[kind] = a.created.Slug
		case err != nil:
			answered = false
		case slug != "":
			a.created.Slug, a.created.HTMLURL = slug, cmpOr(htmlURL, a.created.HTMLURL)
		}
		h.mu.Unlock()
		if errors.Is(err, gh.ErrNotFound) {
			h.cfg.Log.Warn("setup: GitHub no longer knows the App", "kind", kind, "slug", a.created.Slug)
		} else if err != nil {
			h.cfg.Log.Warn("setup: reading the App failed", "kind", kind, "err", err)
		}
	}
	return gone, answered
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// installState asks GitHub whether a's App is installed on the config
// repo's owner, with the config repo among its repositories. It returns
// the installation id with errConfigMissing when only the repo is missing.
func (h *Handler) installState(ctx context.Context, a *app) (int64, error) {
	id, err := a.client.OrgInstallation(ctx, h.org)
	if err != nil {
		return 0, fmt.Errorf("installation on %s: %w", h.org, err)
	}
	if _, err := a.client.ConfigInstallation(ctx); errors.Is(err, gh.ErrNotFound) {
		return id, errConfigMissing
	} else if err != nil {
		return id, fmt.Errorf("installation on %s: %w", h.cfg.ConfigRepo, err)
	}
	return id, nil
}

// reconcileInstall checks a's installation and records what GitHub said.
// It returns the installation id, the in-place state for the install step
// ("" when it's fine or simply not done yet), and whether GitHub answered;
// when it didn't, the last known installation stands.
func (h *Handler) reconcileInstall(ctx context.Context, a *app) (int64, string, bool) {
	id, err := h.installState(ctx, a)
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case err == nil:
		a.installation, a.configMissing = id, false
		return id, "", true
	case errors.Is(err, errConfigMissing):
		a.installation, a.configMissing = id, true
		return id, installConfigMissing, true
	case errors.Is(err, gh.ErrNotFound):
		a.installation, a.configMissing = 0, false // never installed, or uninstalled since
		return 0, "", true
	default:
		h.cfg.Log.Warn("setup: checking the installation failed", "err", err)
		if a.configMissing {
			return a.installation, installConfigMissing, false
		}
		return a.installation, "", false
	}
}

// appSecretState reads the private key exemplar-github-app mounts into this
// pod (optional, filled in while the pod runs) and compares it with a's.
func (h *Handler) appSecretState(a *app) string {
	if h.cfg.AppSecretDir == "" {
		return secretUnknown
	}
	b, err := os.ReadFile(filepath.Join(h.cfg.AppSecretDir, "private-key"))
	switch {
	case err != nil || len(bytes.TrimSpace(b)) == 0:
		return secretNotApplied
	case strings.TrimSpace(string(b)) == strings.TrimSpace(a.created.PEM):
		return secretApplied
	default:
		return secretOther
	}
}

// appliedAppID is the app-id exemplar-github-app mounts into this pod, or
// "" when none is mounted.
func (h *Handler) appliedAppID() string {
	if h.cfg.AppSecretDir == "" || !mounted(filepath.Join(h.cfg.AppSecretDir, "private-key")) {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(h.cfg.AppSecretDir, "app-id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// installationURL is where the owner changes the installation's
// repositories.
func (h *Handler) installationURL(ctx context.Context, id int64) string {
	p, _ := h.settingsPath(ctx)
	return h.cfg.GitHubWeb + strings.TrimSuffix(p, "apps/") + fmt.Sprintf("installations/%d", id)
}
