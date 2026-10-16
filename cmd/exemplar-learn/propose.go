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

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/propose"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runPropose opens draft PRs in exemplar-config for the top candidates.
// --dry-run builds and validates the drafts and prints them without any
// GitHub access.
func runPropose(args []string) error {
	fs := flag.NewFlagSet("propose", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "build and print drafts; write nothing")
	maxOpen := fs.Int("max-open-per-owner", 3, "open candidate PRs allowed per owner team")
	maxRun := fs.Int("max-per-run", 10, "PRs opened per run")
	showRules := fs.Bool("show-rules", false, "with --dry-run, print each drafted rule file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-learn")
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

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
	sc, err := redact.New()
	if err != nil {
		return err
	}

	var repo propose.Repo
	if !*dryRun {
		if repo, err = configRepo(ctx); err != nil {
			return err
		}
	}
	w := propose.New(propose.Config{MaxOpenPerOwner: *maxOpen, MaxPerRun: *maxRun, Logger: log}, st, repo, sc)
	outs, err := w.Run(ctx)
	for _, o := range outs {
		line, _ := json.Marshal(o)
		fmt.Println(string(line))
		if *dryRun && *showRules && o.Draft != nil {
			fmt.Printf("--- %s\n%s\n", o.Draft.Path, o.Draft.Rule)
		}
	}
	return err
}

// configRepo builds the config-writer client: the only token flavor that
// can write to exemplar-config, and nothing else.
func configRepo(ctx context.Context) (*gh.ConfigRepo, error) {
	app, installation, cfgRepo, err := appFromEnv()
	if err != nil {
		return nil, fmt.Errorf("%w (or use --dry-run)", err)
	}
	client, err := app.Client(ctx, installation, gh.FlavorConfigWriter, []string{cfgRepo})
	if err != nil {
		return nil, err
	}
	return gh.NewConfigRepo(client, cfgRepo)
}

// appFromEnv builds the review app from EXEMPLAR_GH_* settings.
func appFromEnv() (*gh.App, int64, string, error) {
	appID, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_APP_ID"), 10, 64)
	installation, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_INSTALLATION_ID"), 10, 64)
	keyFile, cfgRepo := os.Getenv("EXEMPLAR_GH_PRIVATE_KEY_FILE"), os.Getenv("EXEMPLAR_CONFIG_REPO")
	if appID == 0 || installation == 0 || keyFile == "" || cfgRepo == "" {
		return nil, 0, "", errors.New("EXEMPLAR_GH_APP_ID, EXEMPLAR_GH_INSTALLATION_ID, EXEMPLAR_GH_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO are required")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read private key: %w", err)
	}
	app, err := gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: cfgRepo, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL")}, nil)
	return app, installation, cfgRepo, err
}
