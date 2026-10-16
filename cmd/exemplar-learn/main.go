// Command exemplar-learn runs the learning plane's batch jobs.
// Subcommands:
//
//	exemplar-learn ingest --config DIR [--repos a/b,c/d] [--once]
//	exemplar-learn path-events --config DIR [--incidents FEED.jsonl]
//	exemplar-learn extract [--once] [--poll 5m]
//	exemplar-learn evals [--no-inject]
//	exemplar-learn compile --config DIR [--limit 20]
//	exemplar-learn propose [--dry-run]
//	exemplar-learn demote --config DIR [--dry-run] [--all]
//	exemplar-learn expiry --config DIR [--dry-run]
//	exemplar-learn digest --config DIR [--dry-run]
//	exemplar-learn teach --config DIR [--dry-run]
//	exemplar-learn export --config DIR [--dry-run]
//
// ingest: mines review-thread roots from eligible repos (all opted-in
// repos by default) over GraphQL with a read-only ingest token, resuming
// from each repo's saved cursor and waiting out rate limits unless --once.
// Needs EXEMPLAR_DATABASE_URL, EXEMPLAR_PSEUDONYM_KEY (at least 32 bytes,
// from the KMS-held secret) and the EXEMPLAR_GH_* app settings.
//
// path-events: records paths touched by reverts on default branches and
// by PRs or commits an incident feed links. Reverts are scanned
// from each repo's saved point with the ingest token; only refs (PR number,
// SHA, incident id) are stored in path_events. Unless --no-seeds, a
// reverted PR's hunks no reviewer commented on also become revert seeds
// for the extractor, with ingest's data handling. Same environment as
// ingest (the pseudonym key is needed only for seeds).
//
// extract: each pass collects every finished extraction batch, then submits
// one new batch of pending comments. --once does a single pass (a CronJob);
// without it the job loops until stopped. Needs EXEMPLAR_DATABASE_URL,
// EXEMPLAR_GATEWAY_URL and EXEMPLAR_GATEWAY_TOKEN.
//
// evals: generates eval cases for new candidates: positives from source
// comments, negatives from CODEOWNER-approved PRs, and (unless --no-inject)
// injected violations validated in code. Injection needs the gateway and
// reads repos with read-only tokens when EXEMPLAR_GH_* is set.
//
// compile: asks the high-tier model for an ast-grep or Semgrep rule per
// candidate, checks it in code and with the engine, runs it in a sandbox
// (Linux unshare + prlimit; required unless --require-sandbox=false) over
// the candidate's eval cases, and records every attempt. Accepted compiles
// make the candidate PR kind: compiled. Needs both engines on PATH, the
// gateway, and EXEMPLAR_GH_* to read case files.
//
// propose: opens draft PRs in exemplar-config for the top candidates, at
// most 3 open per owner team. Needs EXEMPLAR_DATABASE_URL and, unless
// --dry-run, the review app's EXEMPLAR_GH_APP_ID, EXEMPLAR_GH_INSTALLATION_ID,
// EXEMPLAR_GH_PRIVATE_KEY_FILE and EXEMPLAR_CONFIG_REPO; it writes with the
// config-writer token, which can reach exemplar-config only.
//
// demote: nightly auto-demotion. Over each rule's last 30 judged findings
// (the finding_outcomes view), a rule whose weighted eFP exceeds the bar,
// whose precision lower bound is under the floor, and whose negatives
// include an owner adjudication or two non-author people, is set to
// shadow by a config PR it opens and merges, naming the owner. Same
// environment as propose; --dry-run needs only the database.
//
// expiry: daily rule expiry. An active rule past review_by gets a PR
// extending it (its last 90 days of feedback under the eFP bar), for the
// owner to merge, or a PR to shadow that merges after
// expiry.auto_demote_after_days unless a person responds.
//
// digest: the weekly rule-health issue for each rule owner in
// exemplar-config, from the same views as the dashboards. One issue per
// owner and week; EXEMPLAR_GH_WEB_URL sets the link base (GHES). The
// --platform-team (default @platform/devex) also gets one listing repos
// whose rule exports are more than two config versions behind.
//
// teach: nightly teach-the-bot. A rejection class that recurs for a rule
// (3 times in 30 days, or once from its owner) becomes a draft config PR
// excluding the paths "not important" dismissals cluster under, or an
// owner review issue (exception, rule_wrong, disagree).
//
// export: daily rule exports. Each repo with exports.targets gets its
// active rules' export summaries rendered between BEGIN/END exemplar
// markers in AGENTS.md, CLAUDE.md, Copilot or Cursor instruction files, on
// one exemplar/exports branch and PR, opened only when the rendering
// changed. A block someone edited by hand is left alone and reported.
// Writes with the separate exports app (EXEMPLAR_EXPORTS_APP_ID,
// EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE): contents and pull requests on one
// repo per token, never exemplar-config.
//
// Every step is idempotent, so a crashed or repeated run never loses or
// duplicates work.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/koblas/exemplar/internal/learn/extract"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/telemetry"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: exemplar-learn ingest|path-events|extract|evals|compile|propose|demote|expiry|digest|teach|export [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = runIngest(os.Args[2:])
	case "path-events":
		err = runPathEvents(os.Args[2:])
	case "extract":
		err = runExtract(os.Args[2:])
	case "compile":
		err = runCompile(os.Args[2:])
	case "evals":
		err = runEvals(os.Args[2:])
	case "propose":
		err = runPropose(os.Args[2:])
	case "demote":
		err = runDemote(os.Args[2:])
	case "expiry":
		err = runExpiry(os.Args[2:])
	case "digest":
		err = runDigest(os.Args[2:])
	case "teach":
		err = runTeach(os.Args[2:])
	case "export":
		err = runExport(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: exemplar-learn ingest|path-events|extract|evals|compile|propose|demote|expiry|digest|teach|export [flags]")
		os.Exit(2)
	}
	if err != nil {
		slog.Error("exemplar-learn failed", "command", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func runExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	once := fs.Bool("once", false, "do one collect+submit pass and exit")
	poll := fs.Duration("poll", 5*time.Minute, "time between passes when looping")
	perBatch := fs.Int("requests-per-batch", 10_000, "requests per Message Batch (3 comments each)")
	model := fs.String("model", "claude-sonnet-5-5", "pinned extractor model (spec §8.2)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	log, shutdown, err := telemetry.Setup(ctx, "exemplar-learn")
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	dbURL := os.Getenv("EXEMPLAR_DATABASE_URL")
	if dbURL == "" {
		return errors.New("EXEMPLAR_DATABASE_URL is required")
	}
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	sc, err := redact.New()
	if err != nil {
		return err
	}
	d, err := extract.New(extract.Config{
		GatewayURL: os.Getenv("EXEMPLAR_GATEWAY_URL"), GatewayToken: os.Getenv("EXEMPLAR_GATEWAY_TOKEN"),
		Model: *model, RequestsPerBatch: *perBatch, Logger: log,
	}, st, sc)
	if err != nil {
		return err
	}

	for {
		if err := pass(ctx, d); err != nil {
			if *once || ctx.Err() != nil {
				return err
			}
			// A failed pass leaves state consistent; try again next time.
			log.Error("extraction pass failed", "err", err)
		}
		if *once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*poll):
		}
	}
}

func pass(ctx context.Context, d *extract.Driver) error {
	collected, err := d.Collect(ctx)
	if err != nil {
		return err
	}
	submitted, err := d.Submit(ctx)
	if err != nil {
		return err
	}
	printJSON(map[string]any{"collected": collected, "submitted": submitted})
	return nil
}
