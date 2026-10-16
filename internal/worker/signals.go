package worker

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Webhook-observed feedback signals:
//   - resolved: someone resolved an Exemplar thread (ambiguous alone: it is
//     kept, but enters neither acceptance nor eFP);
//   - reply: a reply in an Exemplar thread that is not a command, classified
//     agree, disagree or question by the light model;
//   - comment: a later review comment overlapping a finding's lines (posted,
//     shadow or suppressed) that the light model judges the same concern
//     is the commenter's agreement (human_agreed);
//   - merged: findings still posted and with no signal when the PR merges
//     were ignored.

// replyKinds maps reply classes to feedback kinds.
var replyKinds = map[string]string{
	engine.ReplyAgree:    "reply_agree",
	engine.ReplyDisagree: "reply_disagree",
	engine.ReplyQuestion: "reply_question",
}

// maxCommentJudgements bounds the judge calls one comment makes.
const maxCommentJudgements = 5

// FeedbackSignalWorker runs feedback_signal jobs.
type FeedbackSignalWorker struct {
	river.WorkerDefaults[store.FeedbackSignalArgs]

	cfg Config
}

// NewFeedbackSignalWorker validates cfg; it needs GitHub, Store, Snapshot
// and Engine.
func NewFeedbackSignalWorker(cfg Config) (*FeedbackSignalWorker, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: feedback signals need a store", errMissingDependency)
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &FeedbackSignalWorker{cfg: w.cfg}, nil
}

// SignalOutcome is what one signal recorded, for logs and tests.
type SignalOutcome struct {
	Ignored string
	Kinds   []string // the event kinds recorded
	Role    string
}

// Work implements river.Worker.
func (w *FeedbackSignalWorker) Work(ctx context.Context, job *river.Job[store.FeedbackSignalArgs]) error {
	a := job.Args
	out, err := w.Handle(ctx, a)
	if err != nil {
		return err
	}
	w.cfg.Log.Info("feedback signal", "repo", a.Repo, "pr", a.PR, "signal", a.Signal, "recorded", out.Kinds, "role", out.Role, "ignored", out.Ignored)
	return nil
}

// Handle records one signal.
func (w *FeedbackSignalWorker) Handle(ctx context.Context, a store.FeedbackSignalArgs) (SignalOutcome, error) {
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: config: %w", err)
	}
	switch a.Signal {
	case store.SignalMerged:
		return w.merged(ctx, a, snap)
	case store.SignalResolved, store.SignalReply:
		return w.inThread(ctx, a, snap)
	case store.SignalComment:
		return w.comment(ctx, a, snap)
	}
	return SignalOutcome{}, river.JobCancel(fmt.Errorf("%w: unknown signal %q", errBadJob, a.Signal))
}

// merged records ignored for each finding still posted with no signal.
func (w *FeedbackSignalWorker) merged(ctx context.Context, a store.FeedbackSignalArgs, snap *config.Snapshot) (SignalOutcome, error) {
	// The merge itself, for counter-metrics.
	if a.HeadSHA != "" && !a.At.IsZero() {
		if err := w.cfg.Store.UpsertPRMerge(ctx, queries.UpsertPRMergeParams{
			Repo: a.Repo, PrNumber: int32(a.PR), HeadSha: a.HeadSHA,
			MergedAt: pgtype.Timestamptz{Time: a.At, Valid: true},
		}); err != nil {
			return SignalOutcome{}, fmt.Errorf("worker: upsert pr merge: %w", err)
		}
	}
	// Reactions are polled once more at close, so a finding
	// someone reacted to is not counted as ignored.
	if _, err := pollReactions(ctx, w.cfg, snap, a.InstallationID, a.Repo, a.PR); err != nil {
		return SignalOutcome{}, err
	}
	open, err := w.cfg.Store.OpenFindings(ctx, queries.OpenFindingsParams{Repo: a.Repo, PrNumber: int32(a.PR)})
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: open findings: %w", err)
	}
	var evs []pipeline.Event
	for _, f := range open {
		has, err := w.cfg.Store.FindingHasFeedback(ctx, f.ID)
		if err != nil {
			return SignalOutcome{}, fmt.Errorf("worker: finding feedback: %w", err)
		}
		if !has {
			evs = append(evs, pipeline.Event{FindingID: f.ID, Kind: "ignored", Actor: a.Actor, Role: pipeline.RoleAuthor, Via: pipeline.ViaInferred})
		}
	}
	if len(evs) == 0 {
		return SignalOutcome{Ignored: "no untouched findings"}, nil
	}
	if _, err := pipeline.RecordEvents(ctx, w.cfg.Store, snap.Global.Feedback, evs); err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: record events: %w", err)
	}
	out := SignalOutcome{Role: pipeline.RoleAuthor}
	for range evs {
		out.Kinds = append(out.Kinds, "ignored")
	}
	return out, nil
}

