package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/precheck"
)

func newExportApprovalCmd() *cobra.Command {
	var (
		configDir, since, repo, headSHA, author, team string
		reviewTokEnv, teamTokEnv, apiURL, markdownOut string
		pr                                            int
		jsonOut                                       bool
	)
	cmd := &cobra.Command{
		Use:   "export-approval",
		Short: "Require a platform approval for changes to export: blocks (spec §8.9, §10.2)",
		Long: "Compares every rule's export: block at --changed-since with the checkout. When any changed, the check " +
			"passes only if a member of --team (default @platform/devex), other than the PR author, approved the PR's " +
			"current head commit. Reviews are read with $GITHUB_TOKEN; team membership needs a token with members: read " +
			"($EXEMPLAR_TEAMS_TOKEN).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if since == "" || repo == "" || pr == 0 || headSHA == "" {
				return errors.New("--changed-since, --repo, --pr and --head-sha are required")
			}
			sel, err := selectRules(ctx, configDir, since, nil)
			if err != nil {
				return err
			}
			changes := precheck.ExportChanges(sel.base, sel.head)
			var res precheck.ExportApproval
			if len(changes) == 0 {
				res, _ = precheck.CheckExportApproval(ctx, nil, team, author, headSHA, nil, nil)
			} else {
				reviewer, err := githubClient(reviewTokEnv, apiURL)
				if err != nil {
					return err
				}
				teams, err := githubClient(teamTokEnv, apiURL)
				if err != nil {
					return err
				}
				reviews, err := gh.Reviews(ctx, reviewer, repo, pr)
				if err != nil {
					return err
				}
				member := func(ctx context.Context, team, login string) (bool, error) {
					return gh.TeamMember(ctx, teams, team, login)
				}
				if res, err = precheck.CheckExportApproval(ctx, changes, team, author, headSHA, reviews, member); err != nil {
					return err
				}
			}
			md := exportMarkdown(res)
			if markdownOut != "" {
				if err := os.WriteFile(markdownOut, []byte(md), 0o644); err != nil {
					return err
				}
			}
			if jsonOut {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(res); err != nil {
					return err
				}
			} else {
				fmt.Fprint(cmd.OutOrStdout(), md)
			}
			if !res.Pass {
				return errors.New(res.Reason)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configDir, "config", ".", "exemplar-config checkout at the PR head")
	cmd.Flags().StringVar(&since, "changed-since", "", "the PR's base commit")
	cmd.Flags().StringVar(&repo, "repo", os.Getenv("GITHUB_REPOSITORY"), "the config repo, owner/name")
	cmd.Flags().IntVar(&pr, "pr", 0, "the config PR's number")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "the PR's head commit; approvals of other commits do not count")
	cmd.Flags().StringVar(&author, "author", "", "the PR's author, whose own approval does not count")
	cmd.Flags().StringVar(&team, "team", precheck.ExportTeam, "team that must approve export changes")
	cmd.Flags().StringVar(&reviewTokEnv, "review-token-env", "GITHUB_TOKEN", "environment variable with a token that can read the PR's reviews")
	cmd.Flags().StringVar(&teamTokEnv, "team-token-env", "EXEMPLAR_TEAMS_TOKEN", "environment variable with a token that can read team membership")
	cmd.Flags().StringVar(&apiURL, "github-api-url", os.Getenv("EXEMPLAR_GH_API_URL"), "GitHub API base URL (GHES)")
	cmd.Flags().StringVar(&markdownOut, "markdown", "", "write the summary here (e.g. $GITHUB_STEP_SUMMARY)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func githubClient(tokenEnv, apiURL string) (*github.Client, error) {
	token := os.Getenv(tokenEnv)
	if token == "" {
		return nil, fmt.Errorf("$%s is empty", tokenEnv)
	}
	opts := []github.ClientOptionsFunc{github.WithAuthToken(token)}
	if apiURL != "" {
		opts = append(opts, github.WithURLs(&apiURL, &apiURL))
	}
	return github.NewClient(opts...)
}

func exportMarkdown(res precheck.ExportApproval) string {
	var b strings.Builder
	b.WriteString("### Exemplar export approval\n\n")
	if len(res.Changes) == 0 {
		b.WriteString("No rule's `export:` block changed.\n")
		return b.String()
	}
	mark := "❌"
	if res.Pass {
		mark = "✅"
	}
	fmt.Fprintf(&b, "%s %s. Every coding agent in the org reads exported summaries, so %s approves these changes in addition to the rule owner:\n\n", mark, res.Reason, res.Team)
	for _, c := range res.Changes {
		before, after := "(none)", "(none)"
		if c.Before != nil {
			before = fmt.Sprintf("agents: %v, summary: %q", c.Before.Agents, c.Before.Summary)
		}
		if c.After != nil {
			after = fmt.Sprintf("agents: %v, summary: %q", c.After.Agents, c.After.Summary)
		}
		fmt.Fprintf(&b, "- `%s`: %s → %s\n", c.Rule, before, after)
	}
	return b.String()
}
