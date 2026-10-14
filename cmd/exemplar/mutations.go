package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/mutate"
	"github.com/koblas/exemplar/internal/schema"
)

func newMutationsCmd() *cobra.Command {
	var configDir, checkout, repo, sha string
	var paths []string
	var perOp, maxPerClass int
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "mutations",
		Short: "Write bug-class recall cases by mutating clean code (spec §8.8)",
		Long: "Applies deterministic mutation operators (drop an error check, flip < to <=, drop a Close or free, " +
			"remove a lock/unlock pair or guard, drop a nil guard) to Go, C and C++ files of a checkout in the repo's bug_lane.critical_paths " +
			"(or --paths), and merges the cases into evals/<bug class>/cases.yaml of an exemplar-config checkout, with a " +
			"patch per case. Code stays referenced by repo@sha; review and commit the result.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMutations(cmd.Context(), cmd.OutOrStdout(), configDir, checkout, repo, sha, paths, perOp, maxPerClass, dryRun)
		},
	}
	cmd.Flags().StringVar(&configDir, "config", "", "exemplar-config checkout to write into (required)")
	cmd.Flags().StringVar(&checkout, "checkout", "", "checkout of the target repo at the commit to reference (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "the target repo, owner/name (required)")
	cmd.Flags().StringVar(&sha, "sha", "", "the checkout's commit (default: its HEAD)")
	cmd.Flags().StringSliceVar(&paths, "paths", nil, "globs of files to mutate (default: the repo's bug_lane.critical_paths)")
	cmd.Flags().IntVar(&perOp, "per-file", 1, "mutations per operator per file")
	cmd.Flags().IntVar(&maxPerClass, "max-per-class", 20, "new cases per bug class")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be written; write nothing")
	_ = cmd.MarkFlagRequired("config")
	_ = cmd.MarkFlagRequired("checkout")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

var cTestFile = regexp.MustCompile(`(_test|_unittest|_tests)\.(c|cc|cpp|cxx)$`)

// mutationLang is the operators a file gets: "go", "c" (C and C++), or ""
// for none. Tests are not mutated.
func mutationLang(p string) string {
	switch ext := path.Ext(p); {
	case ext == ".go" && !strings.HasSuffix(p, "_test.go"):
		return "go"
	case slices.Contains([]string{".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx"}, ext) && !cTestFile.MatchString(p):
		return "c"
	}
	return ""
}

func runMutations(ctx context.Context, out io.Writer, configDir, checkout, repo, sha string, globs []string, perOp, maxPerClass int, dryRun bool) error {
	snap, err := config.LoadSnapshot(os.DirFS(configDir), configDir)
	if err != nil {
		return err
	}
	if len(globs) == 0 {
		if rc, ok := snap.Repos[repo]; ok && rc.BugLane != nil {
			globs = rc.BugLane.CriticalPaths
		}
	}
	if len(globs) == 0 {
		return fmt.Errorf("%s has no bug_lane.critical_paths; pass --paths", repo)
	}
	if sha == "" {
		head, err := exec.CommandContext(ctx, "git", "-C", checkout, "rev-parse", "HEAD").Output()
		if err != nil {
			return fmt.Errorf("read the checkout's HEAD: %w", err)
		}
		sha = strings.TrimSpace(string(head))
	}
	listed, err := exec.CommandContext(ctx, "git", "-C", checkout, "ls-files", "-z").Output()
	if err != nil {
		return fmt.Errorf("list the checkout's files: %w", err)
	}
	enabled := map[string]bool{}
	if bl := snap.Global.BugLane; bl != nil {
		for id, f := range bl.Classes {
			enabled[id] = f.Enabled
		}
	}
	byClass := map[string][]schema.EvalCase{}
	patches := map[string]string{}
	for _, p := range strings.Split(strings.TrimSuffix(string(listed), "\x00"), "\x00") {
		lang := mutationLang(p)
		if lang == "" || strings.HasPrefix(p, "vendor/") || strings.HasPrefix(p, "third_party/") ||
			!slices.ContainsFunc(globs, func(g string) bool { return doublestar.MatchUnvalidated(g, p) }) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		var ms []mutate.Mutation
		if lang == "go" {
			ms, err = mutate.Go(p, src, perOp)
		} else {
			ms, err = mutate.C(ctx, p, src, perOp)
		}
		if err != nil {
			fmt.Fprintf(out, "skip %s: %v\n", p, err)
			continue
		}
		lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
		for _, m := range ms {
			if !enabled[m.Class] || len(byClass[m.Class]) >= maxPerClass {
				continue
			}
			name := fmt.Sprintf("mut-%s-%s-%d", m.Op, strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(p), "-"), "-"), m.Expect[0])
			patch := "patches/" + name + ".diff"
			byClass[m.Class] = append(byClass[m.Class], schema.EvalCase{Name: name, Ref: repo + "@" + sha, Synthetic: true, Patch: patch,
				Expect: []schema.Expectation{{Path: p, Lines: []int{m.Expect[0], m.Expect[1]}}}})
			patches[m.Class+"/"+patch] = m.Diff(lines)
		}
	}
	classes := make([]string, 0, len(byClass))
	for c := range byClass {
		classes = append(classes, c)
	}
	slices.Sort(classes)
	for _, class := range classes {
		dir := filepath.Join(configDir, "evals", class)
		file := filepath.Join(dir, "cases.yaml")
		doc := schema.EvalCases{Rule: class}
		if data, err := os.ReadFile(file); err == nil {
			if err := schema.Decode(schema.DocEvalCases, data, &doc); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		added := 0
		for _, c := range byClass[class] {
			if slices.ContainsFunc(doc.Cases, func(e schema.EvalCase) bool { return e.Name == c.Name }) {
				continue
			}
			doc.Cases = append(doc.Cases, c)
			added++
			if !dryRun {
				pf := filepath.Join(dir, filepath.FromSlash(c.Patch))
				if err := os.MkdirAll(filepath.Dir(pf), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(pf, []byte(patches[class+"/"+c.Patch]), 0o644); err != nil {
					return err
				}
			}
		}
		data, err := renderCases(doc)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d new cases (%d in all)\n", class, added, len(doc.Cases))
		if dryRun || added == 0 {
			continue
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

type casesYAML struct {
	Rule  string     `yaml:"rule"`
	Cases []caseYAML `yaml:"cases"`
}

type caseYAML struct {
	Name      string       `yaml:"name"`
	Ref       string       `yaml:"ref"`
	Synthetic bool         `yaml:"synthetic,omitempty"`
	Patch     string       `yaml:"patch,omitempty"`
	Paths     []string     `yaml:"paths,omitempty,flow"`
	Expect    []expectYAML `yaml:"expect"`
}

type expectYAML struct {
	Path  string `yaml:"path"`
	Lines []int  `yaml:"lines,flow"`
}

// renderCases writes cases.yaml, checked against its schema.
func renderCases(doc schema.EvalCases) ([]byte, error) {
	y := casesYAML{Rule: doc.Rule}
	for _, c := range doc.Cases {
		cy := caseYAML{Name: c.Name, Ref: c.Ref, Synthetic: c.Synthetic, Patch: c.Patch, Paths: c.Paths, Expect: []expectYAML{}}
		for _, e := range c.Expect {
			cy.Expect = append(cy.Expect, expectYAML{Path: e.Path, Lines: e.Lines})
		}
		y.Cases = append(y.Cases, cy)
	}
	data, err := yaml.Marshal(y)
	if err != nil {
		return nil, err
	}
	data = append([]byte("# Bug-class recall cases; mutation cases come from `exemplar mutations` (spec §8.8).\n"), data...)
	var check schema.EvalCases
	if err := schema.Decode(schema.DocEvalCases, data, &check); err != nil {
		return nil, fmt.Errorf("generated cases are invalid: %w", err)
	}
	return data, nil
}
