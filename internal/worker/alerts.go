package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Code-scanning dismissals: dismissing one of Exemplar's
// alerts on a PR is feedback on its finding. The dismisser is the PR's
// author, a member of the rule's owning team (owner), or otherwise a
// reviewer: dismissing needs security-events permission on the repo, so
// it is never an observer's.

// dismissalKinds maps GitHub's dismissal reasons to feedback kinds.
var dismissalKinds = map[string]string{
	"false positive": "fb_wrong",
	"won't fix":      "fb_not_important",
	"used in tests":  "fb_not_important",
}

// AlertDismissalWorker runs alert_dismissal jobs.
type AlertDismissalWorker struct {
	river.WorkerDefaults[store.AlertDismissalArgs]
	cfg Config
}

// NewAlertDismissalWorker validates cfg; it needs GitHub, Store and
// Snapshot.
func NewAlertDismissalWorker(cfg Config) (*AlertDismissalWorker, error) {
	if cfg.Store == nil {
		return nil, errors.New("worker: alert dismissals need a store")
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &AlertDismissalWorker{cfg: w.cfg}, nil
}

// Work implements river.Worker.
func (w *AlertDismissalWorker) Work(ctx context.Context, job *river.Job[store.AlertDismissalArgs]) error {
	a := job.Args
	out, err := w.Handle(ctx, a)
	if err != nil {
		return err
	}
	w.cfg.Log.Info("alert dismissal", "repo", a.Repo, "pr", a.PR, "alert", a.Alert, "recorded", out.Recorded, "role", out.Role, "ignored", out.Ignored)
	return nil
}

// Handle records one dismissal.
func (w *AlertDismissalWorker) Handle(ctx context.Context, a store.AlertDismissalArgs) (CommandOutcome, error) {
	kind, ok := dismissalKinds[strings.ToLower(a.Reason)]
	if !ok {
		return CommandOutcome{Ignored: "dismissal reason " + a.Reason}, nil
	}
	rows, err := w.cfg.Store.FindingsForAlert(ctx, queries.FindingsForAlertParams{Repo: a.Repo, PrNumber: int32(a.PR), RuleID: a.RuleID, Path: a.Path})
	if err != nil {
		return CommandOutcome{}, err
	}
	var id int64
	for _, r := range rows {
		if int(r.StartLine) == a.StartLine {
			id = r.ID
			break
		}
	}
	if id == 0 && len(rows) == 1 {
		id = rows[0].ID // the code moved since it was posted; the only candidate
	}
	if id == 0 {
		return CommandOutcome{Ignored: fmt.Sprintf("no single finding for the alert (%d candidates)", len(rows))}, nil
	}
	if err := w.cfg.Store.SetSarifAlert(ctx, queries.SetSarifAlertParams{ID: id, SarifAlertNumber: int32Ptr(a.Alert)}); err != nil {
		return CommandOutcome{}, err
	}
	reader, err := w.cfg.GitHub.Reader(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return CommandOutcome{}, err
	}
	pr, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return CommandOutcome{}, err
	}
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: config: %w", err)
	}
	role := pipeline.RoleReviewer
	if strings.EqualFold(a.Actor, pr.Author) {
		role = pipeline.RoleAuthor
	} else if owner, err := isRuleOwner(ctx, w.cfg.GitHub, a.InstallationID, a.Repo, ruleOwner(snap, a.RuleID), a.Actor); err != nil {
		return CommandOutcome{}, err
	} else if owner {
		role = pipeline.RoleOwner
	}
	n, err := pipeline.RecordEvents(ctx, w.cfg.Store, snap.Global.Feedback, []pipeline.Event{{
		FindingID: id, Kind: kind, Actor: a.Actor, Role: role, Via: pipeline.ViaCodeScanning, ReasonText: a.Comment,
		Evidence: map[string]any{"alert": a.Alert, "dismissed_reason": a.Reason},
	}})
	if err != nil {
		return CommandOutcome{}, err
	}
	if n > 0 {
		teach(ctx, w.cfg, a.Repo, id, kind, a.Actor, a.Comment)
	}
	return CommandOutcome{Recorded: kind, Role: role}, nil
}

func int32Ptr(n int) *int32 {
	v := int32(n)
	return &v
}
