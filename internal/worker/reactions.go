package worker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/riverqueue/river"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Reactions (spec §9.1): 👍 and 👎 on Exemplar's comments are secondary
// signals, polled every 30 minutes for PRs with recent reviews, and once
// more when a PR merges. A 👎 rejects for acceptance at weight 0.5 but is
// outside eFP; a 👍 is reported and counts for nothing (§9.2). The bot's
// own reactions are excluded (FR-T4). A reaction removed later stays
// recorded.

// ReactionsEvery is how often reactions are polled.
const ReactionsEvery = 30 * time.Minute

// reactionKinds maps GitHub reaction contents to feedback kinds.
var reactionKinds = map[string]string{"THUMBS_UP": "react_up", "THUMBS_DOWN": "react_down"}

// ReactionsArgs is the periodic reactions poll.
type ReactionsArgs struct{}

// Kind implements river.JobArgs.
func (ReactionsArgs) Kind() string { return "poll_reactions" }

// InsertOpts runs it on the maintenance queue, once a period.
func (ReactionsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: MaintenanceQueue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: ReactionsEvery}}
}

// ReactionsWorker polls reactions.
type ReactionsWorker struct {
	river.WorkerDefaults[ReactionsArgs]

	cfg Config
}

// NewReactionsWorker validates cfg; it needs GitHub, Store and Snapshot.
func NewReactionsWorker(cfg Config) (*ReactionsWorker, error) {
	if cfg.Store == nil {
		return nil, errors.New("worker: reactions need a store")
	}
	w, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &ReactionsWorker{cfg: w.cfg}, nil
}

// Work implements river.Worker.
func (w *ReactionsWorker) Work(ctx context.Context, _ *river.Job[ReactionsArgs]) error {
	prs, events, err := w.Run(ctx)
	w.cfg.Log.Info("reactions polled", "prs", prs, "events", events, "err", err)
	return err
}

// Run polls every PR reviewed in the last 24 hours. One PR's failure does
// not stop the rest.
func (w *ReactionsWorker) Run(ctx context.Context) (int, int, error) {
	snap, err := w.cfg.Snapshot(ctx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := w.cfg.Store.PRsToPollReactions(ctx, 24)
	if err != nil {
		return 0, 0, err
	}
	var prs, events int
	var errs []error
	for _, r := range rows {
		n, err := pollReactions(ctx, w.cfg, snap, r.InstallationID, r.Repo, int(r.PrNumber))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		prs++
		events += n
	}
	return prs, events, errors.Join(errs...)
}

// pollReactions records the reactions on one PR's Exemplar comments.
func pollReactions(ctx context.Context, cfg Config, snap *config.Snapshot, inst int64, repo string, pr int) (int, error) {
	findings, err := cfg.Store.CommentedFindings(ctx, queries.CommentedFindingsParams{Repo: repo, PrNumber: int32(pr)})
	if err != nil || len(findings) == 0 {
		return 0, err
	}
	reader, err := cfg.GitHub.Reader(ctx, inst, repo)
	if err != nil {
		return 0, err
	}
	byComment, err := gh.CommentReactions(ctx, reader, repo, pr)
	if err != nil {
		return 0, err
	}
	p, err := gh.GetPullRequest(ctx, reader, repo, pr)
	if err != nil {
		return 0, err
	}
	weight := pipeline.ReactionWeight(snap.Global.Feedback)
	zero := float32(0)
	roles := map[string]string{}
	var evs []pipeline.Event
	for _, f := range findings {
		for _, r := range byComment[f.GhCommentID] {
			kind := reactionKinds[r.Content]
			if kind == "" || r.Bot {
				continue
			}
			role, ok := roles[r.Login]
			if !ok {
				if role, err = reactorRole(ctx, cfg, snap, inst, reader, repo, pr, p.Author, f.RuleID, r.Login); err != nil {
					return 0, err
				}
				roles[r.Login] = role
			}
			w := &weight
			if role == pipeline.RoleObserver {
				w = &zero
			}
			evs = append(evs, pipeline.Event{FindingID: f.ID, Kind: kind, Actor: r.Login, Role: role, Via: pipeline.ViaReaction, Weight: w})
		}
	}
	return pipeline.RecordEvents(ctx, cfg.Store, snap.Global.Feedback, evs)
}

// reactorRole is a reactor's role (spec §9.1): the author, the rule's
// owner, someone who took part in the PR, or else an observer.
func reactorRole(ctx context.Context, cfg Config, snap *config.Snapshot, inst int64, reader *github.Client, repo string, pr int, author, ruleID, login string) (string, error) {
	if strings.EqualFold(login, author) {
		return pipeline.RoleAuthor, nil
	}
	if owner, err := isRuleOwner(ctx, cfg.GitHub, inst, repo, ruleOwner(snap, ruleID), login); err != nil || owner {
		return pipeline.RoleOwner, err
	}
	took, err := gh.Participated(ctx, reader, repo, pr, login, time.Now(), 0)
	if err != nil {
		return "", err
	}
	if took {
		return pipeline.RoleReviewer, nil
	}
	return pipeline.RoleObserver, nil
}