// inThread records a resolution of, or a reply in, an Exemplar thread.
func (w *FeedbackSignalWorker) inThread(ctx context.Context, a store.FeedbackSignalArgs, snap *config.Snapshot) (SignalOutcome, error) {
	rows, err := w.cfg.Store.FindingByComment(ctx, queries.FindingByCommentParams{Repo: a.Repo, PrNumber: int32(a.PR), GhCommentID: &a.ThreadRoot})
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: finding by comment: %w", err)
	}
	if len(rows) == 0 {
		return SignalOutcome{Ignored: "not an Exemplar thread"}, nil
	}
	id := rows[0].ID
	kind := "resolved"
	if a.Signal == store.SignalReply {
		d, err := w.cfg.Store.FindingDetail(ctx, id)
		if err != nil {
			return SignalOutcome{}, fmt.Errorf("worker: finding detail: %w", err)
		}
		class := w.cfg.Engine.ClassifyReply(ctx, engine.CallMeta{Caller: Caller, Repo: a.Repo}, findingOf(d), a.Body)
		if kind = replyKinds[class]; kind == "" {
			return SignalOutcome{Ignored: "reply class " + class}, nil
		}
	}
	// Resolving needs write access, so a non-author resolver is a reviewer
	// at least; a reply's author must have taken part to weigh.
	role, err := w.role(ctx, a, snap, rows[0].RuleID, a.Signal == store.SignalReply)
	if err != nil {
		return SignalOutcome{}, err
	}
	if _, err := pipeline.RecordEvents(ctx, w.cfg.Store, snap.Global.Feedback, []pipeline.Event{{
		FindingID: id, Kind: kind, Actor: a.Actor, Role: role, Via: pipeline.ViaWebhook, ReasonText: a.Body,
		Evidence: map[string]any{"comment_id": a.CommentID},
	}}); err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: record events: %w", err)
	}
	return SignalOutcome{Kinds: []string{kind}, Role: role}, nil
}

