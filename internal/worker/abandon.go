package worker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Abandoner closes out a review whose job River will not retry (it failed
// its last attempt). Without it the head stays unreviewed: the sweeper
// skips heads that have a review, Re-run only re-runs an incomplete
// review, and a redelivery is deduplicated. The review is recorded
// incomplete and its check run completed with the Re-run action, so a
// person decides whether to try again; a PR that kills its runner every
// time is not retried on its own.
type Abandoner struct {
	Store  *store.Store
	GitHub GitHub
	Log    *slog.Logger
}

// abandonedReason is the incomplete reason of an abandoned review.
const abandonedReason = "the review runner failed on every attempt"

// Abandon closes out a's review after its job's last failed attempt
// (cause). A head with no review row is the sweeper's, and a superseded
// review needs nothing. Failures are logged: River has given up on the job.
func (ab Abandoner) Abandon(ctx context.Context, a store.PRReviewArgs, cause error) {
	log := ab.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	reason := abandonedReason
	id, err := ab.Store.AbandonReview(ctx, queries.AbandonReviewParams{
		Repo: a.Repo, PrNumber: int32(a.PR), HeadSha: a.HeadSHA, Attempt: int32(max(a.Attempt, 1)), IncompleteReason: &reason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		log.Warn("review job abandoned with no review to close", "repo", a.Repo, "pr", a.PR, "head", a.HeadSHA, "err", cause)
		return
	}
	if err != nil {
		log.Error("abandoned review not recorded", "repo", a.Repo, "pr", a.PR, "head", a.HeadSHA, "err", err)
		return
	}
	log.Warn("review abandoned after its last attempt", "repo", a.Repo, "pr", a.PR, "head", a.HeadSHA, "review_id", id, "err", cause)
	target, err := gh.ParsePR(a.Repo, a.PR, a.HeadSHA)
	if err != nil {
		log.Error("abandoned review's check not completed", "review_id", id, "err", err)
		return
	}
	client, err := ab.GitHub.Poster(ctx, a.InstallationID, a.Repo)
	if err != nil {
		log.Error("abandoned review's check not completed", "review_id", id, "err", err)
		return
	}
	poster := gh.NewPoster(client)
	checks, err := poster.OpenChecks(ctx, target, pipeline.CheckExternalID(id))
	if err != nil {
		log.Error("abandoned review's check not completed", "review_id", id, "err", err)
		return
	}
	for _, check := range checks {
		if err := poster.CompleteCheck(ctx, target, check, gh.CheckResult{
			Conclusion: "neutral", Title: "Review did not complete", Incomplete: true,
			Summary: "Exemplar's review runner failed on every attempt for this commit. Anything already posted stays; use Re-run to try again.",
		}); err != nil {
			log.Error("abandoned review's check not completed", "review_id", id, "check_run", check, "err", err)
		}
	}
}
