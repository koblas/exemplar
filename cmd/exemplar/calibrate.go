package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/calibrate"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/store"
)

func newCalibrateCmd() *cobra.Command {
	var (
		model                            modelFlags
		configDir, since, dbURL, workDir string
		jsonOut, markdownOut, tokEnv     string
		stage                            string
		dbLimit                          int
	)
	cmd := &cobra.Command{
		Use:   "calibrate",
		Short: "Replay labeled findings through the verifier and labeled PRs through the Stage 2 categorizer (spec §6.8, FR-X9)",
		Long: "Verifier: verifies each labeled finding alone (no generator) and fails unless the verifier refutes at least " +
			"eval_thresholds.verifier.known_wrong_refuted_min of known-wrong findings and at most known_true_refuted_max " +
			"of known-true ones. Labels: the evals/_verifier seed set in --config, plus verifier_labels with --db.\n\n" +
			"Categorizer: runs Stage 2's two-model categorization on each evals/_categorizer PR and fails if more than " +
			"eval_thresholds.categorizer.known_risky_approved_max of risky PRs, or fewer than known_safe_approved_min of " +
			"safe ones, reach min_review_confidence. Skipped while global.yaml has no providers.categorizer.\n\n" +
			"With --only-if-changed-since, each stage runs only when its providers, settings, thresholds or labels changed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			sel, err := selectRules(ctx, configDir, since, nil)
			if err != nil {
				return err
			}
			runVerifier, runCategorizer := stage != "categorizer", stage != "verifier"
			if stage != "verifier" && stage != "categorizer" && stage != "all" {
				return fmt.Errorf("--stage %q %w: want verifier, categorizer or all", stage, errInvalidFlag)
			}
			var md strings.Builder
			if runVerifier && since != "" && !verifierInputsChanged(sel.base, sel.head) {
				md.WriteString("### Verifier calibration\n\nNothing the verifier depends on changed; not run.\n")
				runVerifier = false
			}
			if runCategorizer && sel.head.Global.Providers.Categorizer == nil {
				if stage == "categorizer" {
					return errNoCategorizer
				}
				md.WriteString("### Stage 2 categorizer calibration\n\nNo providers.categorizer in global.yaml; not run.\n")
				runCategorizer = false
			} else if runCategorizer && since != "" && !categorizerInputsChanged(sel.base, sel.head) {
				md.WriteString("### Stage 2 categorizer calibration\n\nNothing the categorizer depends on changed; not run.\n")
				runCategorizer = false
			}
			if !runVerifier && !runCategorizer {
				fmt.Fprint(cmd.OutOrStdout(), md.String())
				return writeOut(markdownOut, md.String())
			}
			var labels []calibrate.Label
			if runVerifier {
				labels = calibrate.SeedLabels(sel.head)
			}
			if runVerifier && dbURL != "" {
				st, err := store.Open(ctx, dbURL)
				if err != nil {
					return fmt.Errorf("open store: %w", err)
				}
				defer st.Close()
				stored, err := calibrate.StoredLabels(ctx, st, dbLimit)
				if err != nil {
					return fmt.Errorf("load stored labels: %w", err)
				}
				labels = append(labels, stored...)
			}
			h, err := model.harness(false)
			if err != nil {
				return err
			}
			sc, err := newScanner()
			if err != nil {
				return err
			}
			if workDir == "" {
				if workDir, err = os.MkdirTemp("", "exemplar-calibrate-*"); err != nil {
					return fmt.Errorf("create work dir: %w", err)
				}
				defer func() { _ = os.RemoveAll(workDir) }()
			}
			r := &calibrate.Runner{Engine: engine.New(h, sc, compiledRunner()), Scanner: sc, Snapshot: sel.head, WorkDir: workDir}
			if tok := os.Getenv(tokEnv); tok != "" {
				r.CloneToken = func(context.Context, string) (string, error) { return tok, nil }
			}
			var (
				out      calibrationReports
				failures []error
			)
			if runVerifier {
				rep, err := r.Verifier(ctx, labels, sel.head.Global.EvalThresholds.Verifier)
				if err != nil {
					return fmt.Errorf("calibrate verifier: %w", err)
				}
				out.Verifier = &rep
				if md.Len() > 0 {
					md.WriteString("\n")
				}
				md.WriteString(calibrationMarkdown(rep, sel.head))
				if !rep.Pass {
					failures = append(failures, fmt.Errorf("verifier %w: %s", errCalibrationFailed, strings.Join(rep.Reasons, "; ")))
				}
			}
			if runCategorizer {
				rep, err := r.Categorizer(ctx, calibrate.SeedPRLabels(sel.head), sel.head.Global.EvalThresholds.Categorizer)
				if err != nil {
					return fmt.Errorf("calibrate categorizer: %w", err)
				}
				out.Categorizer = &rep
				if md.Len() > 0 {
					md.WriteString("\n")
				}
				md.WriteString(categorizerMarkdown(rep, sel.head))
				if !rep.Pass {
					failures = append(failures, fmt.Errorf("categorizer %w: %s", errCalibrationFailed, strings.Join(rep.Reasons, "; ")))
				}
			}
			if jsonOut != "" {
				data, err := json.MarshalIndent(out, "", "  ")
				if err != nil {
					return fmt.Errorf("encode calibration reports: %w", err)
				}
				if err := os.WriteFile(jsonOut, append(data, '\n'), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
					return fmt.Errorf("write json report: %w", err)
				}
			}
			if err := writeOut(markdownOut, md.String()); err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), md.String())
			if len(failures) > 0 {
				return errors.Join(failures...)
			}
			return nil
		},
	}
	model.register(cmd)
	cmd.Flags().StringVar(&configDir, "config", ".", "exemplar-config checkout")
	cmd.Flags().StringVar(&stage, "stage", "all", "verifier, categorizer or all")
	cmd.Flags().StringVar(&since, "only-if-changed-since", "", "run only if verifier inputs changed since this revision (a config PR's base)")
	cmd.Flags().StringVar(&dbURL, "db", os.Getenv("EXEMPLAR_DATABASE_URL"), "Postgres URL for verifier_labels (optional)")
	cmd.Flags().IntVar(&dbLimit, "db-limit", 500, "most verifier_labels rows to replay")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "scratch directory (default: a temp dir)")
	cmd.Flags().StringVar(&jsonOut, "json", "", "write the report as JSON here")
	cmd.Flags().StringVar(&markdownOut, "markdown", "", "write the summary here (e.g. $GITHUB_STEP_SUMMARY)")
	cmd.Flags().StringVar(&tokEnv, "token-env", "EXEMPLAR_EVAL_TOKEN", "environment variable with a read-only token for recorded labels' repos")
	return cmd
}

