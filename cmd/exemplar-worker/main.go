// Command exemplar-worker reviews live pull requests (the orchestrator,
// worker and poster, run as one process for now).
//
//	exemplar-worker serve --config DIR --work-dir /var/lib/exemplar/work [--addr :8080] [--concurrency 4]
//	exemplar-worker enqueue --repo org/repo --pr 123 --head SHA --installation ID
//
// serve also runs periodic jobs on River's leader: every 5 minutes it
// requests redelivery of failed webhook deliveries, every 10 it sweeps
// opted-in repos for open PR heads that were never reviewed, and daily it
// applies platform/retention.yaml.
//
// serve routes gh_event jobs (queue "events", inserted by exemplar-webhook)
// into pr_review jobs, and consumes those from the queue "reviews": for
// each it reads the PR, checks it out, runs the review through the
// gateway, stores it, posts the review and check run, and approves only
// when every auto-approve gate passed. enqueue inserts one review by hand.
//
// Settings come from the environment, never flags, for anything secret:
//
//	EXEMPLAR_DATABASE_URL                   Postgres (reviews, River)
//	EXEMPLAR_GH_APP_ID                      the review app
//	EXEMPLAR_GH_PRIVATE_KEY_FILE            its private key
//	EXEMPLAR_CONFIG_REPO                    exemplar-config, which worker tokens may not read
//	EXEMPLAR_GH_API_URL                     GHES API base (optional)
//	EXEMPLAR_GH_WEB_URL                     GHES web base for clones (optional)
//	EXEMPLAR_GATEWAY_URL, EXEMPLAR_GATEWAY_TOKEN
//	EXEMPLAR_SEARCH_URL, EXEMPLAR_SEARCH_TOKEN  exemplar-search (optional: no
//	                                        find_references or path_history without it)
//	EXEMPLAR_AST_GREP                       ast-grep binary (optional)
//	EXEMPLAR_API_URL, EXEMPLAR_LINK_KEY_FILE  exemplar-api and its link key (optional:
//	                                        comments carry signed feedback links)
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/owneract"
	"github.com/koblas/exemplar/internal/pathhistory"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/search/api"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
	"github.com/koblas/exemplar/internal/worker"
)

