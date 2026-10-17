package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/evalgen"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runEvals generates eval cases for new candidates: positives
// and negatives from history, plus injected violations unless --no-inject.
func runEvals(args []string) error {
	fs := flag.NewFlagSet("evals", flag.ContinueOnError)
	noInject := fs.Bool("no-inject", false, "only history cases; no model calls or repo checkouts")
	maxNeg := fs.Int("max-negatives", 10, "negative cases per candidate")
	maxInj := fs.Int("max-injected", 3, "injected cases per candidate")
	workDir := fs.String("work-dir", "", "scratch directory for checkouts (default: a temp dir)")
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

	var inj *evalgen.Injector
	if !*noInject {
		gwURL, gwToken := os.Getenv("EXEMPLAR_GATEWAY_URL"), os.Getenv("EXEMPLAR_GATEWAY_TOKEN")
		if gwURL == "" || gwToken == "" {
			return fmt.Errorf("EXEMPLAR_GATEWAY_URL and EXEMPLAR_GATEWAY_TOKEN are %w for injection (or use --no-inject)", errRequired)
		}
		sc, err := redact.New()
		if err != nil {
			return fmt.Errorf("create redaction scanner: %w", err)
		}
		dir := *workDir
		if dir == "" {
			if dir, err = os.MkdirTemp("", "exemplar-evals-*"); err != nil {
				return fmt.Errorf("create temp dir: %w", err)
			}
			defer func() { _ = os.RemoveAll(dir) }()
		}
		files := &evalgen.CheckoutFiles{Root: dir, Token: checkoutToken(), CloneURL: gh.CloneURL(os.Getenv("EXEMPLAR_GH_WEB_URL"))}
		inj = &evalgen.Injector{Model: evalgen.NewGatewayModel(gwURL, gwToken, nil), Files: files, Scanner: sc}
	}
	reports, err := evalgen.New(evalgen.Config{MaxNegatives: *maxNeg, MaxInjected: *maxInj, Logger: log}, st, inj).Run(ctx)
	for _, r := range reports {
		printJSON(r)
	}
	if err != nil {
		return fmt.Errorf("run eval generation: %w", err)
	}
	return nil
}

// checkoutToken mints worker tokens for injection checkouts when the
// review app is configured; nil means anonymous checkouts.
func checkoutToken() func(ctx context.Context, repo string) (string, error) {
	app, installation, _, err := appFromEnv()
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
