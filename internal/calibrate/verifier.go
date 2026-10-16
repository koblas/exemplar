// Package calibrate replays labeled findings through the verifier:
// the verifier must refute at least known_wrong_refuted_min
// of known-wrong findings and at most known_true_refuted_max of known-true
// ones (global.yaml eval_thresholds.verifier). It runs nightly and on every
// change to the verifier's prompt, model or provider.
//
// Labels come from the verifier_labels table (author Wrong/Fixed signals,
// once feedback flows) and from the seed set in exemplar-config
// evals/_verifier/. Each label's PR is checked out and its candidate is
// verified alone, with no generator; only the verdict counts.
package calibrate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/replay"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Label is one labeled finding to replay.
type Label struct {
	ID        string
	Label     string // schema.LabelTrue | schema.LabelWrong
	Fixture   schema.Fixture
	Candidate engine.Candidate
	Source    string // seed | verifier_labels
}

// SeedLabels converts a snapshot's evals/_verifier set.
func SeedLabels(snap *config.Snapshot) []Label {
	out := make([]Label, 0, len(snap.VerifierLabels))
	for _, l := range snap.VerifierLabels {
		c := l.Candidate
		out = append(out, Label{ID: l.ID, Label: l.Label, Fixture: l.Fixture, Source: "seed", Candidate: engine.Candidate{
			RuleID: c.Rule, Path: c.Path, Side: c.Side, StartLine: c.StartLine, EndLine: c.EndLine, Claim: c.Claim,
		}})
	}
	return out
}

// Rates are one label class's counts.
type Rates struct {
	N       int     `json:"n"`
	Refuted int     `json:"refuted"`
	Rate    float64 `json:"refuted_rate"`
}

