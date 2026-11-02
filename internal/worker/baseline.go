package worker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// BaselineCaller is reviews.caller for the baseline reviewer's runs.
const BaselineCaller = "baseline"

// sampledForBaseline reports whether a reviewed head also gets a baseline
// run (spec §6.3 item 10, FR-X8). The draw is a hash of the head, so a
// retried job decides the same way, and every head is drawn at rate
// whatever its repo and size, which samples each repo × size-bucket
// stratum at rate in expectation.
func sampledForBaseline(rate float64, repo string, pr int, head string) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s#%d@%s", repo, pr, head))
	return float64(binary.BigEndian.Uint64(sum[:8])>>11)/(1<<53) < rate
}

// baseline runs the rule-less baseline reviewer on a sampled head after
// its production review, while the checkout is still there. It is stored
// with caller=baseline and never posted; it touches no check run, gate or
// feedback roll-up. A failure is logged: the production review is done.
func (w *Worker) baseline(ctx context.Context, p *engine.Prepared, a store.PRReviewArgs) {
	if w.cfg.Store == nil || w.cfg.Engine == nil || !sampledForBaseline(p.Effective.Global.Baseline.SampleRate, a.Repo, a.PR, a.HeadSHA) {
		return
	}
	log := w.cfg.Log.With("repo", a.Repo, "pr", a.PR, "head", a.HeadSHA)
	done, err := w.cfg.Store.BaselineReviewed(ctx, queries.BaselineReviewedParams{Repo: a.Repo, PrNumber: int32(a.PR), HeadSha: a.HeadSHA})
	if err != nil {
		log.Warn("baseline review skipped", "err", err)
		return
	}
	if done {
		return
	}
	rec, err := pipeline.Begin(ctx, w.cfg.Store, p, BaselineCaller, 1, nil)
	if err != nil {
		log.Warn("baseline review not started", "err", err)
		return
	}
	res, err := w.cfg.Engine.Baseline(ctx, p, engine.Options{ReviewID: rec.ReviewID, Caller: BaselineCaller, RuleRunIDs: rec.RunIDs})
	if err != nil {
		log.Warn("baseline review failed", "review_id", rec.ReviewID, "err", err)
		return
	}
	if err := pipeline.Finish(ctx, w.cfg.Store, rec, res); err != nil {
		log.Warn("baseline review not stored", "review_id", rec.ReviewID, "err", err)
		return
	}
	log.Info("baseline review stored", "review_id", rec.ReviewID, "findings", len(res.Findings))
}
