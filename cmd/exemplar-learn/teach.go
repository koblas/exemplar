package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/teach"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runTeach is the nightly teach-the-bot fast path:
// recurring reasons become draft scope PRs or owner review issues.
// --dry-run prints the proposals and writes nothing.
func runTeach(args []string) error {
	fs := flag.NewFlagSet("teach", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	dryRun := fs.Bool("dry-run", false, "print decisions; write nothing")
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
	r := teach.Runner{Store: st, Log: log, GitHubURL: os.Getenv("EXEMPLAR_GH_WEB_URL")}
	if !*dryRun {
		if r.Repo, err = configRepo(ctx); err != nil {
			return err
		}
	}
	outs, err := r.Run(ctx, snap)
	for _, o := range outs {
		printJSON(o)
	}
	if err != nil {
		return fmt.Errorf("run teach: %w", err)
	}
	return nil
}