// comment records human_agreed on findings a later review comment raises
// again. Only trusted reviewers count, as for human-comment dedupe.
func (w *FeedbackSignalWorker) comment(ctx context.Context, a store.FeedbackSignalArgs, snap *config.Snapshot) (SignalOutcome, error) {
	if !slices.Contains([]string{"OWNER", "MEMBER", "COLLABORATOR"}, strings.ToUpper(a.Association)) {
		return SignalOutcome{Ignored: "commenter is not a member or collaborator"}, nil
	}
	rows, err := w.cfg.Store.FindingsOnLines(ctx, queries.FindingsOnLinesParams{
		Repo: a.Repo, PrNumber: int32(a.PR), Path: a.Path, Side: a.Side, StartLine: int32(a.Start), EndLine: int32(a.End),
		Before: pgtype.Timestamptz{Time: a.At, Valid: true},
	})
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: findings on lines: %w", err)
	}
	if len(rows) == 0 {
		return SignalOutcome{Ignored: "no finding on these lines"}, nil
	}
	reader, err := w.cfg.GitHub.Reader(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: github reader: %w", err)
	}
	pr, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: get pull request: %w", err)
	}
	if strings.EqualFold(a.Actor, pr.Author) {
		return SignalOutcome{Ignored: "the PR's author"}, nil // the author raising it is not a reviewer's agreement
	}
	c := engine.HumanComment{
		ID: a.CommentID, Author: a.Actor, AuthorType: "User", Association: a.Association,
		Path: a.Path, Side: a.Side, Start: a.Start, End: a.End, Body: a.Body,
	}
	meta := engine.CallMeta{Caller: Caller, Repo: a.Repo}
	var evs []pipeline.Event
	out := SignalOutcome{Role: pipeline.RoleReviewer}
	for i, r := range rows {
		if i == maxCommentJudgements {
			break
		}
		if !w.cfg.Engine.HumanSameConcern(ctx, meta, findingOf(queries.FindingDetailRow(r)), c) {
			continue
		}
		role := pipeline.RoleReviewer
		if owner, err := isRuleOwner(ctx, w.cfg.GitHub, a.InstallationID, a.Repo, ruleOwner(snap, r.RuleID), a.Actor); err != nil {
			return SignalOutcome{}, err
		} else if owner {
			role = pipeline.RoleOwner
		}
		out.Role = role
		evs = append(evs, pipeline.Event{
			FindingID: r.ID, Kind: pipeline.EventHumanAgreed, Actor: a.Actor, Role: role,
			Via: pipeline.ViaInferred, Evidence: map[string]any{"comment_id": a.CommentID},
		})
		out.Kinds = append(out.Kinds, pipeline.EventHumanAgreed)
	}
	if len(evs) == 0 {
		return SignalOutcome{Ignored: "no finding judged the same concern"}, nil
	}
	if _, err := pipeline.RecordEvents(ctx, w.cfg.Store, snap.Global.Feedback, evs); err != nil {
		return SignalOutcome{}, fmt.Errorf("worker: record events: %w", err)
	}
	return out, nil
}

// role is the actor's role on the PR: the author, the rule's
// owner, a reviewer (with participate, only one who reviewed or commented
// before), or else an observer.
func (w *FeedbackSignalWorker) role(ctx context.Context, a store.FeedbackSignalArgs, snap *config.Snapshot, ruleID string, participate bool) (string, error) {
	reader, err := w.cfg.GitHub.Reader(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return "", fmt.Errorf("worker: github reader: %w", err)
	}
	pr, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return "", fmt.Errorf("worker: get pull request: %w", err)
	}
	if strings.EqualFold(a.Actor, pr.Author) {
		return pipeline.RoleAuthor, nil
	}
	if owner, err := isRuleOwner(ctx, w.cfg.GitHub, a.InstallationID, a.Repo, ruleOwner(snap, ruleID), a.Actor); err != nil {
		return "", err
	} else if owner {
		return pipeline.RoleOwner, nil
	}
	if !participate {
		return pipeline.RoleReviewer, nil
	}
	took, err := gh.Participated(ctx, reader, a.Repo, a.PR, a.Actor, a.At, a.CommentID)
	if err != nil {
		return "", fmt.Errorf("worker: participation: %w", err)
	}
	if took {
		return pipeline.RoleReviewer, nil
	}
	return pipeline.RoleObserver, nil
}

func findingOf(d queries.FindingDetailRow) engine.Finding {
	return engine.Finding{
		ID: d.ID, RuleID: d.RuleID, Lane: d.Lane, Message: d.Message, Fingerprint: d.Fingerprint,
		Anchor: scope.Anchor{Path: d.Path, Side: scope.Side(d.Side), StartLine: int(d.StartLine), EndLine: int(d.EndLine)},
	}
}
