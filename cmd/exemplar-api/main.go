// Command exemplar-api is Exemplar's public HTTP surface. It serves the
// signed feedback links of review comments, and /v1/evaluate for
// `exemplar review`:
//
//	exemplar-api [--addr :8080] [--evaluate-daily-usd 5] [--evaluate-daily-runs 50] [--evaluate-per-minute 6]
//
// /ui/ is the web UI: rules, findings, candidates, the
// auto-approve audit, cost and the playground, after sign-in, with
// repo-specific rows only for repos the viewer can read.
//
// Sign-in sources follow Coder's (internal/api/signin.go): GitHub OAuth
// (EXEMPLAR_OAUTH2_GITHUB_*; repo access is the person's GitHub
// permission) and OpenID Connect (EXEMPLAR_OIDC_*, see internal/oidc;
// repo access from EXEMPLAR_OIDC_GROUP_REPOS). At least one is required.
//
// /v1/evaluate also accepts Auth0 access tokens from services when
// EXEMPLAR_AUTH0_DOMAIN and EXEMPLAR_AUTH0_AUDIENCE are set; their repos
// come from the token's evaluate:<owner>/<repo> permissions.
//
// /v1/evaluate takes a person's token from `exemplar login` (a GitHub
// user token, or an OIDC ID token), checks the caller can read the repo,
// holds each caller to the daily budget, and enqueues an evaluate job for
// exemplar-worker; the caller polls for the result.
//
// A link opens a confirmation page after sign-in; confirming enqueues a
// feedback_link job, which exemplar-worker records with the person's role.
// It holds no installation credentials.
//
//	EXEMPLAR_DATABASE_URL                Postgres (findings, River)
//	EXEMPLAR_API_URL                     this server's public https base
//	EXEMPLAR_LINK_KEY_FILE               the link-signing key (also given to exemplar-worker)
//	EXEMPLAR_SESSION_KEY_FILE            the session and form key (this server only)
//	EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID     the GitHub OAuth client (the review app's)
//	EXEMPLAR_OAUTH2_GITHUB_CLIENT_SECRET_FILE  its secret
//	EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS  comma-separated orgs whose members may sign in
//	EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE  true lets any GitHub user sign in
//	EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL  GHES, e.g. https://github.example.com
//	EXEMPLAR_OAUTH2_GITHUB_SIGN_IN       false keeps GitHub for connecting to OIDC sign-ins only
//	EXEMPLAR_OIDC_GITHUB_LINK            required (default), optional or off: connect GitHub after an OIDC sign-in
//	EXEMPLAR_OIDC_*                      OpenID Connect (internal/oidc)
//	EXEMPLAR_GH_WEB_URL, EXEMPLAR_GH_API_URL  GHES links and API, without the enterprise base URL
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/api"
	"github.com/koblas/exemplar/internal/auth0"
	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/ghuser"
	"github.com/koblas/exemplar/internal/oidc"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/svcauth"
	"github.com/koblas/exemplar/internal/telemetry"
	"github.com/koblas/exemplar/internal/web"
)

var (
	errRequired           = errors.New("required")
	errAuth0Pair          = errors.New("EXEMPLAR_AUTH0_DOMAIN and EXEMPLAR_AUTH0_AUDIENCE are set together or not at all")
	errEnterpriseNotHTTPS = errors.New("EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL must be https")
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "exemplar-api:", err)
		os.Exit(1)
	}
}

