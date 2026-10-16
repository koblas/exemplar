package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/evaluate"
	"github.com/koblas/exemplar/internal/learn/evalgen"
	"github.com/koblas/exemplar/internal/schema"
)

func newEvalCmd() *cobra.Command {
	var (
		model                        modelFlags
		configDir, since, workDir    string
		rules                        []string
		jsonOut, markdownOut, tokEnv string
		remote                       bool
		remoteRepo                   string
		wait                         time.Duration
	)
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Evaluate rules against their eval cases (spec §8.9)",
		Long: "Runs each selected rule over its evals/<rule>/cases.yaml: compiled rules on the engine, exemplar rules " +
			"through the lane and verifier (caller=eval). Reports precision, recall and F1 for historical and synthetic " +
			"cases and fails when any rule is below global.yaml eval_thresholds. With --changed-since, evaluates the " +
			"rules a config PR changes, with eligibility taken from that base revision. Case files are read with " +
			"the token in $" + "EXEMPLAR_EVAL_TOKEN (read-only).\n\n" +
			"With --remote (spec §8.9 mode: eval), the evaluation runs in exemplar-worker instead: the CLI sends the " +
			"config PR's merge-base with --changed-since and its HEAD (both pushed) to exemplar-api's /v1/evaluate, " +
			"authenticated with $EXEMPLAR_TOKEN (an Auth0 service token in CI, or `exemplar login`) at $EXEMPLAR_API_URL, " +
			"so CI holds no model or GitHub credentials.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if remote {
				return remoteEval(ctx, cmd.OutOrStdout(), configDir, since, remoteRepo, rules, jsonOut, markdownOut, wait)
			}
			sel, err := selectRules(ctx, configDir, since, rules)
			if err != nil {
				return err
			}
			snap, eligible, ids := sel.head, sel.eligible, sel.changed
			if len(ids) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no rules to evaluate")
				return nil
			}
			h, err := model.harness(true)
			if err != nil {
				return err
			}
			sc, err := newScanner()
			if err != nil {
				return err
			}
			if workDir == "" {
				if workDir, err = os.MkdirTemp("", "exemplar-eval-*"); err != nil {
					return err
				}
				defer func() { _ = os.RemoveAll(workDir) }()
			}
			files := &evalgen.CheckoutFiles{Root: workDir + "/checkouts"}
			if tok := os.Getenv(tokEnv); tok != "" {
				files.Token = func(context.Context, string) (string, error) { return tok, nil }
			}
			ev := &evalrun.Evaluator{Snapshot: snap, Eligible: eligible, Matcher: compiledRunner(), Files: files, Scanner: sc, WorkDir: workDir + "/cases"}
			if h != nil {
				ev.Engine = engine.New(h, sc, compiledRunner())
			}
			var reports []evalrun.Report
			for _, id := range ids {
				rep, err := evalOne(ctx, ev, snap, os.DirFS(configDir), id)
				if err != nil {
					return err
				}
				reports = append(reports, rep)
			}
			if jsonOut != "" {
				data, err := json.MarshalIndent(reports, "", "  ")
				if err != nil {
					return fmt.Errorf("encode eval reports: %w", err)
				}
				if err := os.WriteFile(jsonOut, append(data, '\n'), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
					return err
				}
			}
			md := evalMarkdown(reports, snap.Global.EvalThresholds)
			if markdownOut != "" {
				if err := os.WriteFile(markdownOut, []byte(md), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
					return err
				}
			}
			fmt.Fprint(cmd.OutOrStdout(), md)
			for _, r := range reports {
				if !r.Pass {
					return errors.New("one or more rules are below eval_thresholds")
				}
			}
			return nil
		},
	}
	model.register(cmd)
	cmd.Flags().StringVar(&configDir, "config", ".", "exemplar-config checkout")
	cmd.Flags().StringVar(&since, "changed-since", "", "evaluate the rules changed since this git revision (a config PR's base)")
	cmd.Flags().StringSliceVar(&rules, "rules", nil, "rule ids to evaluate")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "scratch directory (default: a temp dir)")
	cmd.Flags().StringVar(&jsonOut, "json", "", "write per-rule reports as JSON here")
	cmd.Flags().StringVar(&markdownOut, "markdown", "", "write the summary table here (e.g. $GITHUB_STEP_SUMMARY)")
	cmd.Flags().StringVar(&tokEnv, "token-env", "EXEMPLAR_EVAL_TOKEN", "environment variable with a read-only token for case repos")
	cmd.Flags().BoolVar(&remote, "remote", false, "evaluate in exemplar-worker through /v1/evaluate (mode: eval); needs --changed-since")
	cmd.Flags().StringVar(&remoteRepo, "repo", "", "exemplar-config's owner/name, with --remote (default: from the origin remote)")
	cmd.Flags().DurationVar(&wait, "wait", time.Hour, "how long to wait for a --remote evaluation")
	return cmd
}

// evalOne evaluates one rule (evalrun.EvaluateRule).
func evalOne(ctx context.Context, ev *evalrun.Evaluator, snap *config.Snapshot, cfg fs.FS, id string) (evalrun.Report, error) {
	return evalrun.EvaluateRule(ctx, ev, snap, cfg, id)
}

