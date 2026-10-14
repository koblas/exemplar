// Command exemplar-search serves repository context (spec §6.4).
//
//	exemplar-search serve --config DIR --root /mnt/ssd/mirrors --index-root /mnt/ssd/index [--addr :8080]
//	exemplar-search bench --repo <url> --work-dir /mnt/ssd/bench --out report.json --markdown report.md
//
// serve keeps the bare git mirrors of every repo in scope (opted-in repos
// and their context.extra_repos, never denylisted ones), rebuilds a zoekt
// index of each default branch after every fetch, and answers the internal
// API: snapshot, read_file, log, search_text, find_definition, and fetch
// requests relayed from push webhooks. Callers send EXEMPLAR_SEARCH_TOKEN
// as a bearer token.
// Symbols need universal-ctags on PATH (--require-ctags in production).
// Mirrors are fetched with context-flavor tokens from the review app's
// EXEMPLAR_GH_APP_ID, EXEMPLAR_GH_INSTALLATION_ID and
// EXEMPLAR_GH_PRIVATE_KEY_FILE (EXEMPLAR_GH_API_URL for GHES), with
// EXEMPLAR_CONFIG_REPO naming the repo those tokens may never read.
//
// bench sizes a node (acceptance test T-0.4).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/koblas/exemplar/internal/search/bench"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: exemplar-search serve|bench [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "bench":
		err = runBench(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: exemplar-search serve|bench [flags]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	var cfg bench.Config
	var sparse, depths, queriesFile, out, md string
	fs.StringVar(&cfg.RepoURL, "repo", "", "monorepo clone URL or path (required)")
	fs.StringVar(&cfg.Branch, "branch", "main", "default branch")
	fs.StringVar(&cfg.WorkDir, "work-dir", "", "scratch directory on SSD comparable to an exemplar-search node (required)")
	fs.StringVar(&sparse, "sparse-dirs", "", "comma-separated dirs a typical PR touches")
	fs.StringVar(&depths, "incremental-depths", "1,10,100", "commits back for incremental index runs")
	fs.StringVar(&queriesFile, "queries", "", "file with one zoekt query per line (default: built-in set)")
	fs.IntVar(&cfg.QueryRepeats, "repeats", 20, "runs per query")
	fs.IntVar(&cfg.CtagsSample, "ctags-sample", 300, "files sampled per language for ctags coverage")
	fs.StringVar(&out, "out", "", "write the JSON report here")
	fs.StringVar(&md, "markdown", "", "write a Markdown summary here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.RepoURL == "" || cfg.WorkDir == "" {
		return fmt.Errorf("--repo and --work-dir are required")
	}
	cfg.Token = os.Getenv("EXEMPLAR_GH_TOKEN")
	if sparse != "" {
		cfg.SparseDirs = strings.Split(sparse, ",")
	}
	for _, d := range strings.Split(depths, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(d))
		if err != nil || n < 1 {
			return fmt.Errorf("bad --incremental-depths entry %q", d)
		}
		cfg.IncrementalDepths = append(cfg.IncrementalDepths, n)
	}
	if queriesFile != "" {
		data, err := os.ReadFile(queriesFile)
		if err != nil {
			return err
		}
		for _, q := range strings.Split(string(data), "\n") {
			if q = strings.TrimSpace(q); q != "" && !strings.HasPrefix(q, "#") {
				cfg.Queries = append(cfg.Queries, q)
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := bench.Run(ctx, cfg)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if out != "" {
		if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
			return err
		}
	} else {
		fmt.Println(string(data))
	}
	if md != "" {
		return os.WriteFile(md, []byte(report.Markdown()), 0o644)
	}
	return nil
}
