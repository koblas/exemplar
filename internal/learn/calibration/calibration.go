// Package calibration is the installation's own verifier and categorizer
// calibration (spec §6.8, FR-X9), run by `exemplar-learn calibrate`. Each
// run replays the labeled sets through the deployed providers and records
// a calibration_results row keyed by what it is valid for
// (calibrate.VerifierKey, calibrate.CategorizerKey). Stage 1 reads the
// latest verifier row for the deployed key: once it passed, a finding
// verified by the generator's own model family no longer keeps a PR from
// approval (FR-E10). The categorizer row is recorded for the record.
package calibration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/koblas/exemplar/internal/calibrate"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
)

// Replayer runs the replays; *calibrate.Runner implements it.
type Replayer interface {
	Verifier(ctx context.Context, labels []calibrate.Label, t *schema.VerifierThreshold) (calibrate.Report, error)
	Categorizer(ctx context.Context, labels []calibrate.PRLabel, t *schema.CategorizerThreshold) (calibrate.CategorizerReport, error)
}

// Job runs one calibration pass.
type Job struct {
	Store  *store.Store
	Replay Replayer
	Log    *slog.Logger
	// Force reruns a stage whose key already has a passing result.
	Force bool
	// StoredLimit bounds the verifier_labels rows replayed beside the seed set.
	StoredLimit int
}

// Result is what one stage did.
type Result struct {
	Kind    string   `json:"kind"`
	Key     string   `json:"key"`
	Skipped bool     `json:"skipped,omitempty"`
	Passed  bool     `json:"passed"`
	Labels  int      `json:"labels"`
	Reasons []string `json:"reasons,omitempty"`
}

// Run calibrates the verifier and, when the config names one, the
// categorizer. A calibration that fails is a recorded result, not an
// error; an error is a replay that could not run, and records nothing.
func (j *Job) Run(ctx context.Context, snap *config.Snapshot) ([]Result, error) {
	if j.Log == nil {
		j.Log = slog.New(slog.DiscardHandler)
	}
	var out []Result
	v, err := j.verifier(ctx, snap)
	if err != nil {
		return out, err
	}
	out = append(out, v)
	if snap.Global.Providers.Categorizer != nil {
		c, err := j.categorizer(ctx, snap)
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, nil
}

// current reports whether kind's latest result for key passed.
func (j *Job) current(ctx context.Context, kind, key string) (bool, error) {
	row, err := j.Store.LatestCalibration(ctx, queries.LatestCalibrationParams{Kind: kind, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("calibration: latest %s result: %w", kind, err)
	}
	return row.Passed, nil
}

func (j *Job) verifier(ctx context.Context, snap *config.Snapshot) (Result, error) {
	res := Result{Kind: calibrate.KindVerifier, Key: calibrate.VerifierKey(snap)}
	if passed, err := j.current(ctx, res.Kind, res.Key); err != nil || passed && !j.Force {
		res.Skipped, res.Passed = err == nil, passed
		return res, err
	}
	labels := calibrate.SeedLabels(snap)
	stored, err := calibrate.StoredLabels(ctx, j.Store, j.StoredLimit)
	if err != nil {
		return res, fmt.Errorf("calibration: stored labels: %w", err)
	}
	labels = append(labels, stored...)
	rep, err := j.Replay.Verifier(ctx, labels, snap.Global.EvalThresholds.Verifier)
	if err != nil {
		return res, fmt.Errorf("calibration: verifier: %w", err)
	}
	res.Passed, res.Labels, res.Reasons = rep.Pass, len(labels), rep.Reasons
	err = j.Store.RecordCalibration(ctx, queries.RecordCalibrationParams{
		Kind: res.Kind, Key: res.Key, ConfigVersion: snap.Version, Providers: calibrate.VerifierProviders(snap),
		KnownWrongRefuted: rate(rep.KnownWrong), KnownTrueRefuted: rate(rep.KnownTrue),
		Labels: int32(min(len(labels), 1<<30)), Passed: rep.Pass,
	})
	if err != nil {
		return res, fmt.Errorf("calibration: record verifier result: %w", err)
	}
	j.Log.Info("verifier calibration", "passed", rep.Pass, "labels", len(labels), "reasons", rep.Reasons)
	return res, nil
}

func (j *Job) categorizer(ctx context.Context, snap *config.Snapshot) (Result, error) {
	res := Result{Kind: calibrate.KindCategorizer, Key: calibrate.CategorizerKey(snap)}
	if passed, err := j.current(ctx, res.Kind, res.Key); err != nil || passed && !j.Force {
		res.Skipped, res.Passed = err == nil, passed
		return res, err
	}
	labels := calibrate.SeedPRLabels(snap)
	rep, err := j.Replay.Categorizer(ctx, labels, snap.Global.EvalThresholds.Categorizer)
	if err != nil {
		return res, fmt.Errorf("calibration: categorizer: %w", err)
	}
	res.Passed, res.Labels, res.Reasons = rep.Pass, len(labels), rep.Reasons
	err = j.Store.RecordCalibration(ctx, queries.RecordCalibrationParams{
		Kind: res.Kind, Key: res.Key, ConfigVersion: snap.Version, Providers: calibrate.CategorizerProviders(snap),
		Labels: int32(min(len(labels), 1<<30)), Passed: rep.Pass,
	})
	if err != nil {
		return res, fmt.Errorf("calibration: record categorizer result: %w", err)
	}
	j.Log.Info("categorizer calibration", "passed", rep.Pass, "labels", len(labels), "reasons", rep.Reasons)
	return res, nil
}

func rate(r calibrate.Rates) *float32 {
	if r.N == 0 {
		return nil
	}
	v := float32(r.Rate)
	return &v
}