// selection is the config a check runs on and the rules it covers.
type selection struct {
	head *config.Snapshot
	// base is the snapshot at --changed-since (nil without it).
	base *config.Snapshot
	// eligible is the base branch's check when there is one, so a config
	// PR cannot un-denylist a repo to run on it.
	eligible func(repo string) bool
	changed  []string // sorted, unique rule ids
}

func selectRules(ctx context.Context, configDir, since string, rules []string) (selection, error) {
	snap, err := config.LoadSnapshot(os.DirFS(configDir), configDir)
	if err != nil {
		return selection{}, fmt.Errorf("config: %w", err)
	}
	sel := selection{head: snap, eligible: func(repo string) bool { return snap.CheckEligible(repo) == nil }}
	ids := slices.Clone(rules)
	if since != "" {
		base, err := config.GitFS(ctx, configDir, since)
		if err != nil {
			return selection{}, fmt.Errorf("base config at %s: %w", since, err)
		}
		if sel.base, err = config.LoadSnapshot(base, since); err != nil {
			return selection{}, fmt.Errorf("base config at %s: %w", since, err)
		}
		baseSnap := sel.base
		sel.eligible = func(repo string) bool { return baseSnap.CheckEligible(repo) == nil }
		changed, err := changedPaths(ctx, configDir, since)
		if err != nil {
			return selection{}, err
		}
		ids = append(ids, changedRules(snap, changed)...)
	}
	slices.Sort(ids)
	sel.changed = slices.Compact(ids)
	return sel, nil
}

// changedPaths lists paths changed between rev and HEAD in dir.
func changedPaths(ctx context.Context, dir, rev string) ([]string, error) {
	if strings.HasPrefix(rev, "-") {
		return nil, fmt.Errorf("bad revision %q", rev)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--name-only", "--no-renames", rev+"...HEAD", "--").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s...HEAD: %w", rev, err)
	}
	return strings.Fields(string(out)), nil
}

// changedRules maps changed paths to rule ids (evalrun.ChangedRules).
func changedRules(snap *config.Snapshot, changed []string) []string {
	return evalrun.ChangedRules(snap, changed)
}

func evalMarkdown(reports []evalrun.Report, t schema.EvalThresholds) string {
	return evalrun.Markdown(reports, t)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// remoteEval is `exemplar eval --remote`: mode eval on exemplar-api.
func remoteEval(ctx context.Context, out io.Writer, configDir, since, repo string, rules []string, jsonOut, markdownOut string, wait time.Duration) error {
	if since == "" {
		return errors.New("--remote needs --changed-since: the config PR's base")
	}
	creds, err := loadCredentials(ctx)
	if err != nil {
		return err
	}
	if repo == "" {
		u, err := runGit(ctx, "-C", configDir, "remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("%w (or pass --repo)", err)
		}
		m := remoteRE.FindStringSubmatch(u)
		if m == nil {
			return fmt.Errorf("cannot read owner/name from origin %q; pass --repo", u)
		}
		repo = m[1]
	}
	base, err := runGit(ctx, "-C", configDir, "merge-base", "HEAD", since)
	if err != nil {
		return err
	}
	head, err := runGit(ctx, "-C", configDir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	req := evaluate.Request{Mode: evaluate.ModeEval, Repo: repo, BaseSHA: base, HeadSHA: head, Rules: rules}
	if err := req.Validate(); err != nil {
		return err
	}
	c := &http.Client{Timeout: 30 * time.Second}
	var st evaluate.Status
	if err := apiCall(ctx, c, creds, http.MethodPost, "/v1/evaluate", req, &st); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for !st.Final() {
		if time.Now().After(deadline) {
			return fmt.Errorf("the evaluation is still running after %s; its id is %s", wait, st.ID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(remotePoll):
		}
		if err := apiCall(ctx, c, creds, http.MethodGet, "/v1/evaluate/"+st.ID, nil, &st); err != nil {
			return err
		}
	}
	if st.Status != evaluate.StatusDone {
		return fmt.Errorf("not evaluated: %s", st.Reason)
	}
	if st.Result == nil || st.Result.Eval == nil {
		return errors.New("exemplar-api returned no eval result")
	}
	ev := st.Result.Eval
	if jsonOut != "" {
		// Indented like the local --json.
		var b bytes.Buffer
		if err := json.Indent(&b, ev.Reports, "", "  "); err != nil {
			return fmt.Errorf("eval reports: %w", err)
		}
		if err := os.WriteFile(jsonOut, append(b.Bytes(), '\n'), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
			return err
		}
	}
	if markdownOut != "" {
		if err := os.WriteFile(markdownOut, []byte(ev.Markdown), 0o644); err != nil { //nolint:gosec // G306: a report the user asked for; an ordinary readable output file
			return err
		}
	}
	fmt.Fprint(out, ev.Markdown)
	if !ev.Pass {
		return errors.New("one or more rules are below eval_thresholds")
	}
	return nil
}

// remotePoll is how often --remote checks on the evaluation.
var remotePoll = 10 * time.Second
