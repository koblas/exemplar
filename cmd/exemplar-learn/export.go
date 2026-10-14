package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/exports"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runExport renders rule exports into each opted-in repo's coding-agent
// instruction files and opens (or updates) one PR per repo when they
// change (spec §10.2). It writes with the separate exports app, never the
// review app; --dry-run renders and writes nothing.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	dryRun := fs.Bool("dry-run", false, "render and print; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configDir == "" {
		return errors.New("--config is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-learn")
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	snap, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return err
	}
	dbURL := os.Getenv("EXEMPLAR_DATABASE_URL")
	if dbURL == "" {
		return errors.New("EXEMPLAR_DATABASE_URL is required")
	}
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
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
				return nil, err
			}
			c, err := app.Client(ctx, inst, gh.FlavorExport, []string{repo})
			if err != nil {
				return nil, err
			}
			return gh.NewRepoWriter(c, repo)
		}
	}
	outs, err := r.Run(ctx, snap)
	for _, o := range outs {
		line, _ := json.Marshal(o)
		fmt.Println(string(line))
	}
	return err
}

// exportsAppFromEnv builds the exports app (spec §5 exemplar-exports) from
// EXEMPLAR_EXPORTS_*; EXEMPLAR_CONFIG_REPO is needed so its tokens can
// refuse to reach exemplar-config.
func exportsAppFromEnv() (*gh.App, error) {
	appID, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_EXPORTS_APP_ID"), 10, 64)
	keyFile, cfgRepo := os.Getenv("EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE"), os.Getenv("EXEMPLAR_CONFIG_REPO")
	if appID == 0 || keyFile == "" || cfgRepo == "" {
		return nil, errors.New("EXEMPLAR_EXPORTS_APP_ID, EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO are required")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read exports private key: %w", err)
	}
	return gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: cfgRepo, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL")}, nil)
}
