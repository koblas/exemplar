package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/envcfg"
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
		return fmt.Errorf("parse flags: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-learn")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

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
	sc, err := redact.New()
	if err != nil {
		return fmt.Errorf("create redaction scanner: %w", err)
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
		printJSON(o)
		if *dryRun && *showRules && o.Draft != nil {
			fmt.Printf("--- %s\n%s\n", o.Draft.Path, o.Draft.Rule)
		}
	}
	if err != nil {
		return fmt.Errorf("run propose: %w", err)
	}
	return nil
}

// configRepo builds the config-writer client: the only token flavor that
// can write to exemplar-config, and nothing else.
func configRepo(ctx context.Context) (*gh.ConfigRepo, error) {
	app, installation, cfgRepo, err := appFromEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w (or use --dry-run)", err)
	}
	client, err := app.Client(ctx, installation, gh.FlavorConfigWriter, []string{cfgRepo})
	if err != nil {
		return nil, fmt.Errorf("create github client: %w", err)
	}
	repo, err := gh.NewConfigRepo(client, cfgRepo)
	if err != nil {
		return nil, fmt.Errorf("open config repo: %w", err)
	}
	return repo, nil
}

// appFromEnv builds the review app from EXEMPLAR_GH_* settings.
func appFromEnv(ctx context.Context) (*gh.App, int64, string, error) {
	if err := envcfg.Check(os.Getenv, "EXEMPLAR_GH_APP_ID", "EXEMPLAR_GH_INSTALLATION_ID", "EXEMPLAR_CONFIG_REPO", "EXEMPLAR_GH_API_URL"); err != nil {
		return nil, 0, "", fmt.Errorf("settings: %w", err)
	}
	appID, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_APP_ID"), 10, 64)
	installation, _ := strconv.ParseInt(os.Getenv("EXEMPLAR_GH_INSTALLATION_ID"), 10, 64)
	keyFile, cfgRepo := os.Getenv("EXEMPLAR_GH_PRIVATE_KEY_FILE"), os.Getenv("EXEMPLAR_CONFIG_REPO")
	if appID == 0 || keyFile == "" || cfgRepo == "" {
		return nil, 0, "", fmt.Errorf("EXEMPLAR_GH_APP_ID, EXEMPLAR_GH_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO are %w", errRequired)
	}
	key, err := os.ReadFile(keyFile) //nolint:gosec // G703: the path comes from the operator's environment, not from a request
	if err != nil {
		return nil, 0, "", fmt.Errorf("read private key: %w", err)
	}
	app, err := gh.NewApp(gh.AppConfig{AppID: appID, PrivateKey: key, ConfigRepo: cfgRepo, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL")}, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("create github app: %w", err)
	}
	if installation == 0 {
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if installation, err = app.ConfigInstallation(lctx); err != nil {
			return nil, 0, "", fmt.Errorf("EXEMPLAR_GH_INSTALLATION_ID unset and no installation found on %s: %w", cfgRepo, err)
		}
	}
	return app, installation, cfgRepo, nil
}

// gatewayToken is EXEMPLAR_GATEWAY_TOKEN, or the token derived from the
// root key.
func gatewayToken() (string, error) {
	tok, err := envcfg.NewKeys(os.Getenv, os.ReadFile).Token("EXEMPLAR_GATEWAY_TOKEN", envcfg.LabelGatewayToken)
	if err != nil {
		return "", fmt.Errorf("gateway token: %w", err)
	}
	return tok, nil
}

// optionalGatewayToken is gatewayToken, or "" when neither is set: the
// callers report the missing gateway with their own advice.
func optionalGatewayToken() string {
	tok, _ := gatewayToken()
	return tok
}