// Outcome is one label's replay.
type Outcome struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Source  string `json:"source"`
	Verdict string `json:"verdict,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Report is a calibration run.
type Report struct {
	KnownWrong Rates     `json:"known_wrong"`
	KnownTrue  Rates     `json:"known_true"`
	Errors     int       `json:"errors"`
	CostUSD    float64   `json:"cost_usd"`
	Models     []string  `json:"models"`
	Outcomes   []Outcome `json:"outcomes"`
	Pass       bool      `json:"pass"`
	Reasons    []string  `json:"reasons,omitempty"`
}

// Runner replays labels.
type Runner struct {
	Engine   *engine.Engine
	Scanner  *redact.Scanner
	Snapshot *config.Snapshot
	WorkDir  string
	// CloneToken returns a read-only token for recorded fixtures' repos.
	CloneToken func(ctx context.Context, repo string) (string, error)
}

// Verifier replays every label and gates the result on t. A label that
// cannot be replayed (checkout fails, rule out of scope) is an error
// outcome; any error fails the run, since a calibration that skipped its
// hard cases would pass too easily.
func (r *Runner) Verifier(ctx context.Context, labels []Label, t *schema.VerifierThreshold) (Report, error) {
	rep := Report{Outcomes: []Outcome{}, Models: []string{}}
	if t == nil {
		return rep, errors.New("calibrate: global.yaml has no eval_thresholds.verifier")
	}
	for i, l := range labels {
		o := Outcome{ID: l.ID, Label: l.Label, Source: l.Source}
		v, cost, model, err := r.one(ctx, l, filepath.Join(r.WorkDir, fmt.Sprintf("label-%04d", i)))
		rep.CostUSD += cost
		if model != "" && !slices.Contains(rep.Models, model) {
			rep.Models = append(rep.Models, model)
		}
		if err != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			o.Error = err.Error()
			rep.Errors++
			rep.Outcomes = append(rep.Outcomes, o)
			continue
		}
		o.Verdict = v
		cls := &rep.KnownTrue
		if l.Label == schema.LabelWrong {
			cls = &rep.KnownWrong
		}
		cls.N++
		if v == engine.VerdictRefuted {
			cls.Refuted++
		}
		rep.Outcomes = append(rep.Outcomes, o)
	}
	for _, c := range []*Rates{&rep.KnownWrong, &rep.KnownTrue} {
		if c.N > 0 {
			c.Rate = math.Round(float64(c.Refuted)/float64(c.N)*1000) / 1000
		}
	}
	switch {
	case rep.KnownWrong.N == 0:
		rep.Reasons = append(rep.Reasons, "no known-wrong labels replayed")
	case rep.KnownWrong.Rate < t.KnownWrongRefutedMin:
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("refuted %.0f%% of known-wrong findings; need at least %.0f%%", 100*rep.KnownWrong.Rate, 100*t.KnownWrongRefutedMin))
	}
	switch {
	case rep.KnownTrue.N == 0:
		rep.Reasons = append(rep.Reasons, "no known-true labels replayed")
	case rep.KnownTrue.Rate > t.KnownTrueRefutedMax:
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("refuted %.0f%% of known-true findings; at most %.0f%% allowed", 100*rep.KnownTrue.Rate, 100*t.KnownTrueRefutedMax))
	}
	if rep.Errors > 0 {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("%d %s could not be replayed", rep.Errors, plural(rep.Errors, "label", "labels")))
	}
	rep.Pass = len(rep.Reasons) == 0
	return rep, nil
}

func (r *Runner) one(ctx context.Context, l Label, dir string) (verdict string, cost float64, model string, err error) {
	defer func() { _ = os.RemoveAll(dir) }()
	cloneURL, base, head, err := replay.Materialize(ctx, l.Fixture, filepath.Join(dir, "source"))
	if err != nil {
		return "", 0, "", err
	}
	token := ""
	if l.Fixture.Synthetic == nil && r.CloneToken != nil {
		if token, err = r.CloneToken(ctx, l.Fixture.Repo); err != nil {
			return "", 0, "", err
		}
	}
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "wt"), engine.CheckoutOptions{CloneURL: cloneURL, BaseSHA: base, HeadSHA: head, Token: token})
	if err != nil {
		return "", 0, "", err
	}
	fx := l.Fixture
	p, err := engine.Prepare(ctx, ws, r.Snapshot, engine.PRInfo{Repo: fx.Repo, Number: fx.PR, Title: fx.Title, Body: fx.Body,
		Author: fx.Author, BaseSHA: base, HeadSHA: head}, r.Scanner)
	if err != nil {
		return "", 0, "", err
	}
	if p.Incomplete != "" {
		return "", 0, "", fmt.Errorf("incomplete: %s", p.Incomplete)
	}
	vs, runs, err := r.Engine.VerifyOnly(ctx, p, []engine.Candidate{l.Candidate}, "eval")
	if err != nil {
		return "", 0, "", err
	}
	for _, run := range runs {
		cost += run.CostUSD
		if run.Status != engine.RunOK {
			return "", cost, "", fmt.Errorf("verifier run %s: %s", run.Status, run.Detail)
		}
	}
	return vs[0].Verdict, cost, vs[0].Model, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Store is where recorded labels come from (store.Store).
type Store interface {
	VerifierCalibrationLabels(ctx context.Context, limit int32) ([]queries.VerifierCalibrationLabelsRow, error)
}

// StoredLabels reads verifier_labels: findings authors marked Wrong or
// Fixed, replayed on the PR commits they were found on.
func StoredLabels(ctx context.Context, st Store, limit int) ([]Label, error) {
	rows, err := st.VerifierCalibrationLabels(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("calibrate: load verifier_labels: %w", err)
	}
	out := make([]Label, 0, len(rows))
	for _, r := range rows {
		out = append(out, Label{
			ID: fmt.Sprintf("finding-%d", r.ID), Label: r.Label, Source: "verifier_labels",
			Fixture: schema.Fixture{ID: fmt.Sprintf("finding-%d", r.ID), Repo: r.Repo, PR: int(r.PrNumber),
				Title: fmt.Sprintf("%s#%d", r.Repo, r.PrNumber), BaseSHA: r.BaseSha, HeadSHA: r.HeadSha},
			Candidate: engine.Candidate{RuleID: r.RuleID, Path: r.Path, Side: r.Side,
				StartLine: int(r.StartLine), EndLine: int(r.EndLine), Claim: r.Message},
		})
	}
	return out, nil
}
