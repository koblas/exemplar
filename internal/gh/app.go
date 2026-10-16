// Package gh is Exemplar's GitHub layer: App authentication, installation
// tokens down-scoped per holder, rate-limited clients, and posting.
//
// Each holder gets its own token flavor, minted for the fewest
// repos and permissions it needs. Agent workers never receive a write token.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/gofri/go-github-ratelimit/v2/github_ratelimit"
	"github.com/google/go-github/v88/github"
	"golang.org/x/time/rate"

	"github.com/koblas/exemplar/internal/telemetry"
)

// Flavor is a token holder; each gets its own down-scoped token.
type Flavor int

// Token flavors.
const (
	// FlavorWorker is for agent workers in all lanes and the verifier: read
	// the one PR repo.
	FlavorWorker Flavor = iota + 1
	// FlavorContext is for the context provider: read allowlisted repos and
	// their issues.
	FlavorContext
	// FlavorPoster is the only flavor that writes to a PR repo.
	FlavorPoster
	// FlavorConfigWriter writes PRs to exemplar-config and nothing else.
	FlavorConfigWriter
	// FlavorIngest is for the learning plane's ingest and path-events jobs:
	// read one repo's PRs, review threads, commits and CODEOWNERS, and the
	// org's team membership.
	FlavorIngest
	// FlavorGate is for the auto-approve gates: read one repo's branch
	// protection (Administration read) and rulesets for the branch-rules lookup,
	// and the org's team membership (Members read) for the author policy's
	// allowed_authors.
	// It reads no code.
	FlavorGate
	// FlavorExport is the exports app's (`exemplar-exports`): write
	// the generated instruction files of one opted-in repo and open its PR.
	// It never reads PR content and never reaches exemplar-config.
	FlavorExport
	// FlavorSweep is for the PR sweeper: list open PRs (pull requests
	// read) across one owner's opted-in repos, never the config repo.
	FlavorSweep
)

func (f Flavor) String() string {
	switch f {
	case FlavorWorker:
		return "worker"
	case FlavorContext:
		return "context"
	case FlavorPoster:
		return "poster"
	case FlavorConfigWriter:
		return "config-writer"
	case FlavorIngest:
		return "ingest"
	case FlavorGate:
		return "gate"
	case FlavorExport:
		return "export"
	case FlavorSweep:
		return "sweep"
	}
	return fmt.Sprintf("flavor(%d)", int(f))
}

// Permissions returns exactly the permissions a flavor's token carries.
func (f Flavor) Permissions() *github.InstallationPermissions {
	read, write := github.Ptr("read"), github.Ptr("write")
	switch f {
	case FlavorWorker:
		return &github.InstallationPermissions{Contents: read, PullRequests: read}
	case FlavorContext:
		return &github.InstallationPermissions{Contents: read, Issues: read}
	case FlavorPoster:
		return &github.InstallationPermissions{PullRequests: write, Checks: write, SecurityEvents: write}
	case FlavorConfigWriter:
		// Issues: flag issues and owner digests in exemplar-config.
		return &github.InstallationPermissions{Contents: write, PullRequests: write, Issues: write}
	case FlavorIngest:
		return &github.InstallationPermissions{Contents: read, PullRequests: read, Members: read}
	case FlavorGate:
		return &github.InstallationPermissions{Administration: read, Members: read}
	case FlavorExport:
		return &github.InstallationPermissions{Contents: write, PullRequests: write}
	case FlavorSweep:
		return &github.InstallationPermissions{PullRequests: read}
	}
	return nil
}

// AppConfig configures the review app.
type AppConfig struct {
	AppID      int64
	PrivateKey []byte
	// ConfigRepo is "<owner>/exemplar-config"; the only repo a config-writer
	// token may name.
	ConfigRepo string
	// BaseURL overrides https://api.github.com/ (tests, GHES).
	BaseURL string
	// RequestsPerHour is the per-installation REST budget: 15000 on
	// Enterprise Cloud, at least 5000 elsewhere. Defaults to 5000.
	RequestsPerHour int
}

// App mints installation tokens and builds throttled clients.
type App struct {
	cfg  AppConfig
	base http.RoundTripper
	apps *github.Client // authenticated as the App (JWT)

	mu       sync.Mutex
	limiters map[int64]*rate.Limiter
}

// NewApp returns an App. base is the underlying transport (nil for default).
func NewApp(cfg AppConfig, base http.RoundTripper) (*App, error) {
	if cfg.AppID == 0 || len(cfg.PrivateKey) == 0 {
		return nil, errors.New("gh: app id and private key are required")
	}
	if strings.Count(cfg.ConfigRepo, "/") != 1 {
		return nil, fmt.Errorf("gh: config repo %q must be owner/name", cfg.ConfigRepo)
	}
	if cfg.RequestsPerHour <= 0 {
		cfg.RequestsPerHour = 5000
	}
	if base == nil {
		base = http.DefaultTransport
	}
	atr, err := ghinstallation.NewAppsTransport(base, cfg.AppID, cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("gh: app transport: %w", err)
	}
	opts := []github.ClientOptionsFunc{github.WithTransport(atr), github.WithTimeout(30 * time.Second)}
	if cfg.BaseURL != "" {
		atr.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
		opts = append(opts, github.WithURLs(&cfg.BaseURL, &cfg.BaseURL))
	}
	apps, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("gh: app client: %w", err)
	}
	return &App{cfg: cfg, base: base, apps: apps, limiters: map[int64]*rate.Limiter{}}, nil
}

