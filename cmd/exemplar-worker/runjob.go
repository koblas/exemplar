package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
	"github.com/koblas/exemplar/internal/worker"
)

// runnerCancelPoll is how often a runner reads its job row for a remote
// cancel (a superseded review).
const runnerCancelPoll = 5 * time.Second

// exitError carries a runner's exit code out of runJob.
type exitError struct{ code worker.Exit }

func (e exitError) Error() string { return fmt.Sprintf("runner exited %d", e.code) }

// runJob runs one dispatched job and exits with its worker.Exit code.
func runJob(args []string) error {
	fs := flag.NewFlagSet("run-job", flag.ContinueOnError)
	jobID := fs.Int64("job", 0, "River job id (required)")
	version := fs.String("config-version", "", "exemplar-config version to review with (required)")
	workDir := fs.String("work-dir", "", "scratch space for the checkout (required)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *jobID < 1 || *version == "" || *workDir == "" {
		return fmt.Errorf("--job, --config-version and --work-dir are %w", errRequired)
	}
	nonce := os.Getenv("EXEMPLAR_RUNNER_NONCE")
	if nonce == "" {
		return fmt.Errorf("EXEMPLAR_RUNNER_NONCE is %w", errRequired)
	}
	env, err := envFromOS(false)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*workDir, 0o750); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}

	base := context.Background()
	log, shutdown, err := telemetry.Setup(base, "exemplar-worker")
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	log = log.With("job", *jobID)

	// Control migrates; a runner only reads and writes.
	st, err := store.OpenWithConns(base, env.dbURL, 4)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	jobs, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}
	wcfg, err := env.workerConfig(st, *workDir, nil, log)
	if err != nil {
		return err
	}
	claims, err := env.claims(st)
	if err != nil {
		return err
	}
	r := &worker.Runner{
		Config: wcfg, Jobs: jobs, CancelPoll: runnerCancelPoll, Claims: claims, BaseURL: os.Getenv("EXEMPLAR_GH_API_URL"),
		Snapshot: func(ctx context.Context, v string) (*config.Snapshot, error) {
			return worker.StoredSnapshot(ctx, st, v)
		},
	}

	ctx, stop := cancelOnSignal(base, jobs, *jobID, log)
	defer stop()
	exit := r.Run(ctx, *jobID, *version, nonce)
	log.Info("runner done", "exit", int(exit))
	if exit != worker.ExitDone {
		return exitError{exit}
	}
	return nil
}

// cancelOnSignal returns a context that SIGTERM (the Job deleted, or the
// node draining) cancels; when River asked for the cancel, the cause is a
// supersede.
func cancelOnSignal(base context.Context, jobs worker.RunnerJobs, id int64, log *slog.Logger) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(base)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, os.Interrupt)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-sigs:
		}
		cctx, ccancel := context.WithTimeout(base, 5*time.Second)
		superseded := worker.CancelRequested(cctx, jobs, id)
		ccancel()
		cause := context.Canceled
		if superseded {
			cause = rivertype.ErrJobCancelledRemotely
		}
		log.Info("runner signalled; cancelling", "superseded", superseded)
		cancel(cause)
	}()
	return ctx, func() { signal.Stop(sigs); cancel(nil) }
}
