package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/playground"
)

func newTryCmd() *cobra.Command {
	var (
		model              modelFlags
		configDir, workDir string
		tokenEnv, apiURL   string
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use:   "try <rule.yaml> <org/repo#N | PR URL>",
		Short: "Run a draft rule on a PR without posting anything (spec §10.1)",
		Long: "Loads exemplar-config from --config with the rule file added (replacing a rule with the same id), " +
			"checks that your GitHub token can read the target repo and that the repo is eligible, then runs only " +
			"that rule on the PR (caller=cli) and prints the findings. Nothing is posted on the PR. The token comes " +
			"from $GITHUB_TOKEN by default and is used for the access check, the PR's metadata and the clone.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ruleYAML, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			target, err := playground.ParseTarget(args[1])
			if err != nil {
				return fmt.Errorf("target: %w", err)
			}
			snap, ruleID, err := playground.RuleFileSnapshot(os.DirFS(configDir), ruleYAML, configDir)
			if err != nil {
				return err
			}
			token := os.Getenv(tokenEnv)
			if token == "" {
				return fmt.Errorf("$%s is empty: try needs your own GitHub token to check that you can read %s", tokenEnv, target.Repo)
			}
			opts := []github.ClientOptionsFunc{github.WithAuthToken(token)}
			if apiURL != "" {
				opts = append(opts, github.WithURLs(&apiURL, &apiURL))
			}
			client, err := github.NewClient(opts...)
			if err != nil {
				return err
			}
			me, _, err := client.Users.Get(ctx, "")
			if err != nil {
				return fmt.Errorf("identify the token's user: %w", err)
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
				if workDir, err = os.MkdirTemp("", "exemplar-try-*"); err != nil {
					return err
				}
				defer func() { _ = os.RemoveAll(workDir) }()
			}
			r := &playground.Runner{
				Engine: engine.New(h, sc, compiledRunner()), Scanner: sc, WorkDir: workDir, Caller: "cli",
				GitHub: &userGitHub{c: client, token: token},
			}
			out, err := r.Try(ctx, playground.Request{Actor: me.GetLogin(), Snapshot: snap, RuleIDs: []string{ruleID}, Target: target})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			fmt.Fprint(cmd.OutOrStdout(), out.Reply)
			if !strings.HasSuffix(out.Reply, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if out.Denied {
				return errors.New("not run")
			}
			return nil
		},
	}
	model.register(cmd)
	cmd.Flags().StringVar(&configDir, "config", "exemplar-config", "exemplar-config checkout")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "scratch directory for the PR checkout (default: a temp dir)")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "GITHUB_TOKEN", "environment variable holding your GitHub token")
	cmd.Flags().StringVar(&apiURL, "github-api-url", os.Getenv("EXEMPLAR_GH_API_URL"), "GitHub API base URL (GHES)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the outcome as JSON")
	return cmd
}

// userGitHub answers with the caller's own token: if it can read the
// repo, the caller can (spec §8.7's user check).
type userGitHub struct {
	c     *github.Client
	token string
}

func (u *userGitHub) CanRead(ctx context.Context, _, repo string) (bool, error) {
	owner, name, _ := strings.Cut(repo, "/")
	_, _, err := u.c.Repositories.Get(ctx, owner, name)
	if err == nil {
		return true, nil
	}
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil && (er.Response.StatusCode == http.StatusNotFound || er.Response.StatusCode == http.StatusForbidden) {
		return false, nil
	}
	return false, err
}

func (u *userGitHub) PullRequest(ctx context.Context, repo string, n int) (gh.PullRequest, error) {
	return gh.GetPullRequest(ctx, u.c, repo, n)
}

func (u *userGitHub) CloneToken(context.Context, string) (string, error) { return u.token, nil }
