package calibrate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/replay"
	"github.com/koblas/exemplar/internal/schema"
)

// PRLabel is one labeled PR for auto-approve risk categorizer
// calibration.
type PRLabel struct {
	ID      string
	Label   string // schema.LabelSafe | schema.LabelRisky
	Fixture schema.Fixture
}

// SeedPRLabels converts a snapshot's evals/_categorizer set.
func SeedPRLabels(snap *config.Snapshot) []PRLabel {
	out := make([]PRLabel, 0, len(snap.CategorizerLabels))
	for _, l := range snap.CategorizerLabels {
		out = append(out, PRLabel{ID: l.ID, Label: l.Label, Fixture: l.Fixture})
	}
	return out
}

// ApprovalRates are one PR label class's counts.
type ApprovalRates struct {
	N        int     `json:"n"`
	Approved int     `json:"approved"`
	Rate     float64 `json:"approved_rate"`
}

// PROutcome is one labeled PR's categorization.
type PROutcome struct {
	ID               string  `json:"id"`
	Label            string  `json:"label"`
	ReviewConfidence float64 `json:"review_confidence"`
	RiskA            string  `json:"risk_a,omitempty"`
	RiskB            string  `json:"risk_b,omitempty"`
	Why              string  `json:"why,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// CategorizerReport is a risk categorizer calibration run.
type CategorizerReport struct {
	KnownRisky ApprovalRates `json:"known_risky"`
	KnownSafe  ApprovalRates `json:"known_safe"`
	Bar        float64       `json:"min_review_confidence"`
	Errors     int           `json:"errors"`
	CostUSD    float64       `json:"cost_usd"`
	Outcomes   []PROutcome   `json:"outcomes"`
	Pass       bool          `json:"pass"`
	Reasons    []string      `json:"reasons,omitempty"`
}

// Categorizer replays labeled PRs through the auto-approve two-model
// categorization. A PR counts as approved when review_confidence reaches
// the bar. It fails if too many risky PRs or too few safe ones are
// approved, if a class is empty, or if any PR cannot be replayed.
func (r *Runner) Categorizer(ctx context.Context, labels []PRLabel, t *schema.CategorizerThreshold) (CategorizerReport, error) {
	rep := CategorizerReport{Outcomes: []PROutcome{}}
	if t == nil {
		return rep, errors.New("calibrate: global.yaml has no eval_thresholds.categorizer")
	}
	// The production bar: calibrating against a lower one would pass a
	// categorizer that approves less safely than auto-approve requires.
	rep.Bar = gates.ReviewConfidenceBar(t.MinReviewConfidence)
	for i, l := range labels {
		o := PROutcome{ID: l.ID, Label: l.Label}
		s2, cost, err := r.categorize(ctx, l, filepath.Join(r.WorkDir, fmt.Sprintf("pr-%04d", i)))
		rep.CostUSD += cost
		if err != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			o.Error = err.Error()
			rep.Errors++
			rep.Outcomes = append(rep.Outcomes, o)
			continue
		}
		o.ReviewConfidence, o.RiskA, o.RiskB, o.Why = s2.ReviewConfidence, s2.A.Risk, s2.B.Risk, s2.Why
		cls := &rep.KnownSafe
		if l.Label == schema.LabelRisky {
			cls = &rep.KnownRisky
		}
		cls.N++
		if s2.ReviewConfidence >= rep.Bar {
			cls.Approved++
		}
		rep.Outcomes = append(rep.Outcomes, o)
	}
	for _, c := range []*ApprovalRates{&rep.KnownRisky, &rep.KnownSafe} {
		if c.N > 0 {
			c.Rate = math.Round(float64(c.Approved)/float64(c.N)*1000) / 1000
		}
	}
	switch {
	case rep.KnownRisky.N == 0:
		rep.Reasons = append(rep.Reasons, "no risky PR labels replayed")
	case rep.KnownRisky.Rate > t.KnownRiskyApprovedMax:
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("would approve %.0f%% of known-risky PRs; at most %.0f%% allowed", 100*rep.KnownRisky.Rate, 100*t.KnownRiskyApprovedMax))
	}
	switch {
	case rep.KnownSafe.N == 0:
		rep.Reasons = append(rep.Reasons, "no safe PR labels replayed")
	case rep.KnownSafe.Rate < t.KnownSafeApprovedMin:
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("would approve %.0f%% of known-safe PRs; need at least %.0f%%", 100*rep.KnownSafe.Rate, 100*t.KnownSafeApprovedMin))
	}
	if rep.Errors > 0 {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("%d %s could not be replayed", rep.Errors, plural(rep.Errors, "PR", "PRs")))
	}
	rep.Pass = len(rep.Reasons) == 0
	return rep, nil
}

func (r *Runner) categorize(ctx context.Context, l PRLabel, dir string) (gates.Stage2Categorization, float64, error) {
	defer func() { _ = os.RemoveAll(dir) }()
	cloneURL, base, head, err := replay.Materialize(ctx, l.Fixture, filepath.Join(dir, "source"))
	if err != nil {
		return gates.Stage2Categorization{}, 0, err
	}
	token := ""
	if l.Fixture.Synthetic == nil && r.CloneToken != nil {
		if token, err = r.CloneToken(ctx, l.Fixture.Repo); err != nil {
			return gates.Stage2Categorization{}, 0, err
		}
	}
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "wt"), engine.CheckoutOptions{CloneURL: cloneURL, BaseSHA: base, HeadSHA: head, Token: token})
	if err != nil {
		return gates.Stage2Categorization{}, 0, err
	}
	fx := l.Fixture
	p, err := engine.Prepare(ctx, ws, r.Snapshot, engine.PRInfo{
		Repo: fx.Repo, Number: fx.PR, Title: fx.Title, Body: fx.Body,
		Author: fx.Author, BaseSHA: base, HeadSHA: head,
	}, r.Scanner)
	if err != nil {
		return gates.Stage2Categorization{}, 0, err
	}
	if p.Incomplete != "" {
		return gates.Stage2Categorization{}, 0, fmt.Errorf("incomplete: %s", p.Incomplete)
	}
	s2, runs := r.Engine.Categorize(ctx, p, "eval")
	cost := 0.0
	for _, run := range runs {
		cost += run.CostUSD
	}
	// A side that failed is an unreplayable label, not a "not approved":
	// otherwise an outage would pass the risky half of the calibration.
	for _, s := range []gates.Categorization{s2.A, s2.B} {
		if !s.OK {
			return s2, cost, fmt.Errorf("categorizer side %s: %s", s.Side, s.Detail)
		}
	}
	return s2, cost, nil
}
