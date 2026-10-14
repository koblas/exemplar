package main

import (
	"context"
	"encoding/json"
	"errors"
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

// runCompile attempts deterministic compiles of candidates (spec §8.6).
func runCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	configDir := fs.String("config", "", "exemplar-config checkout: eval thresholds and eligibility (required)")
	limit := fs.Int("limit", 20, "candidates to attempt this run")
	workDir := fs.String("work-dir", "", "scratch directory for checkouts and cases (default: a temp dir)")
	requireSandbox := fs.Bool("require-sandbox", true, "refuse to run model-written rules without the Linux sandbox (unshare + prlimit)")
	memLimit := fs.Int64("sandbox-memory", 2<<30, "address-space limit per engine run, bytes")
	cpuLimit := fs.Int("sandbox-cpu", 120, "CPU-time limit per engine run, seconds")
	astGrep := fs.String("ast-grep", "ast-grep", "ast-grep binary")
	semgrep := fs.String("semgrep", "semgrep", "semgrep binary")
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

	if *configDir == "" {
		return errors.New("--config is required")
	}
	snap, err := config.LoadSnapshot(os.DirFS(*configDir), *configDir)
	if err != nil {
		return err
	}
	th := snap.Global.EvalThresholds
	for _, bin := range []string{*astGrep, *semgrep} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s is not installed: compile needs both engines", bin)
		}
	}
	runner := compiled.Runner{ASTGrep: *astGrep, Semgrep: *semgrep, Timeout: 2 * time.Minute}
	sandbox := "none"
	if cmd, ok := compiled.SandboxCommand(*memLimit, *cpuLimit); ok {
		runner.Sandbox, sandbox = cmd, strings.Join(cmd, " ")
	} else if *requireSandbox {
		return errors.New("the sandbox (Linux unshare and prlimit) is unavailable; model-written rules are not run without it (--require-sandbox=false only for local runs)")
	} else {
		log.Warn("running engines without a sandbox")
	}

	gwURL, gwToken := os.Getenv("EXEMPLAR_GATEWAY_URL"), os.Getenv("EXEMPLAR_GATEWAY_TOKEN")
	if gwURL == "" || gwToken == "" {
		return errors.New("EXEMPLAR_GATEWAY_URL and EXEMPLAR_GATEWAY_TOKEN are required")
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
	sc, err := redact.New()
	if err != nil {
		return err
	}
	dir := *workDir
	if dir == "" {
		if dir, err = os.MkdirTemp("", "exemplar-compile-*"); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
	}
	files := &evalgen.CheckoutFiles{Root: dir}
	if app, installation, _, err := appFromEnv(); err == nil {
		files.Token = func(ctx context.Context, repo string) (string, error) {
			tok, err := app.Token(ctx, installation, gh.FlavorWorker, []string{repo})
			if err != nil {
				return "", err
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
		return err
	}
	rep, err := job.Run(ctx, *limit)
	line, _ := json.Marshal(rep)
	fmt.Println(string(line))
	return err
}
