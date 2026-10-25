package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/koblas/exemplar/internal/calibrate"
	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/calibration"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runCalibrate is the nightly calibration: the verifier (and the Stage 2
// categorizer, when configured) replayed through the deployed providers,
// recorded in calibration_results. A key that already passed is skipped,
// so most nights it costs nothing; --force reruns it.
func runCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout (required)")
	force := fs.Bool("force", false, "rerun a stage whose inputs already passed")
	dbLimit := fs.Int("db-limit", 500, "most verifier_labels rows to replay beside the seed set")
	workDir := fs.String("work-dir", "", "scratch directory (default: a temp dir)")
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

	snap, err := config.LoadDir(*configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	gwURL, gwToken := os.Getenv("EXEMPLAR_GATEWAY_URL"), optionalGatewayToken()
	if gwURL == "" || gwToken == "" {
		return fmt.Errorf("EXEMPLAR_GATEWAY_URL and EXEMPLAR_GATEWAY_TOKEN are %w", errRequired)
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
	sc, err := redact.New()
	if err != nil {
		return fmt.Errorf("create redaction scanner: %w", err)
	}
	dir := *workDir
	if dir == "" {
		if dir, err = os.MkdirTemp("", "exemplar-calibrate-*"); err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
	}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: gwURL, GatewayToken: gwToken})
	r := &calibrate.Runner{
		Engine: engine.New(h, sc, compiled.Runner{ASTGrep: os.Getenv("EXEMPLAR_AST_GREP")}), Scanner: sc, Snapshot: snap, WorkDir: dir,
	}
	r.CloneToken = cloneTokens(ctx)
	job := &calibration.Job{Store: st, Replay: r, Log: log, Force: *force, StoredLimit: *dbLimit}
	res, err := job.Run(ctx, snap)
	printJSON(res)
	if err != nil {
		return fmt.Errorf("run calibration: %w", err)
	}
	return nil
}

// cloneTokens mints read-only tokens for the repos recorded labels came
// from; nil without the review app, when only seed labels can be cloned.
func cloneTokens(ctx context.Context) func(context.Context, string) (string, error) {
	app, installation, _, err := appFromEnv(ctx)
	if err != nil {
		return nil
	}
	return func(ctx context.Context, repo string) (string, error) {
		tok, err := app.Token(ctx, installation, gh.FlavorWorker, []string{repo})
		if err != nil {
			return "", fmt.Errorf("mint installation token: %w", err)
		}
		return tok.GetToken(), nil
	}
}
