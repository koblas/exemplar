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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/search/api"
	"github.com/koblas/exemplar/internal/search/index"
	"github.com/koblas/exemplar/internal/search/mirror"
	"github.com/koblas/exemplar/internal/telemetry"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	root := fs.String("root", "", "mirror directory on the node's local SSD (required)")
	indexRoot := fs.String("index-root", "", "zoekt index directory on the same SSD (required)")
	requireCTags := fs.Bool("require-ctags", false, "refuse to start without universal-ctags and fail builds whose symbol extraction fails")
	configDir := fs.String("config", "", "exemplar-config checkout: scope and eligibility (required)")
	gitURL := fs.String("git-url", "https://github.com/", "base clone URL (GHES: https://<host>/)")
	debounce := fs.Duration("debounce", mirror.DefaultDebounce, "minimum time between fetches of one repo")
	backstop := fs.Duration("backstop", mirror.DefaultBackstop, "fetch every repo in scope this often")
	reload := fs.Duration("config-reload", time.Minute, "how often to re-read --config")
	drain := fs.Duration("drain", 5*time.Second, "time between failing readiness and closing the listener")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *root == "" || *indexRoot == "" || *configDir == "" {
		return fmt.Errorf("--root, --index-root and --config are %w", errRequired)
	}
	token := os.Getenv("EXEMPLAR_SEARCH_TOKEN")
	if len(token) < 32 {
		return errSearchTokenTooShort
	}
	if !strings.HasSuffix(*gitURL, "/") {
		*gitURL += "/"
	}
	creds, err := credentialsFromEnv()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-search")
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

	m, err := mirror.New(mirror.Config{
		Root: *root, URL: func(repo string) string { return *gitURL + repo + ".git" }, Credentials: creds,
	})
	if err != nil {
		return fmt.Errorf("create mirror manager: %w", err)
	}
	scope := func() []string { return mirror.InScope(snap.Load()) }
	ctags := index.FindCTags()
	if ctags == "" && *requireCTags {
		return errCtagsMissing
	}
	if ctags == "" {
		log.Warn("universal-ctags not found; indexing without symbols")
	}
	ix, err := index.New(index.Config{Root: *indexRoot, Mirrors: m, CTags: ctags, RequireCTags: *requireCTags, Logger: log})
	if err != nil {
		return fmt.Errorf("create indexer: %w", err)
	}
	defer ix.Close()
	sched := mirror.NewScheduler(m, scope, *debounce, *backstop, log)
	// Index after every fetch, in the fetch's goroutine (one repo at a time).
	sched.AfterFetch = func(ctx context.Context, repo, sha string) {
		ix.NoteMirror(repo, sha)
		if _, err := ix.Build(ctx, repo); err != nil && ctx.Err() == nil {
			log.Error("index build failed; search stays on the previous generation", "repo", repo, "err", err)
		}
	}
	srv, err := api.NewServer(m, sched, snap.Load, token, log)
	if err == nil {
		srv.WithIndex(ix)
	}
	if err != nil {
		return fmt.Errorf("create search api server: %w", err)
	}
	hs := &http.Server{
		Addr: *addr, Handler: srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}

	workCtx, cancelWork := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sched.Run(workCtx) }()
	go func() { defer wg.Done(); reloadConfig(workCtx, *configDir, *reload, &snap, log) }()

	errc := make(chan error, 1)
	go func() {
		log.Info("exemplar-search serving", "addr", *addr, "repos", len(scope()))
		if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		cancelWork()
		wg.Wait()
		return err
	case <-ctx.Done():
	}
	// Graceful shutdown: fail readiness so the Service stops routing here,
	// let in-flight requests finish, then stop fetches (they see the
	// canceled context and leave no half-made mirror).
	log.Info("shutting down")
	srv.Drain()
	time.Sleep(*drain)
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	herr := hs.Shutdown(sctx)
	cancelWork()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-sctx.Done():
		return errShutdownDeadline
	}
	if herr != nil {
		return fmt.Errorf("shut down http server: %w", herr)
	}
	return nil
}

// reloadConfig re-reads exemplar-config so scope and eligibility changes
// (a newly denylisted repo above all) apply without a restart.
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
			log.Error("config reload failed; keeping the previous snapshot", "err", err)
			continue
		}
		snap.Store(s)
	}
}

// credentialsFromEnv binds mirror fetches to context-flavor tokens. With
// no app configured, fetches are anonymous and repos count as private:
// only for local use against public or local remotes.
func credentialsFromEnv() (mirror.Credentials, error) {
	appID, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_APP_ID"), 10, 64)
	installation, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_INSTALLATION_ID"), 10, 64)
	keyFile, cfgRepo := os.Getenv("EXEMPLAR_GH_PRIVATE_KEY_FILE"), os.Getenv("EXEMPLAR_CONFIG_REPO")
	if appID == 0 && installation == 0 && keyFile == "" {
		return nil, nil //nolint:nilnil // nil Credentials means anonymous fetches, as documented above
	}
	if appID == 0 || installation == 0 || keyFile == "" || cfgRepo == "" {
		return nil, fmt.Errorf("EXEMPLAR_GH_APP_ID, EXEMPLAR_GH_INSTALLATION_ID, EXEMPLAR_GH_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO %w", errAppPartial)
	}
	key, err := os.ReadFile(keyFile) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	app, err := gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: cfgRepo, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL")}, nil)
	if err != nil {
		return nil, fmt.Errorf("create github app: %w", err)
	}
	return func(ctx context.Context, repo string) (string, string, error) {
		return app.ContextFetch(ctx, installation, repo)
	}, nil
}
