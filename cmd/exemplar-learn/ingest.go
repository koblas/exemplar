package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/ingest"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (eligibility and opted-in repos)")
	repos := fs.String("repos", "", "comma-separated owner/name repos (default: every opted-in repo)")
	once := fs.Bool("once", false, "stop at the first rate limit instead of waiting for the reset")
	maxPages := fs.Int("max-pages", 0, "pages per repo per run (0 = until done)")
	pageSize := fs.Int("page-size", 25, "PRs per GraphQL page")
	docHosts := fs.String("doc-hosts", os.Getenv("EXEMPLAR_DOC_HOSTS"), "comma-separated internal docs hosts (FR-L1.6)")
	bots := fs.String("bot-logins", os.Getenv("EXEMPLAR_BOT_LOGINS"), "comma-separated extra bot logins, including the Exemplar app's")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-learn")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	if *configDir == "" {
		return fmt.Errorf("--config is %w: mining is gated on exemplar-config eligibility", errRequired)
	}
	snap, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	targets := split(*repos)
	if len(targets) == 0 {
		for r, rc := range snap.Repos {
			if rc.Enabled {
				targets = append(targets, r)
			}
		}
		slices.Sort(targets)
	}
	key := os.Getenv("EXEMPLAR_PSEUDONYM_KEY")
	pseudo, err := ingest.NewPseudonymizer([]byte(key))
	if err != nil {
		return fmt.Errorf("EXEMPLAR_PSEUDONYM_KEY: %w", err)
	}
	dbURL := os.Getenv("EXEMPLAR_DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("EXEMPLAR_DATABASE_URL is %w", errRequired)
	}
	app, installation, _, err := appFromEnv()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dbURL)
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
	cfg := ingest.Config{BotLogins: split(*bots), DocHosts: split(*docHosts), PageSize: *pageSize, MaxPages: *maxPages, Logger: log}

	var failed []string
	for _, repo := range targets {
		for {
			// A fresh read-only token per run: backfills outlive the 1h token.
			c, err := app.Client(ctx, installation, gh.FlavorIngest, []string{repo})
			if err != nil {
				return fmt.Errorf("create github client: %w", err)
			}
			q := func(ctx context.Context, query string, vars map[string]any, out any) error {
				return gh.GraphQL(ctx, c, query, vars, out)
			}
			job, err := ingest.New(cfg, q, st, snap, sc, pseudo)
			if err != nil {
				return fmt.Errorf("create ingest job: %w", err)
			}
			stats, err := job.Run(ctx, repo)
			if err != nil {
				// Other repos still run; the cursor keeps this one's progress.
				log.Error("ingest failed", "repo", repo, "err", err)
				failed = append(failed, repo)
				break
			}
			printJSON(stats)
			if stats.ResumeAt == nil || *once || *maxPages > 0 {
				break
			}
			log.Info("ingest rate limited; waiting", "repo", repo, "until", *stats.ResumeAt)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Until(*stats.ResumeAt) + 5*time.Second):
			}
		}
		if ctx.Err() != nil {
			return nil
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w for %s", errIngestFailed, strings.Join(failed, ", "))
	}
	return nil
}

func split(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
