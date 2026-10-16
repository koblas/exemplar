package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/expiry"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runExpiry is daily rule expiry: rules past review_by get
// an extension PR for their owner, or a shadow PR that merges after
// expiry.auto_demote_after_days without a response. --dry-run prints the
// decisions and writes nothing.
func runExpiry(args []string) error {
	fs := flag.NewFlagSet("expiry", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	dryRun := fs.Bool("dry-run", false, "print decisions; write nothing")
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
	r := expiry.Runner{Stats: st, Log: log}
	if !*dryRun {
		if r.Repo, err = configRepo(ctx); err != nil {
			return err
		}
	}
	outs, err := r.Run(ctx, snap)
	for _, o := range outs {
		line, _ := json.Marshal(o)
		fmt.Println(string(line))
	}
	return err
}
