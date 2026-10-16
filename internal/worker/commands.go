package worker

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Feedback slash commands: a reply "/exemplar <command>
// [reason]" in an Exemplar review thread records a feedback event on its
// finding. Commands are honored from the PR's author, a member of the
// rule's owning team, or a human who reviewed or commented on the PR
// before the command; anyone else gets a one-line reply and no event.
// uphold and overturn are owner adjudications: they need membership in the
// owning team, checked live, and are refused to the PR's author.

// commandKinds maps commands to feedback_events kinds.
var commandKinds = map[string]string{
	"fixed":         "fb_fixed",
	"wrong":         "fb_wrong",
	"not-important": "fb_not_important",
	"not_important": "fb_not_important",
	"handled":       "fb_handled",
	"disagree":      "fb_disagree",
	"uphold":        "adjudicated_uphold",
	"overturn":      "adjudicated_overturn",
}

func adjudication(kind string) bool { return strings.HasPrefix(kind, "adjudicated_") }

// Replies to commands that record nothing.
const (
	replyUnknown    = "Exemplar didn't recognize that command. Use `/exemplar fixed`, `wrong <reason>`, `not-important`, `handled` or `disagree`; rule owners can also `uphold` or `overturn`."
	replyNotAllowed = "Only this pull request's author, its reviewers and the rule's owners can give Exemplar feedback, so this wasn't recorded."
	replyNotOwner   = "Only members of the rule's owning team, other than this pull request's author, can uphold or overturn a finding, so this wasn't recorded."
)

// FeedbackCommandWorker runs feedback_command jobs.
type FeedbackCommandWorker struct {
	river.WorkerDefaults[store.FeedbackCommandArgs]

	cfg Config
}

// NewFeedbackCommandWorker validates cfg; it needs GitHub, Store and
// Snapshot.
func NewFeedbackCommandWorker(cfg Config) (*FeedbackCommandWorker, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: feedback commands need a store", errMissingDependency)
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &FeedbackCommandWorker{cfg: w.cfg}, nil
}

// CommandOutcome is what one command did, for logs and tests.
type CommandOutcome struct {
	Ignored  string // nothing done, and why
	Recorded string // the event kind recorded
	Role     string
	Replied  string // the reply posted instead of an event
}

// Work implements river.Worker.
func (w *FeedbackCommandWorker) Work(ctx context.Context, job *river.Job[store.FeedbackCommandArgs]) error {
	a := job.Args
	out, err := w.Handle(ctx, a)
	if err != nil {
		return err
	}
	w.cfg.Log.Info("feedback command", "repo", a.Repo, "pr", a.PR, "command", a.Command,
		"recorded", out.Recorded, "role", out.Role, "replied", out.Replied != "", "ignored", out.Ignored)
	return nil
}

