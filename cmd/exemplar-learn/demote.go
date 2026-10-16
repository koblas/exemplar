package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/demote"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runDemote is nightly auto-demotion: rules whose recent
// feedback crosses both bars are moved to shadow by a config PR the bot
// opens and merges. --dry-run prints the decisions and writes nothing.
func runDemote(args []string) error {
	fs := flag.NewFlagSet("demote", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	dryRun := fs.Bool("dry-run", false, "print decisions; write nothing")
	all := fs.Bool("all", false, "print every rule's decision, not only demotions")
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
	r := demote.Runner{Stats: st, Log: log}
	if f := snap.Global.Feedback; f != nil {
		r.ResponseDays = f.OwnerResponseDays
	}
	if !*dryRun {
		if r.Repo, err = configRepo(ctx); err != nil {
			return err
		}
	}
	decisions, outs, err := r.Run(ctx, snap)
	if *all {
		for _, d := range decisions {
			printJSON(d)
		}
	}
	for _, o := range outs {
		printJSON(o)
	}
	if err != nil {
		return fmt.Errorf("run demote: %w", err)
	}
	return nil
}
