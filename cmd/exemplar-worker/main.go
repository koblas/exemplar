// Command exemplar-worker reviews live pull requests: serve is control
// (River, routing, the cheap queues and the periodic jobs) and dispatches
// each review to a runner; run-job is one runner.
//
//	exemplar-worker serve --config DIR --work-dir /var/lib/exemplar/work [--addr :8080] [--concurrency 4] [--launcher in-process|kubernetes]
//	exemplar-worker run-job --job ID --config-version V --work-dir /var/lib/exemplar/work
//	exemplar-worker enqueue --repo org/repo --pr 123 --head SHA --installation ID
//
// serve dispatches pr_review, evaluate and playground_run jobs through a
// launcher: in-process (the default, the quickstart) runs them in this
// process; kubernetes runs `run-job` as a Kubernetes Job per review from
// the exemplar-runner PodTemplate. Every config version serve loads is
// stored (config_snapshots), and a runner reviews with the version its
// job was dispatched with. serve mints each job's GitHub tokens into a
// single-use runner claim; run-job redeems it with EXEMPLAR_RUNNER_NONCE
// and never reads the App private key. run-job exits 0 when done, 10 when
// the job is cancelled, 11 when it was superseded, and anything else to
// be retried.
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
//	EXEMPLAR_GH_APP_ID                      the review app (serve only)
//	EXEMPLAR_GH_PRIVATE_KEY_FILE            its private key (serve only)
//	EXEMPLAR_RUNNER_NONCE                   run-job: redeems the job's runner claim
//	EXEMPLAR_CLAIM_KEY_FILE                 seals runner claims (optional: derived
//	                                        from EXEMPLAR_ROOT_KEY_FILE)
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

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/owneract"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
	"github.com/koblas/exemplar/internal/worker"
	"github.com/koblas/exemplar/internal/worker/kubelaunch"
)

var (
	// errRequired marks a missing flag or environment setting.
	errRequired       = errors.New("required")
	errBadConcurrency = errors.New("--concurrency must be at least 1")
	errBadLauncher    = errors.New("--launcher must be in-process or kubernetes")
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "run-job":
		err = runJob(os.Args[2:])
	case "enqueue":
		err = runEnqueue(os.Args[2:])
	default:
		usage()
	}
	if exit, ok := errors.AsType[exitError](err); ok {
		os.Exit(int(exit.code))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: exemplar-worker serve|run-job|enqueue [flags]")
	os.Exit(2)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout, kept current by a sidecar (required)")
	workDir := fs.String("work-dir", "", "scratch space for checkouts (required)")
	addr := fs.String("addr", ":8080", "health endpoint address")
	concurrency := fs.Int("concurrency", 4, "reviews run at once: in this process, or as runners in flight")
	launcherName := fs.String("launcher", launcherInProcess, "where reviews run: in-process, or kubernetes (a Job per review)")
	runnerTemplate := fs.String("runner-template", kubelaunch.RunnerName, "the PodTemplate runner Jobs are made from (--launcher=kubernetes)")
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
	if *launcherName != launcherInProcess && *launcherName != launcherKubernetes {
		return errBadLauncher
	}
	env, err := envFromOS(true)
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
	first, err := config.LoadDir(*configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	var snap atomic.Pointer[config.Snapshot]
	snap.Store(first)

	// Each running review holds two connections, for its per-repo queue
	// slot and its PR head's lock, besides what the review itself
	// (in-process), River, the event, linter-check and feedback workers and
	// the periodic jobs use.
	conns := 2*int32(*concurrency) + 14
	if *launcherName == launcherInProcess {
		conns += int32(*concurrency)
	}
	st, err := store.OpenWithConns(ctx, env.dbURL, conns)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate store: %w", err)
	}
	if err := worker.PutSnapshot(ctx, st, first); err != nil {
		return fmt.Errorf("store config: %w", err)
	}
	snapshot := func(context.Context) (*config.Snapshot, error) { return snap.Load(), nil }
	wcfg, err := env.workerConfig(st, *workDir, worker.AppClients{App: env.app}, log)
	if err != nil {
		return err
	}
	wcfg.Snapshot = snapshot
	app := env.app
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

	// Reviews, evaluations and playground runs go to runners, each with
	// only the tokens its claim grants.
	claims, err := env.claims(st)
	if err != nil {
		return err
	}
	runner := &worker.Runner{
		Config: wcfg, Snapshot: worker.Snapshots(st, snapshot), Claims: claims, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL"),
	}
	var launch worker.Launcher = worker.InProcess{Runner: runner}
	if *launcherName == launcherKubernetes {
		if launch, err = kubeLauncher(ctx, *runnerTemplate, log); err != nil {
			return err
		}
	} else if _, err := worker.New(wcfg); err != nil {
		return fmt.Errorf("create review worker: %w", err)
	}
	dcfg := worker.DispatchConfig{
		Launcher: launch, Snapshot: snapshot, Store: st, Log: log, Claims: claims,
		Granter: &worker.Granter{Minter: app, Store: st, ConfigRepo: os.Getenv("EXEMPLAR_CONFIG_REPO")},
		Abandon: worker.Abandoner{Store: st, GitHub: wcfg.GitHub, Log: log}.Abandon,
	}
	reviews, err := worker.NewReviewDispatcher(dcfg)
	if err != nil {
		return fmt.Errorf("create review dispatcher: %w", err)
	}
	evaluations, err := worker.NewEvaluateDispatcher(dcfg)
	if err != nil {
		return fmt.Errorf("create evaluate dispatcher: %w", err)
	}
	tries, err := worker.NewPlaygroundDispatcher(dcfg)
	if err != nil {
		return fmt.Errorf("create playground dispatcher: %w", err)
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
			bot, err := app.BotLogin(ctx)
			if err != nil {
				return nil, fmt.Errorf("app login: %w", err)
			}
			return gh.NewConfigRepo(c, cfgRepo, bot)
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
	river.AddWorker(workers, reviews)
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
	runner.Jobs = client

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
	wg.Go(func() { reloadConfig(workCtx, *configDir, *reload, st, &snap, log) })
	errc := make(chan error, 1)
	go func() {
		log.Info("exemplar-worker serving", "addr", *addr, "concurrency", *concurrency, "launcher", *launcherName, "config", first.Version)
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
// one. A new version is stored before it is used, so a runner can always
// load the version it is dispatched with.
func reloadConfig(ctx context.Context, dir string, every time.Duration, st *store.Store, snap *atomic.Pointer[config.Snapshot], log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s, err := config.LoadDir(dir)
		if err != nil {
			log.Error("config reload failed; keeping the previous config", "err", err)
			continue
		}
		if s.Version == snap.Load().Version {
			continue
		}
		if err := worker.PutSnapshot(ctx, st, s); err != nil {
			log.Error("storing the new config failed; keeping the previous config", "version", s.Version, "err", err)
			continue
		}
		log.Info("config loaded", "version", s.Version)
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
	if err := envcfg.Check(os.Getenv, "EXEMPLAR_GH_APP_ID", "EXEMPLAR_CONFIG_REPO", "EXEMPLAR_GH_API_URL", "EXEMPLAR_GH_WEB_URL", "EXEMPLAR_API_URL"); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
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
