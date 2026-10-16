package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// reviewIncomplete is reviews.status for an incomplete review.
const reviewIncomplete = "incomplete"

// Route is what one GitHub event asks for.
type Route struct {
	// Reviews to enqueue. Attempt is set by the router for re-runs.
	Reviews []store.PRReviewArgs
	// ReRun marks Reviews as re-runs of an incomplete review: each gets the
	// head's next attempt.
	ReRun bool
	// Supersede, when set, cancels the PR's queued and running reviews of
	// other heads.
	Supersede *Supersede
	// LinterChecks are late-CI linter checks to enqueue.
	LinterChecks []store.LinterCheckArgs
	// FeedbackCommands are `/exemplar` replies to handle.
	FeedbackCommands []store.FeedbackCommandArgs
	// AlertDismissals are dismissed Exemplar code-scanning alerts.
	AlertDismissals []store.AlertDismissalArgs
	// Signals are other feedback signals.
	Signals []store.FeedbackSignalArgs
	// MergePolicy re-decides exemplar/merge-policy after a review changed.
	MergePolicy []store.MergePolicyArgs
	// Ignored says why nothing is enqueued.
	Ignored string
}

// Supersede names the reviews an event makes obsolete: every pr_review of
// Repo#PR whose head is not KeepHead ("" when the PR closed: all of them).
type Supersede struct {
	Repo     string
	PR       int
	KeepHead string
}

// prEvent is the part of a pull_request payload the router reads.
type prEvent struct {
	Action       string `json:"action"`
	Number       int    `json:"number"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		State    string    `json:"state"`
		Merged   bool      `json:"merged"`
		MergedAt time.Time `json:"merged_at"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Changes struct {
		Base json.RawMessage `json:"base"`
	} `json:"changes"`
}

// checkRunEvent is the part of a check_run payload the router reads.
type checkRunEvent struct {
	Action          string `json:"action"`
	RequestedAction struct {
		Identifier string `json:"identifier"`
	} `json:"requested_action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	CheckRun struct {
		Name         string `json:"name"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
}

// reviewCommentEvent is the part of a pull_request_review_comment payload
// the router reads.
type reviewCommentEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Comment struct {
		ID          int64     `json:"id"`
		InReplyTo   int64     `json:"in_reply_to_id"`
		Body        string    `json:"body"`
		CreatedAt   time.Time `json:"created_at"`
		Association string    `json:"author_association"`
		Path        string    `json:"path"`
		Side        string    `json:"side"`
		Line        int       `json:"line"`
		StartLine   int       `json:"start_line"`
		User        struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
}

