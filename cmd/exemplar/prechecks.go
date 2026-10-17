package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/precheck"
	"github.com/koblas/exemplar/internal/schema"
)

// precheckFlags are shared by volume and linter-overlap.
type precheckFlags struct {
	configDir, since, jsonOut, markdownOut, tokEnv, apiURL string
	rules                                                  []string
}

func (f *precheckFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.configDir, "config", ".", "exemplar-config checkout")
	cmd.Flags().StringVar(&f.since, "changed-since", "", "check the rules changed since this git revision (a config PR's base)")
	cmd.Flags().StringSliceVar(&f.rules, "rules", nil, "rule ids to check")
	cmd.Flags().StringVar(&f.jsonOut, "json", "", "write the reports as JSON here")
	cmd.Flags().StringVar(&f.markdownOut, "markdown", "", "write the summary here (e.g. $GITHUB_STEP_SUMMARY)")
	cmd.Flags().StringVar(&f.tokEnv, "token-env", "EXEMPLAR_EVAL_TOKEN", "environment variable with a read-only token for the target repos")
	cmd.Flags().StringVar(&f.apiURL, "github-api-url", os.Getenv("EXEMPLAR_GH_API_URL"), "GitHub API base URL (GHES)")
}

func (f *precheckFlags) client() (*github.Client, string, error) {
	token := os.Getenv(f.tokEnv)
	if token == "" {
		return nil, "", fmt.Errorf("$%s %w: this check reads the target repos", f.tokEnv, errEmptyToken)
	}
	opts := []github.ClientOptionsFunc{github.WithAuthToken(token)}
	if f.apiURL != "" {
		opts = append(opts, github.WithURLs(&f.apiURL, &f.apiURL))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, "", fmt.Errorf("github client: %w", err)
	}
	return c, token, nil
}

func (f *precheckFlags) write(cmd *cobra.Command, reports any, md string) error {
	if f.jsonOut != "" {
		data, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			return fmt.Errorf("encode reports: %w", err)
		}
		if err := os.WriteFile(f.jsonOut, append(data, '\n'), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
			return fmt.Errorf("write json report: %w", err)
		}
	}
	if f.markdownOut != "" {
		if err := os.WriteFile(f.markdownOut, []byte(md), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
			return fmt.Errorf("write markdown report: %w", err)
		}
	}
	fmt.Fprint(cmd.OutOrStdout(), md)
	return nil
}

func findRule(snap *config.Snapshot, id string) (config.Rule, bool) {
	for _, r := range snap.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return config.Rule{}, false
}

// addedOrActivated keeps the rules a config PR adds (new and runnable) or
// activates (status becomes active), the trigger for a volume estimate.
func addedOrActivated(sel selection, ids []string) []string {
	var out []string
	for _, id := range ids {
		r, ok := findRule(sel.head, id)
		if !ok || (r.Status != schema.StatusActive && r.Status != schema.StatusShadow) {
			continue
		}
		if sel.base == nil {
			out = append(out, id)
			continue
		}
		before, existed := findRule(sel.base, id)
		if !existed || (r.Status == schema.StatusActive && before.Status != schema.StatusActive) {
			out = append(out, id)
		}
	}
	return out
}

func newVolumeCmd() *cobra.Command {
	var (
		f       precheckFlags
		model   modelFlags
		workDir string
		cfg     precheck.VolumeConfig
	)
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Estimate a new or activated rule's comment volume (spec §6.10)",
		Long: "Runs each rule the config PR adds or activates alone over a sample of PRs merged in the last --days in " +
			"the repos it applies to, and reports the share of PRs it would comment on and comments per PR after the " +
			"comment budget. Informational: the check fails only if a run fails.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			sel, err := selectRules(ctx, f.configDir, f.since, f.rules)
			if err != nil {
				return err
			}
			ids := addedOrActivated(sel, sel.changed)
			if len(ids) == 0 {
				return f.write(cmd, []precheck.VolumeReport{}, "### Exemplar volume estimate\n\nNo rule is added or activated.\n")
			}
			client, token, err := f.client()
			if err != nil {
				return err
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
				if workDir, err = os.MkdirTemp("", "exemplar-volume-*"); err != nil {
					return fmt.Errorf("create work dir: %w", err)
				}
				defer func() { _ = os.RemoveAll(workDir) }()
			}
			trial := &playground.Runner{
				Engine: engine.New(h, sc, compiledRunner()), Scanner: sc, WorkDir: workDir, Caller: "eval",
				GitHub: &userGitHub{c: client, token: token}, CloneURL: gh.CloneURL(os.Getenv("EXEMPLAR_GH_WEB_URL")),
			}
			var reports []precheck.VolumeReport
			for _, id := range ids {
				r, _ := findRule(sel.head, id)
				rep, err := precheck.Volume(ctx, ghPRs{client}, trial, sel.head, sel.eligible, r, cfg)
				if err != nil {
					return fmt.Errorf("estimate volume of %s: %w", id, err)
				}
				reports = append(reports, rep)
			}
			return f.write(cmd, reports, volumeMarkdown(reports, cfg))
		},
	}
	f.register(cmd)
	model.register(cmd)
	cmd.Flags().StringVar(&workDir, "work-dir", "", "scratch directory (default: a temp dir)")
	cmd.Flags().IntVar(&cfg.Days, "days", 30, "window of merged PRs")
	cmd.Flags().IntVar(&cfg.PerRepo, "per-repo", 10, "merged PRs sampled per repo")
	cmd.Flags().IntVar(&cfg.MaxRuns, "max-runs", 40, "rule runs across all repos (bounds model cost)")
	return cmd
}