// Token mints an installation token for flavor, restricted to repos
// ("owner/name"). Worker and poster tokens cover exactly one repo; the
// config writer covers only the config repo.
func (a *App) Token(ctx context.Context, installationID int64, f Flavor, repos []string) (*github.InstallationToken, error) {
	if err := a.checkScope(f, repos); err != nil {
		return nil, err
	}
	names := make([]string, len(repos))
	for i, r := range repos {
		_, name, _ := strings.Cut(r, "/")
		names[i] = name
	}
	tok, _, err := a.apps.Apps.CreateInstallationToken(ctx, installationID, &github.InstallationTokenOptions{
		Repositories: names,
		Permissions:  f.Permissions(),
	})
	if err != nil {
		return nil, fmt.Errorf("gh: mint %s token: %w", f, err)
	}
	return tok, nil
}

func (a *App) checkScope(f Flavor, repos []string) error {
	if f.Permissions() == nil {
		return fmt.Errorf("gh: unknown token flavor %d", int(f))
	}
	if len(repos) == 0 {
		return fmt.Errorf("gh: %s token needs at least one repo", f)
	}
	owner := ""
	for _, r := range repos {
		o, name, ok := strings.Cut(r, "/")
		if !ok || o == "" || name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("gh: repo %q must be owner/name", r)
		}
		if owner != "" && !strings.EqualFold(o, owner) {
			return errors.New("gh: one token cannot span installation owners")
		}
		owner = o
	}
	switch f {
	case FlavorWorker, FlavorPoster, FlavorIngest, FlavorGate, FlavorExport:
		if len(repos) != 1 {
			return fmt.Errorf("gh: %s token must cover exactly one repo, got %d", f, len(repos))
		}
		if (f == FlavorIngest || f == FlavorExport) && strings.EqualFold(repos[0], a.cfg.ConfigRepo) {
			return fmt.Errorf("gh: %s token may not reach the config repo", f)
		}
	case FlavorConfigWriter:
		if len(repos) != 1 || !strings.EqualFold(repos[0], a.cfg.ConfigRepo) {
			return fmt.Errorf("gh: config-writer token may only name %s", a.cfg.ConfigRepo)
		}
	case FlavorContext, FlavorSweep:
		if slices.ContainsFunc(repos, func(r string) bool { return strings.EqualFold(r, a.cfg.ConfigRepo) }) {
			return fmt.Errorf("gh: %s token may not read the config repo", f)
		}
	}
	return nil
}

// Client returns a REST client authenticated with a fresh token of flavor,
// throttled by the installation's shared limiter and GitHub's secondary
// rate-limit signals.
func (a *App) Client(ctx context.Context, installationID int64, f Flavor, repos []string) (*github.Client, error) {
	tok, err := a.Token(ctx, installationID, f, repos)
	if err != nil {
		return nil, err
	}
	return a.clientWithToken(installationID, tok.GetToken())
}

func (a *App) clientWithToken(installationID int64, token string) (*github.Client, error) {
	lim := a.limiter(installationID)
	throttled := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if err := lim.Wait(req.Context()); err != nil {
			return nil, fmt.Errorf("gh: rate limiter: %w", err)
		}
		resp, err := a.base.RoundTrip(req)
		status, resource, remaining := 0, "", ""
		if resp != nil {
			status, resource, remaining = resp.StatusCode, resp.Header.Get("X-RateLimit-Resource"), resp.Header.Get("X-RateLimit-Remaining")
		}
		telemetry.GitHubResponse(req.Context(), installationID, status, resource, remaining)
		return resp, err
	})
	opts := []github.ClientOptionsFunc{
		github.WithTransport(github_ratelimit.New(throttled)),
		github.WithAuthToken(token),
		github.WithTimeout(30 * time.Second),
	}
	if a.cfg.BaseURL != "" {
		opts = append(opts, github.WithURLs(&a.cfg.BaseURL, &a.cfg.BaseURL))
	}
	return github.NewClient(opts...)
}

// limiter returns the installation's shared limiter, creating it on first use.
func (a *App) limiter(installationID int64) *rate.Limiter {
	a.mu.Lock()
	defer a.mu.Unlock()
	lim, ok := a.limiters[installationID]
	if !ok {
		perSecond := rate.Limit(float64(a.cfg.RequestsPerHour) / 3600)
		lim = rate.NewLimiter(perSecond, 50)
		a.limiters[installationID] = lim
	}
	return lim
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
