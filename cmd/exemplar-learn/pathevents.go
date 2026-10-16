package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/ingest"
	"github.com/koblas/exemplar/internal/learn/pathevents"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

func runPathEvents(args []string) error {
	fs := flag.NewFlagSet("path-events", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (eligibility and opted-in repos)")
	repos := fs.String("repos", "", "comma-separated owner/name repos to scan for reverts (default: every opted-in repo)")
	incidents := fs.String("incidents", "", "incident feed (JSON Lines) to load; see internal/learn/pathevents")
	noReverts := fs.Bool("no-reverts", false, "only load the incident feed")
	noSeeds := fs.Bool("no-seeds", false, "record revert events only; write no revert seeds for the extractor")
	docHosts := fs.String("doc-hosts", os.Getenv("EXEMPLAR_DOC_HOSTS"), "comma-separated internal docs hosts (FR-L1.6)")
	bots := fs.String("bot-logins", os.Getenv("EXEMPLAR_BOT_LOGINS"), "comma-separated extra bot logins, including the Exemplar app's")
	backfill := fs.Duration("backfill", 365*24*time.Hour, "how far back a repo's first revert scan goes")
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
		return fmt.Errorf("--config is %w: path events are gated on exemplar-config eligibility", errRequired)
	}
	snap, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	// Parse the feed before touching GitHub so a bad export fails fast.
	var incs []pathevents.Incident
	if *incidents != "" {
		f, err := os.Open(*incidents)
		if err != nil {
			return fmt.Errorf("open incidents: %w", err)
		}
		incs, err = pathevents.ReadIncidents(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("read incidents: %w", err)
		}
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
	connect := func(ctx context.Context, repo string) (pathevents.API, error) {
		c, err := app.Client(ctx, installation, gh.FlavorIngest, []string{repo})
		if err != nil {
			return pathevents.API{}, fmt.Errorf("create github client: %w", err)
		}
		return pathevents.API{
			Query: func(ctx context.Context, q string, vars map[string]any, out any) error {
				return gh.GraphQL(ctx, c, q, vars, out)
			},
			PRFiles: func(ctx context.Context, owner, name string, number int) ([]gh.FilePatch, error) {
				return gh.PRFilePatches(ctx, c, owner, name, number)
			},
			CommitFiles: func(ctx context.Context, owner, name, sha string) ([]string, error) {
				return gh.CommitFiles(ctx, c, owner, name, sha)
			},
		}, nil
	}
	cfg := pathevents.Config{Backfill: *backfill, Logger: log}
	if !*noReverts && !*noSeeds {
		// Seeds carry code and free text: same handling as ingest.
		pseudo, err := ingest.NewPseudonymizer([]byte(os.Getenv("EXEMPLAR_PSEUDONYM_KEY")))
		if err != nil {
			return fmt.Errorf("EXEMPLAR_PSEUDONYM_KEY (or --no-seeds): %w", err)
		}
		sc, err := redact.New()
		if err != nil {
			return fmt.Errorf("create redaction scanner: %w", err)
		}
		cfg.Seeds = &pathevents.Seeds{Pseudonyms: pseudo, Scanner: sc, BotLogins: split(*bots), DocHosts: split(*docHosts)}
	}
	job, err := pathevents.New(cfg, connect, st, snap)
	if err != nil {
		return fmt.Errorf("create path events job: %w", err)
	}

	var failed []string
	if !*noReverts {
		targets := split(*repos)
		if len(targets) == 0 {
			for r, rc := range snap.Repos {
				if rc.Enabled {
					targets = append(targets, r)
				}
			}
			slices.Sort(targets)
		}
		for _, repo := range targets {
			stats, err := job.Reverts(ctx, repo)
			if err != nil {
				log.Error("revert scan failed", "repo", repo, "err", err)
				failed = append(failed, repo)
				continue
			}
			printJSON(stats)
		}
	}
	if len(incs) > 0 {
		stats, err := job.Incidents(ctx, incs)
		if err != nil {
			return fmt.Errorf("process incidents: %w", err)
		}
		printJSON(stats)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w for %v", errRevertScanFailed, failed)
	}
	return nil
}

// printJSON prints v as one line of JSON on stdout, reporting an encoding
// failure on stderr instead.
func printJSON(v any) {
	out, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode output: %v\n", err)
		return
	}
	fmt.Println(string(out))
}
