package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/ghuser"
	"github.com/koblas/exemplar/internal/svcauth"
)

// /v1/evaluate (spec §10.1, PRD FR-S4, FR-S7) is `exemplar review`'s API:
//
//	POST /v1/evaluate       a working-tree diff against a pushed base; 202 with its id
//	GET  /v1/evaluate/{id}  its status and, when done, what would have been posted
//
// Callers send either a GitHub user token (the CLI signs in with the
// review app's device flow; org SSO sits in front at the ingress) or,
// for services, a JWT from a configured source (spec §13 IN-5): an Auth0
// access token for this API ("svc:<sub>"), or a GitHub Actions OIDC token
// ("gha:<owner>/<repo>"). A user must be able to read the repo; a service
// needs an `evaluate:<owner>/<repo>` (or `<owner>/*`, `*`) permission
// (an Actions token has one, for its own repo), and is recorded as
// caller=agent. Each
// caller is rate-limited and held to a daily budget, and only the
// requester sees a result. Tokens are never stored.

// Services verifies service access tokens (svcauth.Multi).
type Services interface {
	Verify(ctx context.Context, token string) (svcauth.Claims, error)
}

// ErrNotFound is an evaluation that does not exist.
var ErrNotFound = errors.New("api: not found")

// EvalConfig wires the evaluate endpoints.
type EvalConfig struct {
	// GitHubAPI is https://api.github.com/ (GHES: https://<host>/api/v3/).
	GitHubAPI string
	// Create stores the request for the requester and enqueues its job, in
	// one transaction, returning the evaluation's id. caller is cli for a
	// person, agent for a service.
	Create func(ctx context.Context, r evaluate.Request, requester, caller string) (string, error)
	// Services verifies JWTs: Auth0 service tokens and people's OIDC ID
	// tokens (svcauth.Multi). Nil accepts no JWT.
	Services Services
	// GitHub, when set, accepts GitHub user tokens from the people its
	// settings allow (allowed orgs); nil refuses them (OIDC only).
	GitHub *GitHubAuth
	// Identities and GitHubLink map an OIDC person to the GitHub account
	// they connected (signin.go): they are that login here too, and with
	// GitHubLink required they must have connected one.
	Identities Identities
	GitHubLink string
	// Get returns an evaluation and who requested it (ErrNotFound).
	Get func(ctx context.Context, id string) (evaluate.Status, string, error)
	// Spent is what login's evaluations since cost, and how many ran.
	Spent func(ctx context.Context, login string, since time.Time) (float64, int, error)
	// DailyUSD and DailyRuns bound one caller's evaluations per UTC day.
	DailyUSD  float64
	DailyRuns int
	// PerMinute bounds one caller's new evaluations; polls get ten times
	// as many.
	PerMinute int
	HTTP      *http.Client
	Log       *slog.Logger
	Now       func() time.Time
}

// Evaluations serves /v1/evaluate.
type Evaluations struct {
	cfg EvalConfig

	users    *ghuser.Checker
	mu       sync.Mutex
	limiters map[string]*rate.Limiter // "post " or "poll " + login
}

// NewEvaluations validates cfg.
func NewEvaluations(cfg EvalConfig) (*Evaluations, error) {
	switch {
	case cfg.Create == nil || cfg.Get == nil || cfg.Spent == nil:
		return nil, errors.New("api: Create, Get and Spent are required")
	case cfg.DailyUSD <= 0 || cfg.DailyRuns <= 0 || cfg.PerMinute <= 0:
		return nil, errors.New("api: the daily budget, daily runs and per-minute limit must be positive")
	}
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = "https://api.github.com/"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	users := ghuser.New(cfg.GitHubAPI, cfg.HTTP)
	users.Now = cfg.Now
	return &Evaluations{cfg: cfg, users: users, limiters: map[string]*rate.Limiter{}}, nil
}

// Handler returns the routes.
func (e *Evaluations) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/evaluate", e.create)
	mux.HandleFunc("GET /v1/evaluate/{id}", e.get)
	return mux
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	// The status is written; a failed write to the client has nowhere to go.
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errchkjson // see above
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	// The status is written; a failed write to the client has nowhere to go.
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // see above
}

// identity is a request's caller.
type identity struct {
	token   string
	name    string // a GitHub login, or "svc:<subject>"
	service *svcauth.Claims
}

