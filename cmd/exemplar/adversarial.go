package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/adversarial"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

func newAdversarialCmd() *cobra.Command {
	var (
		model     modelFlags
		configDir string
		mode      string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "adversarial",
		Short: "Run the prompt-injection set (T-0.5); fails if any attack succeeds",
		Long: "Runs every case in evals/_adversarial. Compromised mode replaces the model with a script " +
			"that obeys the injection, testing the code-level defenses; it needs no model access. Live mode " +
			"sends each case to the real model through the gateway.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != schema.ModeCompromised && mode != schema.ModeLive {
				return fmt.Errorf("--mode must be %s or %s", schema.ModeCompromised, schema.ModeLive)
			}
			snap, err := config.LoadSnapshot(os.DirFS(configDir), configDir)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			sc, err := newScanner()
			if err != nil {
				return err
			}
			work, err := os.MkdirTemp("", "exemplar-adversarial-*")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(work) }()
			r := &adversarial.Runner{Snapshot: snap, Scanner: sc, WorkDir: work, Compiled: compiledRunner()}
			if mode == schema.ModeLive {
				if r.Live, err = model.harness(true); err != nil {
					return err
				}
			}
			results, err := r.RunAll(cmd.Context(), mode)
			if err != nil {
				return err
			}
			failed := 0
			for _, res := range results {
				if !res.Passed {
					failed++
				}
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(results); err != nil {
					return err
				}
			} else {
				for _, res := range results {
					status := "PASS"
					switch {
					case res.Pending != "":
						status = "PENDING"
					case !res.Passed:
						status = "FAIL"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%-7s %-40s %-14s %-20s coverage=%.2f\n", status, res.Case, res.Vector, res.Goal, res.Coverage)
					if res.Pending != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "        %s\n", res.Pending)
					}
					for _, f := range res.Failures {
						fmt.Fprintf(cmd.OutOrStdout(), "        attack succeeded: %s\n", f)
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d adversarial case(s) achieved their goal", failed)
			}
			if len(results) == 0 {
				return fmt.Errorf("no %s cases found under %s", mode, strings.TrimSuffix(configDir, "/")+"/"+config.AdversarialDir)
			}
			return nil
		},
	}
	model.register(cmd)
	cmd.Flags().StringVar(&configDir, "config", ".", "exemplar-config checkout")
	cmd.Flags().StringVar(&mode, "mode", schema.ModeCompromised, "compromised or live")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}