// verifierInputsChanged reports whether anything the verifier's behavior
// or its calibration depends on differs between base and head.
func verifierInputsChanged(base, head *config.Snapshot) bool {
	if base == nil {
		return true
	}
	b, h := base.Global, head.Global
	var bt, ht any
	if b.EvalThresholds.Verifier != nil {
		bt = *b.EvalThresholds.Verifier
	}
	if h.EvalThresholds.Verifier != nil {
		ht = *h.EvalThresholds.Verifier
	}
	return !reflect.DeepEqual(b.Providers.Verifier, h.Providers.Verifier) ||
		!reflect.DeepEqual(b.Verifier, h.Verifier) || !reflect.DeepEqual(bt, ht) ||
		!reflect.DeepEqual(labelSet(base), labelSet(head))
}

// calibrationReports is the --json output: one report per stage that ran.
type calibrationReports struct {
	Verifier    *calibrate.Report            `json:"verifier,omitempty"`
	Categorizer *calibrate.CategorizerReport `json:"categorizer,omitempty"`
}

func writeOut(path, s string) error {
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// categorizerInputsChanged reports whether anything the auto-approve
// risk categorizer or its calibration depends on differs between base and head.
func categorizerInputsChanged(base, head *config.Snapshot) bool {
	if base == nil {
		return true
	}
	b, h := base.Global, head.Global
	var bp, hp, bt, ht any
	if b.Providers.Categorizer != nil {
		bp = *b.Providers.Categorizer
	}
	if h.Providers.Categorizer != nil {
		hp = *h.Providers.Categorizer
	}
	if b.EvalThresholds.Categorizer != nil {
		bt = *b.EvalThresholds.Categorizer
	}
	if h.EvalThresholds.Categorizer != nil {
		ht = *h.EvalThresholds.Categorizer
	}
	return !reflect.DeepEqual(bp, hp) || !reflect.DeepEqual(bt, ht) ||
		!reflect.DeepEqual(prLabelSet(base), prLabelSet(head))
}

func prLabelSet(s *config.Snapshot) map[string]string {
	m := map[string]string{}
	for _, l := range s.CategorizerLabels {
		data, _ := json.Marshal(l.CategorizerLabel) //nolint:errchkjson // a label decoded from JSON re-encodes without error; this is only a comparison key
		m[l.ID] = string(data)
	}
	return m
}

func categorizerMarkdown(rep calibrate.CategorizerReport, snap *config.Snapshot) string {
	var b strings.Builder
	t := snap.Global.EvalThresholds.Categorizer
	b.WriteString("### Stage 2 categorizer calibration\n\n")
	mark := "✅ pass"
	if !rep.Pass {
		mark = "❌ " + strings.Join(rep.Reasons, "; ")
	}
	fmt.Fprintf(&b, "%s\n\nApproved = review_confidence ≥ %.2f.\n\n| | Labels | Approved | Rate | Bar |\n|---|---|---|---|---|\n", mark, rep.Bar)
	fmt.Fprintf(&b, "| Known risky | %d | %d | %.0f%% | ≤ %.0f%% |\n", rep.KnownRisky.N, rep.KnownRisky.Approved, 100*rep.KnownRisky.Rate, 100*t.KnownRiskyApprovedMax)
	fmt.Fprintf(&b, "| Known safe | %d | %d | %.0f%% | ≥ %.0f%% |\n", rep.KnownSafe.N, rep.KnownSafe.Approved, 100*rep.KnownSafe.Rate, 100*t.KnownSafeApprovedMin)
	fmt.Fprintf(&b, "\nCost $%.4f\n", rep.CostUSD)
	for _, o := range rep.Outcomes {
		if o.Error != "" {
			fmt.Fprintf(&b, "- `%s` could not be replayed: %s\n", o.ID, o.Error)
		}
	}
	return b.String()
}

func labelSet(s *config.Snapshot) map[string]string {
	m := map[string]string{}
	for _, l := range s.VerifierLabels {
		data, _ := json.Marshal(l.VerifierLabel) //nolint:errchkjson // a label decoded from JSON re-encodes without error; this is only a comparison key
		m[l.ID] = string(data)
	}
	return m
}

func calibrationMarkdown(rep calibrate.Report, snap *config.Snapshot) string {
	var b strings.Builder
	t := snap.Global.EvalThresholds.Verifier
	b.WriteString("### Verifier calibration\n\n")
	mark := "✅ pass"
	if !rep.Pass {
		mark = "❌ " + strings.Join(rep.Reasons, "; ")
	}
	fmt.Fprintf(&b, "%s\n\n| | Labels | Refuted | Rate | Bar |\n|---|---|---|---|---|\n", mark)
	fmt.Fprintf(&b, "| Known wrong | %d | %d | %.0f%% | ≥ %.0f%% |\n", rep.KnownWrong.N, rep.KnownWrong.Refuted, 100*rep.KnownWrong.Rate, 100*t.KnownWrongRefutedMin)
	fmt.Fprintf(&b, "| Known true | %d | %d | %.0f%% | ≤ %.0f%% |\n", rep.KnownTrue.N, rep.KnownTrue.Refuted, 100*rep.KnownTrue.Rate, 100*t.KnownTrueRefutedMax)
	fmt.Fprintf(&b, "\nModels: %s · cost $%.4f\n", strings.Join(rep.Models, ", "), rep.CostUSD)
	for _, o := range rep.Outcomes {
		if o.Error != "" {
			fmt.Fprintf(&b, "- `%s` could not be replayed: %s\n", o.ID, o.Error)
		}
	}
	return b.String()
}
