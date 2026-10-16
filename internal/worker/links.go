package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/feedbacklink"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
)

// Feedback links: exemplar-api verified the link's
// signature and the clicker's GitHub identity, and enqueued this job. The
// role comes from PR participation before the click: a click never
// makes someone a reviewer on its own, and anyone else is an observer.

// FeedbackLinkWorker runs feedback_link jobs.
type FeedbackLinkWorker struct {
	river.WorkerDefaults[store.FeedbackLinkArgs]

	cfg Config
}

// NewFeedbackLinkWorker validates cfg; it needs GitHub, Store, Snapshot
// and Engine.
func NewFeedbackLinkWorker(cfg Config) (*FeedbackLinkWorker, error) {
	if cfg.Store == nil {
		return nil, errors.New("worker: feedback links need a store")
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &FeedbackLinkWorker{cfg: w.cfg}, nil
}

// Work implements river.Worker.
func (w *FeedbackLinkWorker) Work(ctx context.Context, job *river.Job[store.FeedbackLinkArgs]) error {
	a := job.Args
	out, err := w.Handle(ctx, a)
	if err != nil {
		return err
	}
	w.cfg.Log.Info("feedback link", "finding", a.FindingID, "reason", a.Reason, "recorded", out.Kinds, "role", out.Role, "ignored", out.Ignored)
	return nil
}

// Handle records one confirmed link.
func (w *FeedbackLinkWorker) Handle(ctx context.Context, a store.FeedbackLinkArgs) (SignalOutcome, error) {
	kind := feedbacklink.Kind(a.Reason)
	if kind == "" {
		return SignalOutcome{}, river.JobCancel(fmt.Errorf("worker: unknown link reason %q", a.Reason))
	}
	ref, err := w.cfg.Store.FindingRef(ctx, a.FindingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SignalOutcome{Ignored: "finding no longer stored"}, nil // retention removed it
	}
	if err != nil {
		return SignalOutcome{}, err
	}
	if ref.InstallationID == 0 {
		return SignalOutcome{}, fmt.Errorf("worker: no installation known for %s", ref.Repo)
	}
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: config: %w", err)
	}
	sig := store.FeedbackSignalArgs{InstallationID: ref.InstallationID, Repo: ref.Repo, PR: int(ref.PrNumber), Actor: a.Actor, At: a.At}
	role, err := (&FeedbackSignalWorker{cfg: w.cfg}).role(ctx, sig, snap, ref.RuleID, true)
	if err != nil {
		return SignalOutcome{}, err
	}
	n, err := pipeline.RecordEvents(ctx, w.cfg.Store, snap.Global.Feedback, []pipeline.Event{{
		FindingID: a.FindingID, Kind: kind, Actor: a.Actor, Role: role, Via: pipeline.ViaLink, ReasonText: a.Text,
	}})
	if err != nil {
		return SignalOutcome{}, err
	}
	if n > 0 {
		teach(ctx, w.cfg, ref.Repo, a.FindingID, kind, a.Actor, a.Text)
	}
	return SignalOutcome{Kinds: []string{kind}, Role: role}, nil
}