var (
	// errRequired marks a missing flag or environment setting.
	errRequired       = errors.New("required")
	errBadConcurrency = errors.New("--concurrency must be at least 1")
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "enqueue":
		err = runEnqueue(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: exemplar-worker serve|enqueue [flags]")
	os.Exit(2)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout, kept current by a sidecar (required)")
	workDir := fs.String("work-dir", "", "scratch space for checkouts (required)")
	addr := fs.String("addr", ":8080", "health endpoint address")
	concurrency := fs.Int("concurrency", 4, "reviews run at once by this process")
	platformTeam := fs.String("platform-team", "@platform/devex", "team that may take every web UI owner action")
	reload := fs.Duration("reload", time.Minute, "how often to re-read --config")
	drain := fs.Duration("drain", 5*time.Second, "how long readiness reports false before stopping")
	stopAfter := fs.Duration("stop-timeout", 60*time.Second, "how long running reviews may finish on shutdown before they are cancelled")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *configDir == "" || *workDir == "" {
		return fmt.Errorf("--config and --work-dir are %w", errRequired)
	}
	if *concurrency < 1 {
		return errBadConcurrency
	}
	dbURL := os.Getenv("EXEMPLAR_DATABASE_URL")
	gwURL, gwToken := os.Getenv("EXEMPLAR_GATEWAY_URL"), os.Getenv("EXEMPLAR_GATEWAY_TOKEN")
	if dbURL == "" || gwURL == "" || gwToken == "" {
		return fmt.Errorf("EXEMPLAR_DATABASE_URL, EXEMPLAR_GATEWAY_URL and EXEMPLAR_GATEWAY_TOKEN are %w", errRequired)
	}
	app, err := appFromEnv()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*workDir, 0o750); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-worker")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	// Fail at startup on an invalid config; later reloads keep the last good one.
	first, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	var snap atomic.Pointer[config.Snapshot]
	snap.Store(first)

	// Each running review holds a connection for its per-repo queue slot,
	// besides what the review itself, River, the event, linter-check and
	// feedback workers and the periodic jobs use.
	st, err := store.OpenWithConns(ctx, dbURL, int32(*concurrency)*2+14)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate store: %w", err)
	}
	sc, err := redact.New()
	if err != nil {
		return fmt.Errorf("create redaction scanner: %w", err)
	}
	eng := engine.New(engine.NewHarness(engine.HarnessConfig{GatewayURL: gwURL, GatewayToken: gwToken}), sc,
		compiled.Runner{ASTGrep: os.Getenv("EXEMPLAR_AST_GREP")})
	if url, tok := os.Getenv("EXEMPLAR_SEARCH_URL"), os.Getenv("EXEMPLAR_SEARCH_TOKEN"); url != "" {
		search, err := api.NewClient(url, tok, nil)
		if err != nil {
			return fmt.Errorf("create search client: %w", err)
		}
		hist, err := pathhistory.New(st, pathhistory.Remote(search))
		if err != nil {
			return fmt.Errorf("create path history: %w", err)
		}
		eng.WithReferences(search).WithPathHistory(hist)
	} else {
		log.Warn("EXEMPLAR_SEARCH_URL not set: agents get no find_references or path_history, and bug.contract is skipped")
	}

	wcfg := worker.Config{
		GitHub: worker.AppClients{App: app}, Store: st,
		Snapshot: func(context.Context) (*config.Snapshot, error) { return snap.Load(), nil },
		Engine:   eng, Scanner: sc, WorkDir: *workDir, Log: log,
		// Mode eval (config CI) evaluates exemplar-config's changed rules.
		CloneURL:   gh.CloneURL(os.Getenv("EXEMPLAR_GH_WEB_URL")),
		ConfigRepo: os.Getenv("EXEMPLAR_CONFIG_REPO"), Matcher: compiled.Runner{ASTGrep: os.Getenv("EXEMPLAR_AST_GREP")},
	}
	// Feedback links need exemplar-api's URL and the key it
	// verifies with; without them comments name the slash commands only.
	if apiURL, keyFile := os.Getenv("EXEMPLAR_API_URL"), os.Getenv("EXEMPLAR_LINK_KEY_FILE"); apiURL != "" || keyFile != "" {
		key, err := os.ReadFile(keyFile) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
		if err != nil {
			return fmt.Errorf("EXEMPLAR_API_URL and EXEMPLAR_LINK_KEY_FILE go together: %w", err)
		}
		if wcfg.Links, err = feedbacklink.New(bytes.TrimSpace(key), apiURL); err != nil {
			return fmt.Errorf("create feedback link signer: %w", err)
		}
	}
	w, err := worker.New(wcfg)
	if err != nil {
		return fmt.Errorf("create review worker: %w", err)
	}
	linterChecks, err := worker.NewLinterCheckWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create linter check worker: %w", err)
	}
	commands, err := worker.NewFeedbackCommandWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create feedback command worker: %w", err)
	}
	dismissals, err := worker.NewAlertDismissalWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create alert dismissal worker: %w", err)
	}
	signals, err := worker.NewFeedbackSignalWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create feedback signal worker: %w", err)
	}
	mergePolicy, err := worker.NewMergePolicyWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create merge policy worker: %w", err)
	}
	evaluations, err := worker.NewEvaluateWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create evaluate worker: %w", err)
	}
	// The web UI's playground: the deployed config with the
	// draft rule file added.
	tries, err := worker.NewPlaygroundWorker(wcfg, func(ruleYAML []byte) (*config.Snapshot, string, error) {
		return playground.RuleFileSnapshot(os.DirFS(*configDir), ruleYAML, snap.Load().Version)
	})
	if err != nil {
		return fmt.Errorf("create playground worker: %w", err)
	}
	// Owner actions from the web UI write exemplar-config with the
	// config-writer token and check teams with the gate token, both
	// scoped to the config repo.
	cfgRepo := os.Getenv("EXEMPLAR_CONFIG_REPO")
	cfgInstallation := func(ctx context.Context) (int64, error) { return app.RepoInstallation(ctx, cfgRepo) }
	ownerActions, err := worker.NewOwnerActionWorker(owneract.Runner{
		Store: st, PlatformTeam: *platformTeam,
		Repo: func(ctx context.Context) (owneract.Repo, error) {
			inst, err := cfgInstallation(ctx)
			if err != nil {
				return nil, err
			}
			c, err := app.Client(ctx, inst, gh.FlavorConfigWriter, []string{cfgRepo})
			if err != nil {
				return nil, fmt.Errorf("create github client: %w", err)
			}
			return gh.NewConfigRepo(c, cfgRepo)
		},
		Member: func(ctx context.Context, team, login string) (bool, error) {
			inst, err := cfgInstallation(ctx)
			if err != nil {
				return false, err
			}
			c, err := app.Client(ctx, inst, gh.FlavorGate, []string{cfgRepo})
			if err != nil {
				return false, fmt.Errorf("create github client: %w", err)
			}
			return gh.TeamMember(ctx, c, team, login)
		},
	}, func(context.Context) (*config.Snapshot, error) { return snap.Load(), nil }, log)
	if err != nil {
		return fmt.Errorf("create owner action worker: %w", err)
	}
	reactions, err := worker.NewReactionsWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create reactions worker: %w", err)
	}
	linkClicks, err := worker.NewFeedbackLinkWorker(wcfg)
	if err != nil {
		return fmt.Errorf("create feedback link worker: %w", err)
	}
	stats, err := worker.NewStatsWorker(st, log)
	if err != nil {
		return fmt.Errorf("create stats worker: %w", err)
	}
	events, err := worker.NewEventWorker(st, log)
	if err != nil {
		return fmt.Errorf("create event worker: %w", err)
	}
	snapshot := func(context.Context) (*config.Snapshot, error) { return snap.Load(), nil }
	reconcile, err := worker.NewReconcileWorker(st, app, log)
	if err != nil {
		return fmt.Errorf("create reconcile worker: %w", err)
	}
	sweep, err := worker.NewSweepWorker(st, app, snapshot, log)
	if err != nil {
		return fmt.Errorf("create sweep worker: %w", err)
	}
	retain, err := worker.NewRetentionWorker(st, snapshot, log)
	if err != nil {
		return fmt.Errorf("create retention worker: %w", err)
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	river.AddWorker(workers, events)
	river.AddWorker(workers, reconcile)
	river.AddWorker(workers, sweep)
	river.AddWorker(workers, retain)
	river.AddWorker(workers, linterChecks)
	river.AddWorker(workers, commands)
	river.AddWorker(workers, stats)
	river.AddWorker(workers, dismissals)
	river.AddWorker(workers, signals)
	river.AddWorker(workers, mergePolicy)
	river.AddWorker(workers, evaluations)
	river.AddWorker(workers, tries)
	river.AddWorker(workers, ownerActions)
	river.AddWorker(workers, reactions)
	river.AddWorker(workers, linkClicks)
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			store.ReviewQueue:       {MaxWorkers: *concurrency},
			store.EventQueue:        {MaxWorkers: 4}, // routing only: cheap, one transaction each
			store.LinterQueue:       {MaxWorkers: 4}, // late-CI linter dedupe: a few GitHub calls and judge calls each
			store.EvaluateQueue:     {MaxWorkers: 2}, // `exemplar review`: full reviews, apart from PRs
			store.FeedbackQueue:     {MaxWorkers: 2}, // commands, dismissals, signals and merge policy: a few GitHub or light-model calls each
			worker.MaintenanceQueue: {MaxWorkers: 1},
		},
		// The reconciler and sweeper; River runs them on its leader only.
		PeriodicJobs: worker.PeriodicJobs(),
		Workers:      workers, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}

	var draining atomic.Bool
	mux := http.NewServeMux()
	// Liveness never depends on Postgres or GitHub.
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
		Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}

	// Jobs run on their own context so a signal lets them finish (Stop)
	// before they are cancelled (StopAndCancel).
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start river client: %w", err)
	}
	workCtx, cancelWork := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { ; reloadConfig(workCtx, *configDir, *reload, &snap, log) })
	errc := make(chan error, 1)
	go func() {
		log.Info("exemplar-worker serving", "addr", *addr, "concurrency", *concurrency)
		if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		cancelWork()
		wg.Wait()
		_ = client.StopAndCancel(context.Background())
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	draining.Store(true)
	time.Sleep(*drain)
	sctx, cancel := context.WithTimeout(context.Background(), *stopAfter)
	defer cancel()
	if err := client.Stop(sctx); err != nil {
		log.Warn("reviews did not finish in time; cancelling them (River retries them)", "err", err)
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = client.StopAndCancel(cctx)
		ccancel()
	}
	cancelWork()
	wg.Wait()
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := hs.Shutdown(hctx); err != nil {
		return fmt.Errorf("shut down http server: %w", err)
	}
	return nil
}

