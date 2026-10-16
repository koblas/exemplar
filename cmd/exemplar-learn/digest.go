package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/digest"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runDigest opens the weekly rule-health digest for each rule owner in
// exemplar-config (FR-B5, PRD §17.8). --dry-run prints the digests and
// writes nothing.
func runDigest(args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	dryRun := fs.Bool("dry-run", false, "print decisions; write nothing")
	platform := fs.String("platform-team", "@platform/devex", "team that gets the export-drift digest (\"\" for none)")
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
	r := digest.Runner{Store: st, Log: log, Platform: *platform, GitHubURL: os.Getenv("EXEMPLAR_GH_WEB_URL")}
	if !*dryRun {
		if r.Repo, err = configRepo(ctx); err != nil {
			return err
		}
	}
	outs, err := r.Run(ctx, snap)
	for _, o := range outs {
		printJSON(o)
		if *dryRun {
			fmt.Printf("--- %s\n%s\n", o.Title, o.Body)
		}
	}
	return err
}
