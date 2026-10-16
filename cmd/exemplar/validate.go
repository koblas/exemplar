package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/config"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [exemplar-config dir]",
		Short: "Validate an exemplar-config tree against the schemas",
		Long: "Validates global, repo, rule, eval and platform files against schema/, " +
			"and checks cross-file references. Exits non-zero listing every problem.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			s, err := config.LoadSnapshot(os.DirFS(dir), "")
			if probs, ok := errors.AsType[config.Problems](err); ok {
				for _, p := range probs {
					fmt.Fprintln(cmd.ErrOrStderr(), p.Error())
				}
				return fmt.Errorf("%d %w in %s", len(probs), errConfigProblems, dir)
			}
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %d rules, %d repos, %d eval sets, %d adversarial cases, %d verifier labels, %d categorizer labels\n",
				len(s.Rules), len(s.Repos), len(s.Evals), len(s.Adversarial), len(s.VerifierLabels), len(s.CategorizerLabels))
			return nil
		},
	}
}