// reloadConfig re-reads exemplar-config so kill switches and eligibility
// changes apply without a restart; an invalid config keeps the last good
// one.
func reloadConfig(ctx context.Context, dir string, every time.Duration, snap *atomic.Pointer[config.Snapshot], log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s, err := config.LoadSnapshot(os.DirFS(dir), dir)
		if err != nil {
			log.Error("config reload failed; keeping the previous config", "err", err)
			continue
		}
		snap.Store(s)
	}
}

func runEnqueue(args []string) error {
	fs := flag.NewFlagSet("enqueue", flag.ContinueOnError)
	var a store.PRReviewArgs
	fs.StringVar(&a.Repo, "repo", "", "org/repo (required)")
	fs.IntVar(&a.PR, "pr", 0, "pull request number (required)")
	fs.StringVar(&a.HeadSHA, "head", "", "head commit to review (required)")
	fs.Int64Var(&a.InstallationID, "installation", 0, "the app's installation id on the org (required)")
	fs.IntVar(&a.Attempt, "attempt", 0, "re-run attempt for an incomplete review")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if a.Repo == "" || a.PR < 1 || a.HeadSHA == "" || a.InstallationID == 0 {
		return fmt.Errorf("--repo, --pr, --head and --installation are %w", errRequired)
	}
	dbURL := os.Getenv("EXEMPLAR_DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("EXEMPLAR_DATABASE_URL is %w", errRequired)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	client, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}
	res, err := client.Insert(ctx, a, nil)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	if res.UniqueSkippedAsDuplicate {
		fmt.Printf("already queued: job %d\n", res.Job.ID)
		return nil
	}
	fmt.Printf("queued job %d\n", res.Job.ID)
	return nil
}

// appFromEnv builds the review app from EXEMPLAR_GH_* settings.
func appFromEnv() (*gh.App, error) {
	appID, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_APP_ID"), 10, 64)
	keyFile, cfgRepo := os.Getenv("EXEMPLAR_GH_PRIVATE_KEY_FILE"), os.Getenv("EXEMPLAR_CONFIG_REPO")
	if appID == 0 || keyFile == "" || cfgRepo == "" {
		return nil, fmt.Errorf("EXEMPLAR_GH_APP_ID, EXEMPLAR_GH_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO are %w", errRequired)
	}
	key, err := os.ReadFile(keyFile) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	app, err := gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: cfgRepo, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL")}, nil)
	if err != nil {
		return nil, fmt.Errorf("create github app: %w", err)
	}
	return app, nil
}