func newLinterOverlapCmd() *cobra.Command {
	var f precheckFlags
	cmd := &cobra.Command{
		Use:   "linter-overlap",
		Short: "Fail when a repo's linters already flag a rule's positive cases (spec §8.7)",
		Long: "For each changed rule's positive history cases, compares the annotations the repo's configured linters " +
			"(repos/<org>/<repo>.yaml linters) reported on that commit with the expected lines. Any overlap fails the " +
			"check and names the linter rule. Synthetic cases are skipped: no CI ran on them.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			sel, err := selectRules(ctx, f.configDir, f.since, f.rules)
			if err != nil {
				return err
			}
			var ids []string
			for _, id := range sel.changed {
				if _, ok := sel.head.Evals[id]; ok {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				return f.write(cmd, []precheck.OverlapReport{}, "### Exemplar linter overlap\n\nNo changed rule has eval cases to check.\n")
			}
			client, _, err := f.client()
			if err != nil {
				return err
			}
			var reports []precheck.OverlapReport
			failed := false
			for _, id := range ids {
				cases, err := evalrun.Cases(os.DirFS(f.configDir), sel.head.Evals[id])
				if err != nil {
					return fmt.Errorf("load eval cases for %s: %w", id, err)
				}
				rep, err := precheck.LinterOverlap(ctx, ghAnnotations{client}, sel.head, sel.eligible, id, cases)
				if err != nil {
					return fmt.Errorf("check linter overlap for %s: %w", id, err)
				}
				failed = failed || !rep.Pass
				reports = append(reports, rep)
			}
			if err := f.write(cmd, reports, overlapMarkdown(reports)); err != nil {
				return err
			}
			if failed {
				return errLinterOverlap
			}
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

type ghPRs struct{ c *github.Client }

func (g ghPRs) MergedPRs(ctx context.Context, repo string, since time.Time, limit int) ([]gh.PullRequest, [][]string, error) {
	prs, files, err := gh.MergedPRs(ctx, g.c, repo, since, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("list merged PRs in %s: %w", repo, err)
	}
	return prs, files, nil
}

type ghAnnotations struct{ c *github.Client }

func (g ghAnnotations) Annotations(ctx context.Context, repo, sha string, checks []string) ([]gh.Annotation, error) {
	anns, err := gh.Annotations(ctx, g.c, repo, sha, checks)
	if err != nil {
		return nil, fmt.Errorf("list annotations in %s@%s: %w", repo, sha, err)
	}
	return anns, nil
}

func volumeMarkdown(reports []precheck.VolumeReport, cfg precheck.VolumeConfig) string {
	var b strings.Builder
	b.WriteString("### Exemplar volume estimate\n\n")
	fmt.Fprintf(&b, "Each new or activated rule ran alone on up to %d PRs merged in the last %d days per repo, after the comment budget.\n\n", cfg.PerRepo, cfg.Days)
	b.WriteString("| Rule | Repos | PRs sampled | In scope | PRs commented on | Comments per PR | Most on one PR | Cost |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range reports {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d (%.0f%%) | %.2f | %d | $%.4f |\n",
			r.Rule, len(r.Repos), r.Sampled, r.InScope, r.Affected, 100*r.ShareAffected, r.CommentsPerPR, r.MaxPerPR, r.CostUSD)
	}
	for _, r := range reports {
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "\n`%s`: %s.", r.Rule, n)
		}
	}
	b.WriteString("\n")
	return b.String()
}

func overlapMarkdown(reports []precheck.OverlapReport) string {
	var b strings.Builder
	b.WriteString("### Exemplar linter overlap\n\n")
	for _, r := range reports {
		if r.Pass {
			fmt.Fprintf(&b, "- ✅ `%s`: no linter flags its %d checked positive %s.", r.Rule, r.Checked, plural(r.Checked, "case", "cases"))
		} else {
			fmt.Fprintf(&b, "- ❌ `%s`: the repo's linters already flag these positive cases, so the rule would duplicate them:\n", r.Rule)
			for _, o := range r.Overlaps {
				fmt.Fprintf(&b, "  - %s `%s` on `%s` L%d (case %q, %s)\n", o.Linter, o.Rule, o.Path, o.Start, o.Case, o.Repo)
			}
		}
		if len(r.Skipped) > 0 {
			fmt.Fprintf(&b, " Skipped %d: %s.", len(r.Skipped), strings.Join(r.Skipped, "; "))
		}
		b.WriteString("\n")
	}
	return b.String()
}