// threadEvent is the part of a pull_request_review_thread payload the
// router reads.
type threadEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Thread struct {
		Comments []struct {
			ID        int64 `json:"id"`
			InReplyTo int64 `json:"in_reply_to_id"`
		} `json:"comments"`
	} `json:"thread"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
}

// reviewEvent is the part of a pull_request_review payload the router
// reads.
type reviewEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	} `json:"pull_request"`
	Review struct {
		ID   int64 `json:"id"`
		User struct {
			Type string `json:"type"`
		} `json:"user"`
	} `json:"review"`
}

// maxSignalBody bounds the comment text a signal job carries.
const maxSignalBody = 2000

// alertEvent is the part of a code_scanning_alert payload the router reads.
type alertEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Alert struct {
		Number           int    `json:"number"`
		DismissedReason  string `json:"dismissed_reason"`
		DismissedComment string `json:"dismissed_comment"`
		DismissedBy      struct {
			Login string `json:"login"`
		} `json:"dismissed_by"`
		Rule struct {
			ID string `json:"id"`
		} `json:"rule"`
		Tool struct {
			Name string `json:"name"`
		} `json:"tool"`
		Instance struct {
			Ref      string `json:"ref"`
			Location struct {
				Path      string `json:"path"`
				StartLine int    `json:"start_line"`
			} `json:"location"`
		} `json:"most_recent_instance"`
	} `json:"alert"`
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// maxReasonChars bounds the free text stored with a command.
const maxReasonChars = 1000

// ParseCommand reads a "/exemplar <command> [reason]" comment: the first
// non-blank line must start with /exemplar. command is lower-cased; reason
// is the rest of the comment, trimmed and capped.
func ParseCommand(body string) (string, string, bool) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	first, rest, _ := strings.Cut(body, "\n")
	fields := strings.Fields(first)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/exemplar") {
		return "", "", false
	}
	var command, reason string
	if len(fields) > 1 {
		command = strings.ToLower(fields[1])
		reason = strings.Join(fields[2:], " ")
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		reason = strings.TrimSpace(reason + "\n" + rest)
	}
	if r := []rune(reason); len(r) > maxReasonChars {
		reason = string(r[:maxReasonChars])
	}
	return command, reason, true
}

// RouteEvent decides what a stored event asks for. It reads
// only the payload: GitHub is not called.
//   - pull_request opened, reopened, synchronize, ready_for_review, and an
//     edit that changed the base: review the head, and supersede reviews of
//     the PR's other heads. Drafts are enqueued too; the review decides
//     from review_drafts.
//   - pull_request closed: supersede all of the PR's reviews.
//   - check_run rerequested, or its Re-run action, on Exemplar's check:
//     re-run the head's review as the next attempt.
//   - check_run completed on any other check of a PR: a late-CI linter
//     check. The job reads config to decide whether the
//     check is one of the repo's linters.
//   - pull_request_review_comment created, a reply starting with
//     "/exemplar": a feedback command. The job decides whether
//     the thread is Exemplar's and the commenter may give feedback.
//   - code_scanning_alert closed_by_user on one of Exemplar's alerts on a
//     PR ref: a dismissal to record as feedback.
//   - pull_request_review submitted, edited or dismissed by a person on an
//     open PR: re-decide exemplar/merge-policy. The job ignores repos not
//     in merge_policy_check mode.
//   - everything else is recorded and ignored for now.
func RouteEvent(event string, payload []byte) (Route, error) {
	if payload == nil {
		return Route{Ignored: "payload expired"}, nil
	}
	switch event {
	case "pull_request":
		var e prEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return Route{}, fmt.Errorf("pull_request payload: %w", err)
		}
		switch e.Action {
		case "opened", "reopened", "synchronize", "ready_for_review":
		case "closed":
			if e.Repository.FullName == "" || e.Number < 1 {
				return Route{}, fmt.Errorf("pull_request payload %w repository or number", errPayloadLacks)
			}
			r := Route{Supersede: &Supersede{Repo: e.Repository.FullName, PR: e.Number}}
			if e.PullRequest.Merged {
				// Findings still untouched at merge were ignored.
				r.Signals = []store.FeedbackSignalArgs{{
					InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: e.Number,
					Signal: store.SignalMerged, Actor: e.PullRequest.User.Login, HeadSHA: e.PullRequest.Head.SHA, At: e.PullRequest.MergedAt,
				}}
			}
			return r, nil
		case "edited":
			if len(e.Changes.Base) == 0 {
				return Route{Ignored: "edited without a base change"}, nil
			}
		default:
			return Route{Ignored: "pull_request " + e.Action}, nil
		}
		if e.PullRequest.State != "open" {
			return Route{Ignored: "pull request is " + e.PullRequest.State}, nil
		}
		a := store.PRReviewArgs{InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: e.Number, HeadSHA: e.PullRequest.Head.SHA}
		if a.InstallationID == 0 || a.Repo == "" || a.PR < 1 || a.HeadSHA == "" {
			return Route{}, fmt.Errorf("pull_request payload %w installation, repository, number or head", errPayloadLacks)
		}
		return Route{Reviews: []store.PRReviewArgs{a}, Supersede: &Supersede{Repo: a.Repo, PR: a.PR, KeepHead: a.HeadSHA}}, nil
	case "pull_request_review_comment":
		var e reviewCommentEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return Route{}, fmt.Errorf("pull_request_review_comment payload: %w", err)
		}
		c := e.Comment
		cmd, reason, ok := ParseCommand(c.Body)
		switch {
		case e.Action != "created":
			return Route{Ignored: "review comment " + e.Action}, nil
		case !strings.EqualFold(c.User.Type, "User"):
			return Route{Ignored: "review comment from a " + c.User.Type}, nil
		case !ok:
			// Not a command: a reply in a thread, or a new comment that may
			// raise a finding's concern.
			sig := store.FeedbackSignalArgs{
				InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: e.PullRequest.Number,
				Signal: store.SignalReply, CommentID: c.ID, ThreadRoot: c.InReplyTo, Actor: c.User.Login, Association: c.Association,
				At: c.CreatedAt, Body: truncate(c.Body, maxSignalBody),
			}
			if c.InReplyTo == 0 {
				start := c.StartLine
				if start == 0 {
					start = c.Line
				}
				sig.Signal, sig.Path, sig.Side, sig.Start, sig.End = store.SignalComment, c.Path, c.Side, start, c.Line
				if c.Line == 0 {
					return Route{Ignored: "review comment on no line"}, nil
				}
			}
			return Route{Signals: []store.FeedbackSignalArgs{sig}}, nil
		case c.InReplyTo == 0:
			return Route{Ignored: "command outside a thread"}, nil
		}
		a := store.FeedbackCommandArgs{
			InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: e.PullRequest.Number,
			CommentID: c.ID, ThreadRoot: c.InReplyTo, Actor: c.User.Login, ActorType: c.User.Type, At: c.CreatedAt, Command: cmd, Reason: reason,
		}
		if a.InstallationID == 0 || a.Repo == "" || a.PR < 1 || a.CommentID == 0 || a.Actor == "" {
			return Route{}, fmt.Errorf("pull_request_review_comment payload %w installation, repository, number, comment or user", errPayloadLacks)
		}
		return Route{FeedbackCommands: []store.FeedbackCommandArgs{a}}, nil
	case "pull_request_review":
		var e reviewEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return Route{}, fmt.Errorf("pull_request_review payload: %w", err)
		}
		switch {
		case e.Action != "submitted" && e.Action != "edited" && e.Action != "dismissed":
			return Route{Ignored: "review " + e.Action}, nil
		case !strings.EqualFold(e.Review.User.Type, "User") && e.Action != "dismissed":
			return Route{Ignored: "review by a " + e.Review.User.Type}, nil
		case e.PullRequest.State != "open":
			return Route{Ignored: "pull request is " + e.PullRequest.State}, nil
		}
		a := store.MergePolicyArgs{InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: e.PullRequest.Number, Review: e.Review.ID, Action: e.Action}
		if a.InstallationID == 0 || a.Repo == "" || a.PR < 1 {
			return Route{}, fmt.Errorf("pull_request_review payload %w installation, repository or number", errPayloadLacks)
		}
		return Route{MergePolicy: []store.MergePolicyArgs{a}}, nil
	case "pull_request_review_thread":
		var e threadEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return Route{}, fmt.Errorf("pull_request_review_thread payload: %w", err)
		}
		var root int64
		for _, c := range e.Thread.Comments {
			if c.InReplyTo == 0 {
				root = c.ID
				break
			}
		}
		switch {
		case e.Action != "resolved":
			return Route{Ignored: "review thread " + e.Action}, nil
		case !strings.EqualFold(e.Sender.Type, "User"):
			return Route{Ignored: "thread resolved by a " + e.Sender.Type}, nil // Exemplar resolves its own when settled
		case root == 0:
			return Route{Ignored: "thread without a first comment"}, nil
		}
		return Route{Signals: []store.FeedbackSignalArgs{{
			InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: e.PullRequest.Number,
			Signal: store.SignalResolved, ThreadRoot: root, Actor: e.Sender.Login,
		}}}, nil
	case "code_scanning_alert":
		var e alertEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return Route{}, fmt.Errorf("code_scanning_alert payload: %w", err)
		}
		al := e.Alert
		var pr int
		_, scanErr := fmt.Sscanf(al.Instance.Ref, "refs/pull/%d/", &pr)
		switch {
		case e.Action != "closed_by_user":
			return Route{Ignored: "code scanning alert " + e.Action}, nil
		case al.Tool.Name != pipeline.SARIFTool:
			return Route{Ignored: "another tool's alert"}, nil
		case scanErr != nil || pr < 1:
			return Route{Ignored: "alert not on a pull request"}, nil //nolint:nilerr // an unparsable ref means the alert is not on a PR: ignored, not an error
		}
		a := store.AlertDismissalArgs{
			InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: pr, Alert: al.Number,
			RuleID: al.Rule.ID, Path: al.Instance.Location.Path, StartLine: al.Instance.Location.StartLine,
			Reason: al.DismissedReason, Comment: truncate(al.DismissedComment, maxReasonChars), Actor: al.DismissedBy.Login,
		}
		if a.InstallationID == 0 || a.Repo == "" || a.Actor == "" || a.RuleID == "" || a.Path == "" {
			return Route{}, fmt.Errorf("code_scanning_alert payload %w installation, repository, dismisser, rule or location", errPayloadLacks)
		}
		return Route{AlertDismissals: []store.AlertDismissalArgs{a}}, nil
	case "check_run":
		var e checkRunEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return Route{}, fmt.Errorf("check_run payload: %w", err)
		}
		if e.Action == "completed" && e.CheckRun.Name != gh.CheckName {
			var r Route
			for _, pr := range e.CheckRun.PullRequests {
				r.LinterChecks = append(r.LinterChecks, store.LinterCheckArgs{
					InstallationID: e.Installation.ID, Repo: e.Repository.FullName,
					PR: pr.Number, HeadSHA: e.CheckRun.HeadSHA, Check: e.CheckRun.Name,
				})
			}
			if len(r.LinterChecks) == 0 {
				return Route{Ignored: "check run has no pull request"}, nil
			}
			if e.Installation.ID == 0 || e.Repository.FullName == "" || e.CheckRun.HeadSHA == "" || e.CheckRun.Name == "" {
				return Route{}, fmt.Errorf("check_run payload %w installation, repository, head or name", errPayloadLacks)
			}
			return r, nil
		}
		rerun := e.Action == "rerequested" || (e.Action == "requested_action" && e.RequestedAction.Identifier == gh.RerunAction)
		if !rerun || e.CheckRun.Name != gh.CheckName {
			return Route{Ignored: "check_run " + e.Action}, nil
		}
		var r Route
		for _, pr := range e.CheckRun.PullRequests {
			r.Reviews = append(r.Reviews, store.PRReviewArgs{InstallationID: e.Installation.ID, Repo: e.Repository.FullName, PR: pr.Number, HeadSHA: e.CheckRun.HeadSHA})
		}
		if len(r.Reviews) == 0 {
			return Route{Ignored: "check run has no pull request"}, nil
		}
		r.ReRun = true
		return r, nil
	}
	return Route{Ignored: event + " is not handled yet"}, nil
}

// EventWorker is the orchestrator's gh_event job: it routes a stored event
// and enqueues its reviews in the same transaction that marks the event
// processed, so a retry never enqueues twice.
type EventWorker struct {
	river.WorkerDefaults[store.GHEventArgs]

	st  *store.Store
	log *slog.Logger
}

// NewEventWorker returns an EventWorker.
func NewEventWorker(st *store.Store, log *slog.Logger) (*EventWorker, error) {
	if st == nil {
		return nil, fmt.Errorf("%w: events need a store", errMissingDependency)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &EventWorker{st: st, log: log}, nil
}

// Work implements river.Worker.
func (w *EventWorker) Work(ctx context.Context, job *river.Job[store.GHEventArgs]) error {
	id, err := uuid.Parse(job.Args.DeliveryID)
	if err != nil {
		return river.JobCancel(fmt.Errorf("delivery id %q: %w", job.Args.DeliveryID, err))
	}
	ev, err := w.st.GetGHEvent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(fmt.Errorf("delivery %s is %w", id, errNotStored))
	}
	if err != nil {
		return fmt.Errorf("get event %s: %w", id, err)
	}
	if ev.ProcessedAt.Valid {
		return nil
	}
	route, err := RouteEvent(ev.Event, ev.Payload)
	if err != nil {
		// A payload GitHub sent that we cannot read will not improve.
		w.log.Error("event not routable", "delivery", id, "event", ev.Event, "err", err)
		return river.JobCancel(err)
	}
	client := river.ClientFromContext[pgx.Tx](ctx)
	cancelled := 0
	err = w.st.InTx(ctx, func(tx pgx.Tx, q *queries.Queries) error {
		// Cancel first, in the same transaction as the new review's insert:
		// River cancels a running job's context on whichever pod holds it.
		if s := route.Supersede; s != nil {
			ids, err := store.ReviewJobsToSupersede(ctx, tx, s.Repo, s.PR, s.KeepHead)
			if err != nil {
				return fmt.Errorf("find superseded review jobs: %w", err)
			}
			for _, id := range ids {
				if _, err := client.JobCancelTx(ctx, tx, id); err != nil {
					return fmt.Errorf("cancel superseded review job %d: %w", id, err)
				}
			}
			cancelled = len(ids)
		}
		for _, a := range route.Reviews {
			if route.ReRun {
				rows, err := q.LatestReviewAttempt(ctx, queries.LatestReviewAttemptParams{Repo: a.Repo, PrNumber: int32(a.PR), HeadSha: a.HeadSHA})
				if err != nil {
					return fmt.Errorf("latest review attempt: %w", err)
				}
				// Re-run only an incomplete review; re-running a complete
				// one would post its comments again. No review yet: the
				// first attempt.
				switch {
				case len(rows) == 0:
					a.Attempt = 1
				case rows[0].Status == reviewIncomplete:
					a.Attempt = int(rows[0].Attempt) + 1
				default:
					w.log.Info("re-run ignored: the review completed", "repo", a.Repo, "pr", a.PR, "head", a.HeadSHA)
					continue
				}
			}
			if _, err := client.InsertTx(ctx, tx, a, nil); err != nil {
				return fmt.Errorf("enqueue review: %w", err)
			}
		}
		for _, a := range route.FeedbackCommands {
			if _, err := client.InsertTx(ctx, tx, a, nil); err != nil {
				return fmt.Errorf("enqueue feedback command: %w", err)
			}
		}
		for _, a := range route.Signals {
			if _, err := client.InsertTx(ctx, tx, a, nil); err != nil {
				return fmt.Errorf("enqueue feedback signal: %w", err)
			}
		}
		for _, a := range route.AlertDismissals {
			if _, err := client.InsertTx(ctx, tx, a, nil); err != nil {
				return fmt.Errorf("enqueue alert dismissal: %w", err)
			}
		}
		for _, a := range route.LinterChecks {
			if _, err := client.InsertTx(ctx, tx, a, nil); err != nil {
				return fmt.Errorf("enqueue linter check: %w", err)
			}
		}
		for _, a := range route.MergePolicy {
			if _, err := client.InsertTx(ctx, tx, a, nil); err != nil {
				return fmt.Errorf("enqueue merge policy: %w", err)
			}
		}
		if err := q.MarkGHEventProcessed(ctx, id); err != nil {
			return fmt.Errorf("mark event processed: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("route event %s: %w", id, err)
	}
	w.log.Info("event routed", "delivery", id, "event", ev.Event,
		"reviews", len(route.Reviews), "linter_checks", len(route.LinterChecks),
		"feedback_commands", len(route.FeedbackCommands), "alert_dismissals", len(route.AlertDismissals),
		"signals", len(route.Signals), "merge_policy", len(route.MergePolicy),
		"superseded", cancelled, "ignored", route.Ignored)
	return nil
}
