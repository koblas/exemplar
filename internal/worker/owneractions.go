package worker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/owneract"
	"github.com/koblas/exemplar/internal/store"
)

// OwnerActionWorker runs owner_action jobs: the web UI's shadow, kill,
// candidate-kill and kill-switch requests (spec §12.8), with the
// config-writer token, which only this job uses in the worker.
type OwnerActionWorker struct {
	river.WorkerDefaults[store.OwnerActionArgs]
	runner   owneract.Runner
	snapshot func(context.Context) (*config.Snapshot, error)
	log      *slog.Logger
}

// NewOwnerActionWorker validates its inputs.
func NewOwnerActionWorker(r owneract.Runner, snapshot func(context.Context) (*config.Snapshot, error), log *slog.Logger) (*OwnerActionWorker, error) {
	if r.Store == nil || r.Repo == nil || r.Member == nil || snapshot == nil {
		return nil, errors.New("worker: owner actions need a store, the config repo, team membership and the config")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r.Log = log
	return &OwnerActionWorker{runner: r, snapshot: snapshot, log: log}, nil
}

// Work implements river.Worker. A failure is retried; after the last
// attempt the action is recorded as failed so the UI shows it.
func (w *OwnerActionWorker) Work(ctx context.Context, job *river.Job[store.OwnerActionArgs]) error {
	snap, err := w.snapshot(ctx)
	if err != nil {
		return err
	}
	out, err := w.runner.Run(ctx, snap, job.Args.ID)
	if err != nil {
		if job.Attempt >= job.MaxAttempts {
			w.log.Error("owner action failed", "id", job.Args.ID, "err", err)
			return w.runner.Fail(context.WithoutCancel(ctx), job.Args.ID, err)
		}
		return err
	}
	w.log.Info("owner action", "id", job.Args.ID, "status", out.Status, "result", out.Result, "url", out.URL)
	return nil
}
