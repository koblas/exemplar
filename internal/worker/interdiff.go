package worker

import (
	"context"
	"slices"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Interdiff scoping: on a new head, rules whose scoped files
// did not change since the last complete review, and hold no open finding,
// carry their result forward instead of re-running.

// PlanCarry picks the exemplar rules to carry forward from the previous
// review's runs. A rule carries only if its previous run was ok at full
// coverage with the same version and exactly the same scoped files, and
// none of those files is in changed (the interdiff) or holds an open
// finding. Everything else re-runs on its whole scope.
func PlanCarry(scoped []engine.ScopedRule, prev []queries.CarryableRunsRow, changed, openPaths []string) map[string]engine.CarriedRun {
	touched := map[string]bool{}
	for _, p := range changed {
		touched[p] = true
	}
	for _, p := range openPaths {
		touched[p] = true
	}
	byRule := map[string]queries.CarryableRunsRow{}
	for _, r := range prev {
		byRule[r.RuleID] = r
	}
	out := map[string]engine.CarriedRun{}
	for _, s := range scoped {
		r, ok := byRule[s.Rule.ID]
		if !ok || s.Lane != engine.LaneExemplar || r.Lane != engine.LaneExemplar || r.Status != engine.RunOK ||
			r.Coverage == nil || *r.Coverage < 1 || r.RuleVersion != s.Rule.VersionSHA || !sameFiles(r.ScopedFiles, s.Files) ||
			slices.ContainsFunc(s.Files, func(f string) bool { return touched[f] }) {
			continue
		}
		out[s.Rule.ID] = engine.CarriedRun{FromRunID: r.OriginID, Version: r.RuleVersion, Candidates: int(r.Candidates)}
	}
	return out
}

func sameFiles(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// carry finds the previous complete review of the PR and plans what to
// carry from it. Anything that prevents a safe plan (no such review, a
// moved base, an earlier head that cannot be fetched) means a full review.
func (w *Worker) carry(ctx context.Context, p *engine.Prepared, open []pipeline.OpenFinding) (map[string]engine.CarriedRun, string) {
	if w.cfg.Store == nil {
		return nil, ""
	}
	last, err := w.cfg.Store.LastCompleteReview(ctx, queries.LastCompleteReviewParams{Repo: p.PR.Repo, PrNumber: int32(p.PR.Number), HeadSha: p.PR.HeadSHA})
	if err != nil || len(last) == 0 {
		return nil, ""
	}
	// A moved base changes every file's diff, changed or not.
	if last[0].BaseSha != p.PR.BaseSHA {
		return nil, ""
	}
	changed, err := p.Workspace.ChangedSince(ctx, last[0].HeadSha)
	if err != nil {
		w.cfg.Log.Info("full review: earlier head unavailable", "repo", p.PR.Repo, "pr", p.PR.Number, "err", err)
		return nil, ""
	}
	prev, err := w.cfg.Store.CarryableRuns(ctx, last[0].ID)
	if err != nil {
		return nil, ""
	}
	var openPaths []string
	for _, f := range open {
		openPaths = append(openPaths, f.Path)
	}
	return PlanCarry(p.Scoped, prev, changed, openPaths), last[0].HeadSha
}
