package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/replay"
	"github.com/koblas/exemplar/internal/store"
)

func newReplayCmd() *cobra.Command {
	var (
		model                        modelFlags
		app                          appFlags
		configDir, fixtures, workDir string
		dbURL, outFile               string
		post, baseline               bool
	)
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Rerun the review pipeline over recorded PRs (T-0.1, T-0.2)",
		Long: "Reviews each fixture with the engine (caller=eval). With --db, each fixture is a River job and " +
			"results are stored; with --post, findings on real fixtures are posted to their sandbox PR; with " +
			"--baseline, the baseline reviewer runs in shadow. Prints a JSON outcome per fixture.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			snap, err := config.LoadSnapshot(os.DirFS(configDir), configDir)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			fx, err := replay.LoadFixtures(fixtures)
			if err != nil {
				return fmt.Errorf("load fixtures: %w", err)
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
				if workDir, err = os.MkdirTemp("", "exemplar-replay-*"); err != nil {
					return fmt.Errorf("create work dir: %w", err)
				}
				defer func() { _ = os.RemoveAll(workDir) }()
			}
			r := &replay.Runner{
				Snapshot: snap, Scanner: sc, WorkDir: workDir, Baseline: baseline,
				Engine: engine.New(h, sc, compiledRunner()),
			}
			if dbURL != "" {
				st, err := store.Open(ctx, dbURL)
				if err != nil {
					return fmt.Errorf("open store: %w", err)
				}
				defer st.Close()
				if err := st.Migrate(ctx); err != nil {
					return fmt.Errorf("migrate store: %w", err)
				}
				r.Store = st
			}
			if post || app.appID != 0 {
				ghApp, err := app.app()
				if err != nil {
					return err
				}
				r.CloneTokenFor = func(ctx context.Context, repo string) (string, error) {
					tok, err := ghApp.Token(ctx, app.installationID, gh.FlavorWorker, []string{repo})
					if err != nil {
						return "", fmt.Errorf("installation token: %w", err)
					}
					return tok.GetToken(), nil
				}
				if post {
					r.PosterFor = func(ctx context.Context, repo string) (*gh.Poster, error) {
						c, err := ghApp.Client(ctx, app.installationID, gh.FlavorPoster, []string{repo})
						if err != nil {
							return nil, fmt.Errorf("poster client: %w", err)
						}
						return gh.NewPoster(c), nil
					}
				}
			}

			outs, errs, err := r.RunAll(ctx, fx)
			if err != nil {
				return fmt.Errorf("run fixtures: %w", err)
			}
			data, err := replay.MarshalOutcomes(outs)
			if err != nil {
				return fmt.Errorf("marshal outcomes: %w", err)
			}
			if outFile != "" {
				if err := os.WriteFile(outFile, append(data, '\n'), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
					return fmt.Errorf("write outcomes: %w", err)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			}
			if len(errs) > 0 {
				ids := make([]string, 0, len(errs))
				for id := range errs {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", id, errs[id])
				}
				return fmt.Errorf("%d %w", len(errs), errFixturesFailed)
			}
			return nil
		},
	}
	model.register(cmd)
	app.register(cmd)
	cmd.Flags().StringVar(&configDir, "config", "", "exemplar-config checkout (required)")
	cmd.Flags().StringVar(&fixtures, "fixtures", "", "directory of recorded-PR fixtures (required)")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "keep checkouts here instead of a temp dir")
	cmd.Flags().StringVar(&dbURL, "db", os.Getenv("EXEMPLAR_DATABASE_URL"), "Postgres URL; enables storage and River jobs (EXEMPLAR_DATABASE_URL)")
	cmd.Flags().StringVar(&outFile, "out", "", "write the JSON outcomes to a file")
	cmd.Flags().BoolVar(&post, "post", false, "post findings on real fixtures to their sandbox PR (needs app flags)")
	cmd.Flags().BoolVar(&baseline, "baseline", false, "also run the baseline reviewer in shadow")
	_ = cmd.MarkFlagRequired("config")
	_ = cmd.MarkFlagRequired("fixtures")
	return cmd
}