// caller identifies the request's caller and applies the rate limit. It
// writes the error response itself when it returns false.
func (e *Evaluations) caller(w http.ResponseWriter, r *http.Request, kind string) (identity, bool) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		jsonError(w, http.StatusUnauthorized, "sign in with `exemplar login`")
		return identity{}, false
	}
	id := identity{token: token}
	if e.cfg.Services != nil && svcauth.LooksLikeJWT(token) {
		c, err := e.cfg.Services.Verify(r.Context(), token)
		if err != nil {
			e.cfg.Log.Info("evaluate: service token refused", "err", err)
			jsonError(w, http.StatusUnauthorized, "the service token was refused")
			return identity{}, false
		}
		id.name, id.service = c.Subject, &c
		if c.User && e.cfg.Identities != nil && e.cfg.GitHubLink != "" && e.cfg.GitHubLink != LinkOff {
			login, ok, err := e.cfg.Identities.GitHubLink(r.Context(), c.Issuer, c.Sub)
			switch {
			case err != nil:
				jsonError(w, http.StatusInternalServerError, "could not read your connected GitHub account")
				return identity{}, false
			case ok:
				id.name = login
			case e.cfg.GitHubLink == LinkRequired:
				jsonError(w, http.StatusForbidden, "connect your GitHub account first: sign in to the web UI once")
				return identity{}, false
			}
		}
	} else {
		if e.cfg.GitHub == nil {
			jsonError(w, http.StatusUnauthorized, "GitHub sign-in is off here; sign in with `exemplar login --oidc`")
			return identity{}, false
		}
		login, err := e.users.Login(r.Context(), token)
		if err != nil {
			e.cfg.Log.Info("evaluate: token refused", "err", err)
			jsonError(w, http.StatusUnauthorized, "the GitHub token was refused; sign in again with `exemplar login`")
			return identity{}, false
		}
		if !e.cfg.GitHub.AllowEveryone {
			if in, err := e.users.InOrgs(r.Context(), token, login, e.cfg.GitHub.AllowedOrgs); err != nil {
				jsonError(w, http.StatusBadGateway, "could not check your organization membership")
				return identity{}, false
			} else if !in {
				jsonError(w, http.StatusForbidden, "your GitHub account is not in an organization allowed to use Exemplar")
				return identity{}, false
			}
		}
		id.name = login
	}
	if !e.limiter(kind, id.name).Allow() {
		jsonError(w, http.StatusTooManyRequests, "too many requests; slow down")
		return identity{}, false
	}
	return id, true
}

// canEvaluate applies FR-S7: users must read the repo, services need the
// permission.
func (e *Evaluations) canEvaluate(ctx context.Context, id identity, repo string) (bool, error) {
	if id.service != nil {
		return id.service.CanEvaluate(repo), nil
	}
	return e.users.CanRead(ctx, id.token, repo)
}

func (e *Evaluations) create(w http.ResponseWriter, r *http.Request) {
	id, ok := e.caller(w, r, "post")
	if !ok {
		return
	}
	login := id.name
	var req evaluate.Request
	body := http.MaxBytesReader(w, r.Body, evaluate.MaxDiff+64<<10)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			jsonError(w, http.StatusRequestEntityTooLarge, "the diff is over 1 MiB")
			return
		}
		jsonError(w, http.StatusBadRequest, "the request is not valid JSON")
		return
	}
	if err := req.Validate(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Results are only for repos the caller can read (FR-S7).
	if ok, err := e.canEvaluate(r.Context(), id, req.Repo); err != nil {
		e.cfg.Log.Warn("evaluate: repo access not checked", "repo", req.Repo, "err", err)
		jsonError(w, http.StatusBadGateway, "could not check your access to the repo")
		return
	} else if !ok {
		jsonError(w, http.StatusNotFound, "no such repo, or you cannot read it")
		return
	}
	day := e.cfg.Now().UTC().Truncate(24 * time.Hour)
	spent, runs, err := e.cfg.Spent(r.Context(), login, day)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not read your budget")
		return
	}
	if spent >= e.cfg.DailyUSD || runs >= e.cfg.DailyRuns {
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("your daily budget is used (%d runs, $%.2f); it resets at 00:00 UTC", runs, spent))
		return
	}
	caller := "cli"
	if id.service != nil && !id.service.User {
		caller = "agent"
	}
	evalID, err := e.cfg.Create(r.Context(), req, login, caller)
	if err != nil {
		e.cfg.Log.Error("evaluate: not stored", "repo", req.Repo, "err", err)
		jsonError(w, http.StatusInternalServerError, "could not queue the review")
		return
	}
	e.cfg.Log.Info("evaluate: queued", "id", evalID, "repo", req.Repo, "caller", login, "kind", caller, "agent", req.Agent)
	writeJSON(w, http.StatusAccepted, evaluate.Status{ID: evalID, Status: evaluate.StatusQueued, Repo: req.Repo, BaseSHA: req.BaseSHA})
}

func (e *Evaluations) get(w http.ResponseWriter, r *http.Request) {
	id, ok := e.caller(w, r, "poll")
	if !ok {
		return
	}
	login := id.name
	st, by, err := e.cfg.Get(r.Context(), r.PathValue("id"))
	// Someone else's evaluation is as absent as a missing one.
	if errors.Is(err, ErrNotFound) || (err == nil && !strings.EqualFold(by, login)) {
		jsonError(w, http.StatusNotFound, "no such evaluation")
		return
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not read the evaluation")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (e *Evaluations) limiter(kind, login string) *rate.Limiter {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := kind + " " + strings.ToLower(login)
	l, ok := e.limiters[key]
	if !ok {
		n := e.cfg.PerMinute
		if kind == "poll" {
			n *= 10
		}
		l = rate.NewLimiter(rate.Limit(float64(n)/60), n)
		e.limiters[key] = l
	}
	return l
}
