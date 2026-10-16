package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/gh"
)

// appFlags configure GitHub App access. Values come from flags or the
// EXEMPLAR_GH_* environment; the private key is read from a file and never
// printed.
type appFlags struct {
	appID          int64
	keyFile        string
	installationID int64
	configRepo     string
	baseURL        string
}

func (f *appFlags) register(cmd *cobra.Command) {
	envInt := func(k string) int64 {
		v, _ := strconv.ParseInt(os.Getenv(k), 10, 64)
		return v
	}
	cmd.Flags().Int64Var(&f.appID, "app-id", envInt("EXEMPLAR_GH_APP_ID"), "review app id (EXEMPLAR_GH_APP_ID)")
	cmd.Flags().StringVar(&f.keyFile, "private-key-file", os.Getenv("EXEMPLAR_GH_PRIVATE_KEY_FILE"), "review app private key PEM (EXEMPLAR_GH_PRIVATE_KEY_FILE)")
	cmd.Flags().Int64Var(&f.installationID, "installation-id", envInt("EXEMPLAR_GH_INSTALLATION_ID"), "installation id (EXEMPLAR_GH_INSTALLATION_ID)")
	cmd.Flags().StringVar(&f.configRepo, "config-repo", os.Getenv("EXEMPLAR_CONFIG_REPO"), "owner/exemplar-config (EXEMPLAR_CONFIG_REPO)")
	cmd.Flags().StringVar(&f.baseURL, "github-api-url", os.Getenv("EXEMPLAR_GH_API_URL"), "GitHub API base URL; empty for github.com")
}

func (f *appFlags) app() (*gh.App, error) {
	if f.appID == 0 || f.keyFile == "" || f.installationID == 0 || f.configRepo == "" {
		return nil, fmt.Errorf("app id, private key file, installation id and config repo %w", errFlagsRequired)
	}
	key, err := os.ReadFile(f.keyFile)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	app, err := gh.NewApp(gh.AppConfig{AppID: f.appID, PrivateKey: key, ConfigRepo: f.configRepo, BaseURL: f.baseURL}, nil)
	if err != nil {
		return nil, fmt.Errorf("github app: %w", err)
	}
	return app, nil
}

func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Checks that run against the sandbox GitHub org",
	}
	cmd.AddCommand(newProbeApprovalCmd())
	return cmd
}

func newProbeApprovalCmd() *cobra.Command {
	var (
		flags appFlags
		repo  string
		pr    int
		wait  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "probe-approval",
		Short: "T-0.3: check whether a native APPROVE from the review app counts toward required approvals",
		Long: "Submits an APPROVE review from the review app on a sandbox PR that has no approvals, " +
			"reads GitHub's review decision, and dismisses the approval. Prints the result as JSON, " +
			"including the approval_mode to set in the repo's config. Use only on sandbox repos.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := flags.app()
			if err != nil {
				return err
			}
			client, err := app.Client(cmd.Context(), flags.installationID, gh.FlavorPoster, []string{repo})
			if err != nil {
				return fmt.Errorf("poster client: %w", err)
			}
			target, err := gh.ParsePR(repo, pr, "")
			if err != nil {
				return fmt.Errorf("parse PR: %w", err)
			}
			pull, _, err := client.PullRequests.Get(cmd.Context(), target.Owner, target.Repo, pr)
			if err != nil {
				return fmt.Errorf("get PR: %w", err)
			}
			target.HeadSHA = pull.GetHead().GetSHA()
			res, err := gh.NewPoster(client).ProbeApproval(cmd.Context(), target, wait)
			if err != nil {
				return fmt.Errorf("probe approval: %w", err)
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&repo, "repo", "", "sandbox repo owner/name (required)")
	cmd.Flags().IntVar(&pr, "pr", 0, "PR number with no approvals (required)")
	cmd.Flags().DurationVar(&wait, "wait", 20*time.Second, "how long to wait for GitHub to recompute the review decision")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}
