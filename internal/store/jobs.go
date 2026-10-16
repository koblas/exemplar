package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// ReviewArgs is the River job that runs one review. Workers live with the
// code that runs reviews; the args live here so any package can enqueue.
type ReviewArgs struct {
	ReviewID int64 `json:"review_id"`
	// FixtureID names the recorded PR for replay runs; empty for live PRs.
	FixtureID string `json:"fixture_id,omitempty"`
	// RunID distinguishes replay invocations, so replaying a fixture again
	// is a new job while a duplicate enqueue within one run is not.
	RunID string `json:"run_id,omitempty"`
}

// Kind implements river.JobArgs.
func (ReviewArgs) Kind() string { return "review" }

// InsertOpts makes a review job unique per review row, so a duplicate
// enqueue (redelivered webhook, retried replay) is a no-op.
func (ReviewArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// ReviewQueue is the River queue live PR reviews run on.
const ReviewQueue = "reviews"

// PRReviewArgs is the River job that reviews one head of a live PR. The
// webhook (and, until it exists, `exemplar-worker enqueue`) inserts it.
type PRReviewArgs struct {
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"`
	PR             int    `json:"pr"`
	HeadSHA        string `json:"head_sha"`
	// Attempt is reviews.attempt: 1, or more for a re-run of an incomplete
	// review, which must not be deduplicated against the first.
	Attempt int `json:"attempt,omitempty"`
}

// Kind implements river.JobArgs.
func (PRReviewArgs) Kind() string { return "pr_review" }

// InsertOpts makes the job unique per (repo, pr, head, attempt), so a
// redelivered webhook or a sweeper re-enqueue is a no-op.
func (PRReviewArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: ReviewQueue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// EventQueue is the River queue GitHub events are routed on.
const EventQueue = "events"

// GHEventArgs is the River job the webhook receiver inserts, in the same
// transaction as the gh_events row it names. The orchestrator
// turns it into reviews; the receiver itself has no business logic.
type GHEventArgs struct {
	DeliveryID string `json:"delivery_id"`
}

// Kind implements river.JobArgs.
func (GHEventArgs) Kind() string { return "gh_event" }

// InsertOpts makes the job unique per delivery.
func (GHEventArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EventQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// openReviewStates are pr_review job states that may still post.
var openReviewStates = []string{"available", "pending", "retryable", "running", "scheduled"}

// ReviewJobsToSupersede returns the ids of queued or running pr_review
// jobs for repo#pr on any head other than keepHead ("" for every head,
// when the PR closed). River's own table is queried because the job args
// carry the PR identity (superseded reviews are cancelled).
func ReviewJobsToSupersede(ctx context.Context, tx pgx.Tx, repo string, pr int, keepHead string) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM river_job
		WHERE kind = $1 AND state = ANY($2::river_job_state[])
		  AND lower(args->>'repo') = lower($3) AND (args->>'pr')::int = $4 AND args->>'head_sha' <> $5
		ORDER BY id`, PRReviewArgs{}.Kind(), openReviewStates, repo, pr, keepHead)
	if err != nil {
		return nil, fmt.Errorf("store: superseded review jobs: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("store: superseded review jobs: %w", err)
	}
	return ids, nil
}

// LinterQueue is the River queue late-CI linter checks run on.
const LinterQueue = "linters"

// LinterCheckArgs is the River job a completed CI check run enqueues: if
// the check is one of the repo's configured linters, findings already
// posted on the head that it also reports are minimized.
type LinterCheckArgs struct {
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"`
	PR             int    `json:"pr"`
	HeadSHA        string `json:"head_sha"`
	Check          string `json:"check"`
}

// Kind implements river.JobArgs.
func (LinterCheckArgs) Kind() string { return "linter_check" }

// InsertOpts makes the job unique per (repo, pr, head, check), so a
// redelivered event is a no-op.
func (LinterCheckArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: LinterQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// ReviewJobOpen reports whether a pr_review job for repo#pr at head is
// queued or running, so it may still post.
func (s *Store) ReviewJobOpen(ctx context.Context, repo string, pr int, head string) (bool, error) {
	var open bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM river_job
		WHERE kind = $1 AND state = ANY($2::river_job_state[])
		  AND lower(args->>'repo') = lower($3) AND (args->>'pr')::int = $4 AND args->>'head_sha' = $5)`,
		PRReviewArgs{}.Kind(), openReviewStates, repo, pr, head).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("store: open review job: %w", err)
	}
	return open, nil
}

// FeedbackQueue is the River queue feedback commands run on.
const FeedbackQueue = "feedback"

// FeedbackCommandArgs is the River job a `/exemplar <command>` reply in an
// Exemplar review thread enqueues.
type FeedbackCommandArgs struct {
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"`
	PR             int    `json:"pr"`
	// CommentID is the command's comment; ThreadRoot the comment it
	// replies to, which starts the thread.
	CommentID  int64     `json:"comment_id"`
	ThreadRoot int64     `json:"thread_root"`
	Actor      string    `json:"actor"`
	ActorType  string    `json:"actor_type"`
	At         time.Time `json:"at"`
	Command    string    `json:"command"`
	Reason     string    `json:"reason,omitempty"`
}

// Kind implements river.JobArgs.
func (FeedbackCommandArgs) Kind() string { return "feedback_command" }

// InsertOpts makes the job unique per command comment.
func (FeedbackCommandArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: FeedbackQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// AlertDismissalArgs is the River job a user's dismissal of one of
// Exemplar's code-scanning alerts on a PR enqueues: the
// dismissal reason becomes a feedback event on the finding.
type AlertDismissalArgs struct {
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"`
	PR             int    `json:"pr"`
	Alert          int    `json:"alert"`
	RuleID         string `json:"rule_id"`
	Path           string `json:"path"`
	StartLine      int    `json:"start_line"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment,omitempty"`
	Actor          string `json:"actor"`
}

// Kind implements river.JobArgs.
func (AlertDismissalArgs) Kind() string { return "alert_dismissal" }

// InsertOpts makes the job unique per dismissal.
func (AlertDismissalArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: FeedbackQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// EvaluateQueue is the River queue `exemplar review` evaluations run on,
// apart from PR reviews so a burst of CLI calls never delays them.
const EvaluateQueue = "evaluate"

// EvaluateArgs runs one evaluation: the evaluations row
// holds the request.
type EvaluateArgs struct {
	ID string `json:"id"`
}

// Kind implements river.JobArgs.
func (EvaluateArgs) Kind() string { return "evaluate" }

// InsertOpts makes the job unique per evaluation; one retry covers a
// worker restart.
func (EvaluateArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EvaluateQueue, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// PlaygroundArgs runs one web UI playground try; the
// playground_runs row holds the request.
type PlaygroundArgs struct {
	ID int64 `json:"id"`
}

// Kind implements river.JobArgs.
func (PlaygroundArgs) Kind() string { return "playground_run" }

// InsertOpts runs tries beside `exemplar review` evaluations, apart from
// PR reviews, once per run.
func (PlaygroundArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EvaluateQueue, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// OwnerActionArgs carries out one owner action from the web UI; the
// owner_actions row holds the request.
type OwnerActionArgs struct {
	ID int64 `json:"id"`
}

// Kind implements river.JobArgs.
func (OwnerActionArgs) Kind() string { return "owner_action" }

// InsertOpts makes the job unique per action.
func (OwnerActionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: FeedbackQueue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// MergePolicyArgs re-decides exemplar/merge-policy on a PR whose reviews
// changed (merge_policy_check repos). Review and Action make
// each event its own job.
type MergePolicyArgs struct {
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"`
	PR             int    `json:"pr"`
	Review         int64  `json:"review"`
	Action         string `json:"action"`
}

// Kind implements river.JobArgs.
func (MergePolicyArgs) Kind() string { return "merge_policy" }

// InsertOpts makes the job unique per review event.
func (MergePolicyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: FeedbackQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// Feedback signals seen in webhooks, handled by the feedback
// queue's signal worker.
const (
	SignalResolved = "resolved" // an Exemplar thread was resolved
	SignalReply    = "reply"    // a reply in an Exemplar thread that is not a command
	SignalComment  = "comment"  // a new review comment, perhaps raising a finding's concern
	SignalMerged   = "merged"   // the PR merged: untouched findings were ignored
)

// FeedbackSignalArgs is one webhook-observed feedback signal on a PR.
type FeedbackSignalArgs struct {
	InstallationID int64     `json:"installation_id"`
	Repo           string    `json:"repo"`
	PR             int       `json:"pr"`
	Signal         string    `json:"signal"`
	CommentID      int64     `json:"comment_id,omitempty"`
	ThreadRoot     int64     `json:"thread_root,omitempty"`
	Actor          string    `json:"actor"`
	Association    string    `json:"association,omitempty"`
	At             time.Time `json:"at"`
	Path           string    `json:"path,omitempty"`
	Side           string    `json:"side,omitempty"`
	Start          int       `json:"start,omitempty"`
	End            int       `json:"end,omitempty"`
	Body           string    `json:"body,omitempty"`
	// HeadSHA is the merged head, for SignalMerged.
	HeadSHA string `json:"head_sha,omitempty"`
}

// Kind implements river.JobArgs.
func (FeedbackSignalArgs) Kind() string { return "feedback_signal" }

// InsertOpts makes the job unique per signal.
func (FeedbackSignalArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: FeedbackQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// FeedbackLinkArgs is the River job exemplar-api enqueues when someone
// signed in through GitHub confirms a feedback link.
// The worker derives their role and records the event; exemplar-api holds
// no installation credentials.
type FeedbackLinkArgs struct {
	FindingID int64     `json:"finding_id"`
	Reason    string    `json:"reason"` // fixed | wrong | not_important | handled | disagree
	Actor     string    `json:"actor"`  // GitHub login, from OAuth
	At        time.Time `json:"at"`
	Text      string    `json:"text,omitempty"`
}

// Kind implements river.JobArgs.
func (FeedbackLinkArgs) Kind() string { return "feedback_link" }

// InsertOpts runs it on the feedback queue; a double submit is one job.
func (FeedbackLinkArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: FeedbackQueue, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}
