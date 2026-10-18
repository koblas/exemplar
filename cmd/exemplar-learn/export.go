package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/envcfg"
	"github.com/koblas/exemplar/internal/exports"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runExport renders rule exports into each opted-in repo's coding-agent
// instruction files and opens (or updates) one PR per repo when they
// change. It writes with the separate exports app, never the
// review app; --dry-run renders and writes nothing.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	dryRun := fs.Bool("dry-run", false, "render and print; write nothing")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *configDir == "" {
		return fmt.Errorf("--config is %w", errRequired)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-learn")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	snap, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	if !*dryRun {
		if skip, err := exportsOff(ctx, log, snap); skip || err != nil {
			return err
		}
	}
	dbURL := os.Getenv("EXEMPLAR_DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("EXEMPLAR_DATABASE_URL is %w", errRequired)
	}
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate store: %w", err)
	}
	r := exports.Runner{Store: st, Log: log}
	if !*dryRun {
		app, err := exportsAppFromEnv()
		if err != nil {
			return fmt.Errorf("%w (or use --dry-run)", err)
		}
		r.Writer = func(ctx context.Context, repo string) (exports.Writer, error) {
			inst, err := app.RepoInstallation(ctx, repo)
			if err != nil {
				return nil, fmt.Errorf("look up repo installation: %w", err)
			}
			c, err := app.Client(ctx, inst, gh.FlavorExport, []string{repo})
			if err != nil {
				return nil, fmt.Errorf("create github client: %w", err)
			}
			return gh.NewRepoWriter(c, repo)
		}
	}
	outs, err := r.Run(ctx, snap)
	for _, o := range outs {
		printJSON(o)
	}
	if err != nil {
		return fmt.Errorf("run export: %w", err)
	}
	return nil
}

// exportsOff is skipExport for the environment and snapshot, logging a skip.
func exportsOff(ctx context.Context, log *slog.Logger, snap *config.Snapshot) (bool, error) {
	skip, err := skipExport(os.Getenv("EXEMPLAR_EXPORTS_APP_ID"), exports.Repos(snap))
	if skip {
		log.InfoContext(ctx, "no exports app and no repo configures exports.targets; nothing to export")
	}
	return skip, err
}

// skipExport reports whether export has nothing to do: the exports app is
// optional, so with no app id (unset or 0) and no repo configuring
// exports.targets the job succeeds without the app's key. Repos that
// configure exports with no app are an error.
func skipExport(appID string, repos []string) (bool, error) {
	if appID != "" && appID != "0" {
		return false, nil
	}
	if len(repos) > 0 {
		return false, fmt.Errorf("%d repos configure exports.targets (%s) but EXEMPLAR_EXPORTS_APP_ID is %w", len(repos), strings.Join(repos, ", "), errRequired)
	}
	return true, nil
}

// exportsAppFromEnv builds the exports app (exemplar-exports) from
// EXEMPLAR_EXPORTS_*; EXEMPLAR_CONFIG_REPO is needed so its tokens can
// refuse to reach exemplar-config.
func exportsAppFromEnv() (*gh.App, error) {
	if err := envcfg.Check(os.Getenv, "EXEMPLAR_EXPORTS_APP_ID", "EXEMPLAR_CONFIG_REPO", "EXEMPLAR_GH_API_URL"); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	appID, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_EXPORTS_APP_ID"), 10, 64)
	keyFile, cfgRepo := os.Getenv("EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE"), os.Getenv("EXEMPLAR_CONFIG_REPO")
	if appID == 0 || keyFile == "" || cfgRepo == "" {
		return nil, fmt.Errorf("EXEMPLAR_EXPORTS_APP_ID, EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO are %w", errRequired)
	}
	key, err := os.ReadFile(keyFile) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
	if err != nil {
		return nil, fmt.Errorf("read exports private key: %w", err)
	}
	app, err := gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: cfgRepo, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL")}, nil)
	if err != nil {
		return nil, fmt.Errorf("create exports github app: %w", err)
	}
	return app, nil
}