func secretFile(env string) ([]byte, error) {
	p := os.Getenv(env)
	if p == "" {
		return nil, fmt.Errorf("%s is %w", env, errRequired)
	}
	b, err := os.ReadFile(p) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", env, err)
	}
	return bytes.TrimSpace(b), nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("exemplar-api", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	drain := fs.Duration("drain", 5*time.Second, "how long readiness reports false before stopping")
	dailyUSD := fs.Float64("evaluate-daily-usd", 5, "each caller's daily model spend on /v1/evaluate")
	dailyRuns := fs.Int("evaluate-daily-runs", 50, "each caller's daily /v1/evaluate runs")
	perMinute := fs.Int("evaluate-per-minute", 6, "each caller's new evaluations per minute")
	tryUSD := fs.Float64("playground-daily-usd", 5, "each viewer's daily model spend in the web UI playground")
	tryRuns := fs.Int("playground-daily-runs", 20, "each viewer's daily web UI playground runs")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if err := envcfg.Check(os.Getenv, "EXEMPLAR_API_URL", "EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID", "EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS",
		"EXEMPLAR_GH_WEB_URL", "EXEMPLAR_GH_API_URL", "EXEMPLAR_OIDC_ISSUER_URL"); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	dbURL, public := os.Getenv("EXEMPLAR_DATABASE_URL"), os.Getenv("EXEMPLAR_API_URL")
	if dbURL == "" || public == "" {
		return fmt.Errorf("EXEMPLAR_DATABASE_URL and EXEMPLAR_API_URL are %w", errRequired)
	}
	linkKey, err := secretFile("EXEMPLAR_LINK_KEY_FILE")
	if err != nil {
		return err
	}
	sessionKey, err := secretFile("EXEMPLAR_SESSION_KEY_FILE")
	if err != nil {
		return err
	}
	github, ghWeb, ghAPI, err := githubFromEnv()
	if err != nil {
		return err
	}
	links, err := feedbacklink.New(linkKey, public)
	if err != nil {
		return fmt.Errorf("feedback links: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-api")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate store: %w", err)
	}
	// An insert-only River client: no queues, no workers.
	client, err := river.NewClient[pgx.Tx](riverpgxv5.New(st.Pool), &river.Config{Logger: log})
	if err != nil {
		return err
	}
	var provider *oidc.Provider
	if cfg, on, err := oidc.FromEnv(os.Getenv, os.ReadFile); err != nil {
		return fmt.Errorf("load OIDC config: %w", err)
	} else if on {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		provider, err = oidc.New(dctx, cfg)
		cancel()
		if err != nil {
			return fmt.Errorf("OIDC provider: %w", err)
		}
	}
	users := ghuser.New(ghAPI, nil)
	identities := identityStore{st}
	link := os.Getenv("EXEMPLAR_OIDC_GITHUB_LINK")
	srv, err := api.New(api.Config{
		Links: links, SessionKey: sessionKey, PublicURL: public, GitHub: github, OIDC: provider, Users: users,
		Identities: identities, GitHubLink: link,
		GitHubWeb: ghWeb, GitHubAPI: ghAPI, Log: log,
		Lookup: func(ctx context.Context, id int64) (api.Finding, error) {
			f, err := st.FindingRef(ctx, id)
			if err != nil {
				return api.Finding{}, fmt.Errorf("find finding %d: %w", id, err)
			}
			return api.Finding{RuleID: f.RuleID, Repo: f.Repo, PR: int(f.PrNumber)}, nil
		},
		Enqueue: func(ctx context.Context, a store.FeedbackLinkArgs) error {
			if _, err := client.Insert(ctx, a, nil); err != nil {
				return fmt.Errorf("enqueue feedback: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("api server: %w", err)
	}

	// Service callers: Auth0 access tokens for this API.
	var sources svcauth.Multi
	switch domain, aud := os.Getenv("EXEMPLAR_AUTH0_DOMAIN"), os.Getenv("EXEMPLAR_AUTH0_AUDIENCE"); {
	case domain != "" && aud != "":
		v, err := auth0.New(domain, aud)
		if err != nil {
			return fmt.Errorf("auth0: %w", err)
		}
		sources = append(sources, v)
	case domain != "" || aud != "":
		return errAuth0Pair
	}
	// People's OIDC ID tokens (`exemplar login --oidc`), same issuer or not.
	if provider != nil {
		sources = append(sources, provider)
	}
	var services api.Services
	if len(sources) > 0 {
		services = sources
	}
	evals, err := api.NewEvaluations(api.EvalConfig{
		GitHubAPI: ghAPI, DailyUSD: *dailyUSD, DailyRuns: *dailyRuns, PerMinute: *perMinute, Log: log,
		Services: services, GitHub: githubTokens(github), Identities: identities, GitHubLink: linkMode(link),
		Create: func(ctx context.Context, r evaluate.Request, login, caller string) (string, error) {
			var id uuid.UUID
			err := st.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
				var err error
				p := queries.CreateEvaluationParams{
					Repo: r.Repo, BaseSha: r.BaseSHA, RequestedBy: login, Caller: caller, Agent: r.Agent,
					Mode: evaluate.ModeReview, Rules: []string{},
				}
				if r.Mode == evaluate.ModeEval {
					p.Mode, p.HeadSha, p.Rules = evaluate.ModeEval, &r.HeadSHA, append(p.Rules, r.Rules...)
				} else {
					p.Diff = &r.Diff
				}
				if id, err = q.CreateEvaluation(ctx, p); err != nil {
					return fmt.Errorf("create evaluation: %w", err)
				}
				if _, err = client.InsertTx(ctx, tx, store.EvaluateArgs{ID: id.String()}, nil); err != nil {
					return fmt.Errorf("enqueue evaluation: %w", err)
				}
				return nil
			})
			return id.String(), err
		},
		Get: func(ctx context.Context, id string) (evaluate.Status, string, error) {
			u, err := uuid.Parse(id)
			if err != nil {
				return evaluate.Status{}, "", api.ErrNotFound
			}
			row, err := st.GetEvaluation(ctx, u)
			if errors.Is(err, pgx.ErrNoRows) {
				return evaluate.Status{}, "", api.ErrNotFound
			}
			if err != nil {
				return evaluate.Status{}, "", fmt.Errorf("get evaluation %s: %w", id, err)
			}
			out := evaluate.Status{ID: id, Status: row.Status, Repo: row.Repo, BaseSHA: row.BaseSha, CostUSD: row.CostUsd}
			if row.Reason != nil {
				out.Reason = *row.Reason
			}
			if len(row.Result) > 0 {
				out.Result = &evaluate.Result{}
				if err := json.Unmarshal(row.Result, out.Result); err != nil {
					return evaluate.Status{}, "", fmt.Errorf("decode evaluation result: %w", err)
				}
			}
			return out, row.RequestedBy, nil
		},
		Spent: func(ctx context.Context, login string, since time.Time) (float64, int, error) {
			s, err := st.EvaluationSpend(ctx, queries.EvaluationSpendParams{RequestedBy: login, Since: pgtype.Timestamptz{Time: since, Valid: true}})
			return s.Spent, int(s.Runs), err
		},
	})
	if err != nil {
		return fmt.Errorf("evaluations api: %w", err)
	}

	formKey := hmac.New(sha256.New, sessionKey)
	formKey.Write([]byte("ui-forms"))
	ui, err := web.New(web.Config{
		Store: st, SignOut: srv.SignOut,
		Session: func(r *http.Request) (string, web.Access, bool) {
			login, access, ok := srv.UISession(r)
			return login, web.Access(access), ok
		},
		GitHubWeb: ghWeb, Log: log, FormKey: formKey.Sum(nil),
		// Owner actions are recorded and enqueued together; exemplar-worker
		// checks the actor and writes exemplar-config.
		Act: func(ctx context.Context, action, target, actor, reason string) (int64, error) {
			var id int64
			err := st.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
				var err error
				if id, err = q.CreateOwnerAction(ctx, queries.CreateOwnerActionParams{Action: action, Target: target, Actor: actor, Reason: reason}); err != nil {
					return fmt.Errorf("create owner action: %w", err)
				}
				if _, err = client.InsertTx(ctx, tx, store.OwnerActionArgs{ID: id}, nil); err != nil {
					return fmt.Errorf("enqueue owner action: %w", err)
				}
				return nil
			})
			if err != nil {
				return 0, fmt.Errorf("record owner action: %w", err)
			}
			return id, nil
		},
		PlaygroundDailyUSD: *tryUSD, PlaygroundDailyRuns: *tryRuns,
		Try: func(ctx context.Context, actor, repo string, pr int, ruleID, ruleYAML string) (int64, error) {
			var id int64
			err := st.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
				var err error
				if id, err = q.CreatePlaygroundRun(ctx, queries.CreatePlaygroundRunParams{Actor: actor, Repo: repo, PrNumber: int32(pr), RuleID: ruleID, RuleYaml: ruleYAML}); err != nil {
					return fmt.Errorf("create playground run: %w", err)
				}
				if _, err = client.InsertTx(ctx, tx, store.PlaygroundArgs{ID: id}, nil); err != nil {
					return fmt.Errorf("enqueue playground run: %w", err)
				}
				return nil
			})
			if err != nil {
				return 0, fmt.Errorf("record playground run: %w", err)
			}
			return id, nil
		},
		FalsePositive: func(ctx context.Context, id int64, actor, text string) error {
			if _, err := client.Insert(ctx, store.FeedbackLinkArgs{FindingID: id, Reason: "wrong", Actor: actor, At: time.Now().UTC(), Text: text}, nil); err != nil {
				return fmt.Errorf("enqueue false positive: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("web ui: %w", err)
	}

	var draining atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	mux.Handle("/v1/", evals.Handler())
	mux.Handle("/ui/", ui.Routes())
	mux.HandleFunc("GET /ui/connect-github", srv.Relink)
	mux.Handle("GET /ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := st.Pool.Ping(pctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	hs := &http.Server{
		Addr: *addr, Handler: http.TimeoutHandler(mux, 20*time.Second, "timeout"),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 25 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("exemplar-api serving", "addr", *addr)
		if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	draining.Store(true)
	time.Sleep(*drain)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	return nil
}

// githubFromEnv reads the EXEMPLAR_OAUTH2_GITHUB_* sign-in settings (nil
// when no client id is set) and the GitHub web and API bases.
func githubFromEnv() (*api.GitHubAuth, string, string, error) {
	web, apiURL := os.Getenv("EXEMPLAR_GH_WEB_URL"), os.Getenv("EXEMPLAR_GH_API_URL")
	if base := strings.TrimSuffix(os.Getenv("EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL"), "/"); base != "" {
		if !strings.HasPrefix(base, "https://") {
			return nil, "", "", errEnterpriseNotHTTPS
		}
		web, apiURL = base+"/", base+"/api/v3/"
	}
	id := os.Getenv("EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID")
	if id == "" {
		return nil, web, apiURL, nil
	}
	secret, err := secretFile("EXEMPLAR_OAUTH2_GITHUB_CLIENT_SECRET_FILE")
	if err != nil {
		return nil, "", "", err
	}
	gh := &api.GitHubAuth{
		ClientID: id, ClientSecret: string(secret), AllowEveryone: os.Getenv("EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE") == "true",
		SignInDisabled: os.Getenv("EXEMPLAR_OAUTH2_GITHUB_SIGN_IN") == "false",
	}
	for o := range strings.SplitSeq(os.Getenv("EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			gh.AllowedOrgs = append(gh.AllowedOrgs, o)
		}
	}
	return gh, web, apiURL, nil
}

// githubTokens is the GitHub sign-in /v1/evaluate accepts user tokens
// from: none when GitHub is only for connecting to OIDC sign-ins.
func githubTokens(g *api.GitHubAuth) *api.GitHubAuth {
	if g == nil || g.SignInDisabled {
		return nil
	}
	return g
}

// linkMode is EXEMPLAR_OIDC_GITHUB_LINK with its default (api.New
// validates it).
func linkMode(m string) string {
	if m == "" {
		return api.LinkRequired
	}
	return m
}

// identityStore keeps GitHub accounts connected to OIDC sign-ins.
type identityStore struct{ st *store.Store }

func (s identityStore) GitHubLink(ctx context.Context, issuer, subject string) (string, bool, error) {
	l, err := s.st.GetIdentityLink(ctx, queries.GetIdentityLinkParams{Issuer: issuer, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get identity link: %w", err)
	}
	return l.GithubLogin, true, nil
}

func (s identityStore) LinkGitHub(ctx context.Context, issuer, subject, email string, githubID int64, login string) error {
	err := s.st.InTx(ctx, func(_ pgx.Tx, q *queries.Queries) error {
		other, err := q.GitHubAccountLinkedTo(ctx, githubID)
		switch {
		case err == nil && (other.Issuer != issuer || other.Subject != subject):
			return api.ErrLinkedElsewhere
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("find GitHub account link: %w", err)
		}
		err = q.SaveIdentityLink(ctx, queries.SaveIdentityLinkParams{Issuer: issuer, Subject: subject, Email: email, GithubID: githubID, GithubLogin: login})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // a concurrent link of the same account
			return api.ErrLinkedElsewhere
		}
		if err != nil {
			return fmt.Errorf("save identity link: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("link GitHub account: %w", err)
	}
	return nil
}
