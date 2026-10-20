package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/compile"
	"github.com/koblas/exemplar/internal/learn/evalgen"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

// runCompile attempts deterministic compiles of candidates.
func runCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout: eval thresholds and eligibility (required)")
	limit := fs.Int("limit", 20, "candidates to attempt this run")
	workDir := fs.String("work-dir", "", "scratch directory for checkouts and cases (default: a temp dir)")
	requireSandbox := fs.Bool("require-sandbox", true, "refuse to run model-written rules without the Linux sandbox (unshare + prlimit)")
	memLimit := fs.Int64("sandbox-memory", compiled.EvalMemoryLimit, "data-segment limit per engine run, bytes")
	cpuLimit := fs.Int("sandbox-cpu", 120, "CPU-time limit per engine run, seconds")
	astGrep := fs.String("ast-grep", "ast-grep", "ast-grep binary")
	semgrep := fs.String("semgrep", "semgrep", "semgrep binary")
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
		return fmt.Errorf("--config is %w", errRequired)
	}
	snap, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return fmt.Errorf("load config snapshot: %w", err)
	}
	th := snap.Global.EvalThresholds
	for _, bin := range []string{*astGrep, *semgrep} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s is %w", bin, errEngineNotInstalled)
		}
	}
	runner := compiled.Runner{ASTGrep: *astGrep, Semgrep: *semgrep, Timeout: 2 * time.Minute}
	sandbox := "none"
	if cmd, ok := compiled.SandboxCommand(*memLimit, *cpuLimit); ok {
		runner.Sandbox, sandbox = cmd, strings.Join(cmd, " ")
	} else if *requireSandbox {
		return errSandboxUnavailable
	} else {
		log.Warn("running engines without a sandbox")
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
		if dir, err = os.MkdirTemp("", "exemplar-compile-*"); err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
	}
	files := &evalgen.CheckoutFiles{Root: dir, CloneURL: gh.CloneURL(os.Getenv("EXEMPLAR_GH_WEB_URL"))}
	if app, installation, _, err := appFromEnv(ctx); err == nil {
		files.Token = func(ctx context.Context, repo string) (string, error) {
			tok, err := app.Token(ctx, installation, gh.FlavorWorker, []string{repo})
			if err != nil {
				return "", fmt.Errorf("mint installation token: %w", err)
			}
			return tok.GetToken(), nil
		}
	}
	job, err := compile.New(compile.Config{
		Thresholds: compile.Thresholds{MinCases: th.MinCases, Precision: th.Precision, Recall: th.Recall},
		Scratch:    dir, Sandbox: sandbox, Logger: log,
		Eligible: func(repo string) bool { return snap.CheckEligible(repo) == nil },
	}, st, compile.NewGatewayModel(gwURL, gwToken, nil), runner, files, sc)
	if err != nil {
		return fmt.Errorf("create compile job: %w", err)
	}
	rep, err := job.Run(ctx, *limit)
	printJSON(rep)
	if err != nil {
		return fmt.Errorf("run compile: %w", err)
	}
	return nil
}