// Handle runs one command.
func (w *FeedbackCommandWorker) Handle(ctx context.Context, a store.FeedbackCommandArgs) (CommandOutcome, error) {
	rows, err := w.cfg.Store.FindingByComment(ctx, queries.FindingByCommentParams{Repo: a.Repo, PrNumber: int32(a.PR), GhCommentID: &a.ThreadRoot})
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: finding by comment: %w", err)
	}
	if len(rows) == 0 {
		// Not an Exemplar thread: say nothing in someone else's thread.
		return CommandOutcome{Ignored: "not an Exemplar thread"}, nil
	}
	finding := rows[0]
	reader, err := w.cfg.GitHub.Reader(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: reader client: %w", err)
	}
	pr, err := gh.GetPullRequest(ctx, reader, a.Repo, a.PR)
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: get pull request: %w", err)
	}
	target, err := gh.ParsePR(a.Repo, a.PR, pr.HeadSHA)
	if err != nil {
		return CommandOutcome{}, river.JobCancel(fmt.Errorf("worker: parse pr: %w", err))
	}
	posterClient, err := w.cfg.GitHub.Poster(ctx, a.InstallationID, a.Repo)
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: poster client: %w", err)
	}
	poster := gh.NewPoster(posterClient)
	reply := func(msg string) (CommandOutcome, error) {
		return CommandOutcome{Replied: msg}, poster.Reply(ctx, target, a.ThreadRoot, msg)
	}

	kind, ok := commandKinds[a.Command]
	if !ok {
		return reply(replyUnknown)
	}
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: config: %w", err)
	}
	isAuthor := strings.EqualFold(a.Actor, pr.Author)
	isOwner, err := isRuleOwner(ctx, w.cfg.GitHub, a.InstallationID, a.Repo, ruleOwner(snap, finding.RuleID), a.Actor)
	if err != nil {
		return CommandOutcome{}, err
	}
	var role string
	switch {
	case adjudication(kind):
		if !isOwner || isAuthor {
			return reply(replyNotOwner)
		}
		role = pipeline.RoleOwner
	case isAuthor:
		// An author on the owning team still weighs as the author.
		role = pipeline.RoleAuthor
	case isOwner:
		role = pipeline.RoleOwner
	default:
		took, err := gh.Participated(ctx, reader, a.Repo, a.PR, a.Actor, a.At, a.CommentID)
		if err != nil {
			return CommandOutcome{}, fmt.Errorf("worker: check participation: %w", err)
		}
		if !took {
			return reply(replyNotAllowed)
		}
		role = pipeline.RoleReviewer
	}
	n, err := pipeline.RecordEvents(ctx, w.cfg.Store, snap.Global.Feedback, []pipeline.Event{{
		FindingID: finding.ID, Kind: kind, Actor: a.Actor, Role: role, Via: pipeline.ViaSlash, ReasonText: a.Reason,
		Evidence: map[string]any{"comment_id": a.CommentID},
	}})
	if err != nil {
		return CommandOutcome{}, fmt.Errorf("worker: record events: %w", err)
	}
	if n > 0 { // a repeat of the same command keeps the first reason's class
		teach(ctx, w.cfg, a.Repo, finding.ID, kind, a.Actor, a.Reason)
	}
	// Acknowledge with a reaction rather than another comment.
	if err := poster.React(ctx, target, a.CommentID, "+1"); err != nil {
		w.cfg.Log.Warn("feedback recorded without a reaction", "repo", a.Repo, "pr", a.PR, "err", err)
	}
	return CommandOutcome{Recorded: kind, Role: role}, nil
}

// isRuleOwner reports whether actor owns a rule: the owning user, or an
// active member of the owning team (gate token, Members: read).
func isRuleOwner(ctx context.Context, clients GitHub, inst int64, repo, owner, actor string) (bool, error) {
	switch {
	case owner == "":
		return false, nil // the secret-in-diff finding, or a rule no longer in config
	case !strings.Contains(owner, "/"):
		return strings.EqualFold(strings.TrimPrefix(owner, "@"), actor), nil
	}
	gate, err := clients.Gate(ctx, inst, repo)
	if err != nil {
		return false, fmt.Errorf("worker: gate client: %w", err)
	}
	member, err := gh.TeamMember(ctx, gate, owner, actor)
	if err != nil {
		return false, fmt.Errorf("worker: team member: %w", err)
	}
	return member, nil
}

// rejections are the kinds whose free text teaches the bot.
var rejections = []string{"fb_wrong", "fb_not_important", "fb_handled", "fb_disagree"}

// teach classifies a rejection's reason for teach-the-bot and stores the
// class on its event. A failure loses a hint, not the feedback.
func teach(ctx context.Context, cfg Config, repo string, findingID int64, kind, actor, reason string) {
	if strings.TrimSpace(reason) == "" || !slices.Contains(rejections, kind) {
		return
	}
	d, err := cfg.Store.FindingDetail(ctx, findingID)
	if err != nil {
		cfg.Log.Warn("teach-the-bot: finding not read", "finding", findingID, "err", err)
		return
	}
	class := cfg.Engine.ClassifyTeach(ctx, engine.CallMeta{Caller: Caller, Repo: repo}, findingOf(d), reason)
	if class == "" {
		return
	}
	if err := cfg.Store.SetTeachClass(ctx, queries.SetTeachClassParams{FindingID: findingID, Kind: kind, Actor: actor, TeachClass: &class}); err != nil {
		cfg.Log.Warn("teach-the-bot: class not stored", "finding", findingID, "err", err)
	}
}

func ruleOwner(snap *config.Snapshot, ruleID string) string {
	for _, r := range snap.Rules {
		if r.ID == ruleID {
			return r.Owner
		}
	}
	return ""
}
