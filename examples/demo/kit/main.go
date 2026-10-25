// Command kit drives the end-to-end demo against a sandbox GitHub org
// (docs/plans/demo.md, examples/demo/README.md). It is not part of the
// product: it plays the demo's three people through the GitHub API, one
// phase at a time, and every phase can be re-run.
//
//	go run ./examples/demo/kit -phase <phase> -org <org> -repo <repo>
//
// Phases, in order: repo (push the sandbox repository's base tree),
// config (render the demo's exemplar-config into -out), seed (the merged
// review history the learning plane mines), prs (the beat A and B PRs),
// feedback (beat D's volume of rejected findings), teach (the owner's "not
// important here"). -dry-run prints the plan and calls nothing.
//
// Tokens come from the environment and are never logged:
// EXEMPLAR_DEMO_AUTHOR_TOKEN (opens and merges PRs), EXEMPLAR_DEMO_OWNER_TOKEN
// (the rule owner, the first reviewer) and EXEMPLAR_DEMO_REVIEWER_TOKEN
// (the second reviewer). Each needs repo scope on the sandbox repository.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-github/v88/github"
)

var (
	errUsage    = errors.New("usage")
	errAccounts = errors.New("the author, owner and reviewer must be three different accounts")
	errNoToken  = errors.New("token not set")
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "kit:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fl := flag.NewFlagSet("kit", flag.ContinueOnError)
	phase := fl.String("phase", "", "repo, config, seed, prs, feedback or teach")
	org := fl.String("org", "", "the sandbox org")
	repo := fl.String("repo", "ledger", "the sandbox repository, created empty beforehand")
	api := fl.String("api", "", "GitHub API base URL (default github.com; GHES: https://<host>/api/v3/)")
	src := fl.String("config-src", "examples/exemplar-config", "the example exemplar-config tree the demo config is rendered from")
	out := fl.String("out", "out/demo-config", "where the config phase writes the rendered tree")
	base := fl.String("base-sha", "", "the sandbox repository's main for the config phase (default: read it)")
	pace := fl.Duration("pace", 2*time.Second, "pause after each GitHub write")
	target := fl.Int("target", 30, "feedback: findings to mark wrong (demotion needs 30 judged)")
	maxPRs := fl.Int("max-prs", 20, "feedback: most volume PRs to open")
	wait := fl.Duration("wait", 20*time.Minute, "feedback, teach: how long to wait for the bot on each PR")
	dry := fl.Bool("dry-run", false, "print the plan and call nothing")
	if err := fl.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if *org == "" || *phase == "" {
		return fmt.Errorf("%w: -org and -phase are required", errUsage)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *dry {
		return plan(*phase, *target)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r, err := connect(ctx, *api, *org, *repo, *pace, log)
	if err != nil {
		return err
	}
	v := Vars{Org: *org, Repo: *repo, Author: r.author.login, Owner: r.reviewers[0].login, BaseSHA: *base}
	f := Feedback{Target: *target, MaxPRs: *maxPRs, Poll: 30 * time.Second, Wait: *wait, AckWait: 5 * time.Minute}
	switch *phase {
	case "repo":
		_, err = r.pushRepo(ctx, v)
	case "config":
		if v.BaseSHA == "" {
			if v.BaseSHA, err = r.mainSHA(ctx); err != nil {
				return err
			}
			if v.BaseSHA == "" {
				return errEmptyRepo
			}
		}
		if err = writeConfig(os.DirFS(*src), v, *out); err == nil {
			fmt.Fprintf(os.Stdout, "demo config written to %s; push it to the org's exemplar-config repository\n", *out)
		}
	case "seed":
		var seeds []SeedPR
		if seeds, err = loadSeed(); err == nil {
			err = r.seed(ctx, seeds)
		}
	case "prs":
		var prs []DemoPR
		if prs, err = loadPRs(); err == nil {
			err = r.openAll(ctx, prs)
		}
	case "feedback":
		var n int
		n, err = r.feedback(ctx, f)
		fmt.Fprintf(os.Stdout, "%d findings of %s marked wrong\n", n, DemoRule)
	case "teach":
		err = r.teach(ctx, f)
	default:
		return fmt.Errorf("%w: unknown phase %q", errUsage, *phase)
	}
	return err
}

// connect builds the three people's clients from their tokens.
func connect(ctx context.Context, api, org, repo string, pace time.Duration, log *slog.Logger) (*repoClient, error) {
	r := &repoClient{org: org, repo: repo, pace: pace, log: log}
	var err error
	if r.author, err = person(ctx, api, "EXEMPLAR_DEMO_AUTHOR_TOKEN"); err != nil {
		return nil, err
	}
	if r.reviewers[0], err = person(ctx, api, "EXEMPLAR_DEMO_OWNER_TOKEN"); err != nil {
		return nil, err
	}
	if r.reviewers[1], err = person(ctx, api, "EXEMPLAR_DEMO_REVIEWER_TOKEN"); err != nil {
		return nil, err
	}
	logins := map[string]bool{strings.ToLower(r.author.login): true, strings.ToLower(r.reviewers[0].login): true, strings.ToLower(r.reviewers[1].login): true}
	if len(logins) != 3 {
		return nil, errAccounts
	}
	return r, nil
}

func person(ctx context.Context, api, env string) (account, error) {
	tok := os.Getenv(env)
	if tok == "" {
		return account{}, fmt.Errorf("%w: %s", errNoToken, env)
	}
	opts := []github.ClientOptionsFunc{github.WithAuthToken(tok)}
	if api != "" {
		opts = append(opts, github.WithURLs(&api, &api))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return account{}, fmt.Errorf("github client: %w", err)
	}
	u, _, err := c.Users.Get(ctx, "")
	if err != nil {
		return account{}, fmt.Errorf("%s: who am I: %w", env, err)
	}
	return account{login: u.GetLogin(), c: c}, nil
}

// plan prints what a phase would do, from the embedded data alone.
func plan(phase string, target int) error {
	switch phase {
	case "repo":
		files, err := tree("_repo", Vars{Owner: "<owner>"})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "push %d files to main: %s\n", len(files), strings.Join(sortedKeys(files), ", "))
	case "config":
		fmt.Fprintln(os.Stdout, "render examples/exemplar-config with the demo overlay: the repo file, the demo-todo-comments rule and its eval case,")
		fmt.Fprintln(os.Stdout, "and global.yaml with auto_approve, a claude/openai categorizer, openai first among verifiers and baseline.sample_rate 1")
	case "seed":
		seeds, err := loadSeed()
		if err != nil {
			return err
		}
		for _, s := range seeds {
			fmt.Fprintf(os.Stdout, "open, review (%d comments) and merge %s: %s\n", len(s.Comments), s.Branch, s.Title)
			for _, c := range s.Comments {
				line, err := lineOf(s.Files[c.Path], c.Match)
				if err != nil {
					return fmt.Errorf("%s: %w", s.Branch, err)
				}
				fmt.Fprintf(os.Stdout, "  reviewer %d on %s:%d\n", c.Reviewer, c.Path, line)
			}
		}
	case "prs":
		prs, err := loadPRs()
		if err != nil {
			return err
		}
		for _, p := range prs {
			fmt.Fprintf(os.Stdout, "beat %s: open %s: %s (%s)\n", p.Beat, p.Branch, p.Title, strings.Join(sortedKeys(p.Files), ", "))
		}
	case "feedback":
		fmt.Fprintf(os.Stdout, "open volume PRs (%d TODOs each, %s) until %d %s findings are marked wrong by both reviewers in turn\n",
			TodosPerVolumePR, volumePR(1).Branch+"...", target, DemoRule)
	case "teach":
		fmt.Fprintf(os.Stdout, "open %s; the owner replies %q on its %s finding\n", teachPR().Branch, TeachReason, DemoRule)
	default:
		return fmt.Errorf("%w: unknown phase %q", errUsage, phase)
	}
	return nil
}
