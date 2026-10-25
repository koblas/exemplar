package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/goccy/go-yaml"
)

// data holds the sandbox repository (data/_repo), the demo's config
// overlay (data/_config), the seed history and the PR set. Files ending
// in .tmpl are rendered with Vars and lose the suffix.
//
//go:embed all:data
var data embed.FS

// Vars are the names the templates and the config render fill in.
type Vars struct {
	Org, Repo string
	// Author opens every PR; Owner is the rule owner and CODEOWNERS
	// owner, and the first reviewer.
	Author, Owner string
	// BaseSHA is the sandbox repository's main after the base push,
	// which the demo rule's eval case is synthesised against.
	BaseSHA string
}

// SeedPR is one merged PR of the seed history.
type SeedPR struct {
	Branch   string            `yaml:"branch"`
	Title    string            `yaml:"title"`
	Body     string            `yaml:"body"`
	Files    map[string]string `yaml:"files"`
	Comments []SeedComment     `yaml:"comments"`
}

// SeedComment is a reviewer's inline comment on a seed PR. Match is a
// substring of exactly one line of the file: the line it anchors to.
type SeedComment struct {
	Reviewer int    `yaml:"reviewer"`
	Path     string `yaml:"path"`
	Match    string `yaml:"match"`
	Body     string `yaml:"body"`
}

// DemoPR is a PR the demo opens live.
type DemoPR struct {
	Beat   string            `yaml:"beat"`
	Branch string            `yaml:"branch"`
	Title  string            `yaml:"title"`
	Body   string            `yaml:"body"`
	Files  map[string]string `yaml:"files"`
	// Approve says what beat B expects: true for an auto-approval.
	Approve *bool `yaml:"approve,omitempty"`
}

func loadYAML[T any](name string) (T, error) {
	var v T
	b, err := data.ReadFile("data/" + name)
	if err != nil {
		return v, fmt.Errorf("read %s: %w", name, err)
	}
	if err := yaml.UnmarshalWithOptions(b, &v, yaml.Strict()); err != nil {
		return v, fmt.Errorf("decode %s: %w", name, err)
	}
	return v, nil
}

func loadSeed() ([]SeedPR, error) { return loadYAML[[]SeedPR]("seed.yaml") }

func loadPRs() ([]DemoPR, error) { return loadYAML[[]DemoPR]("prs.yaml") }

// tree renders the embedded directory dir (relative to data/) into
// path -> content, rendering .tmpl files with v.
func tree(dir string, v Vars) (map[string]string, error) {
	root := "data/" + dir
	out := map[string]string{}
	err := fs.WalkDir(data, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := data.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel := strings.TrimPrefix(p, root+"/")
		if name, ok := strings.CutSuffix(rel, ".tmpl"); ok {
			t, err := template.New(rel).Option("missingkey=error").Parse(string(b))
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			var buf bytes.Buffer
			if err := t.Execute(&buf, v); err != nil {
				return fmt.Errorf("render %s: %w", rel, err)
			}
			rel, b = name, buf.Bytes()
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("demo data %s: %w", dir, err)
	}
	return out, nil
}

var errMatch = errors.New("seed comment anchor")

// lineOf is the 1-based line of content containing match, which must
// occur on exactly one line.
func lineOf(content, match string) (int, error) {
	found := 0
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(l, match) {
			if found != 0 {
				return 0, fmt.Errorf("%w: %q matches lines %d and %d", errMatch, match, found, i+1)
			}
			found = i + 1
		}
	}
	if found == 0 {
		return 0, fmt.Errorf("%w: %q matches no line", errMatch, match)
	}
	return found, nil
}

// DemoRule is the noisy rule beat D demotes and teaches.
const DemoRule = "demo-todo-comments"

// TodosPerVolumePR is how many TODOs each volume PR adds, one per file:
// within posting.class_threshold and comment_budget.per_file, so each
// posts as its own comment.
const TodosPerVolumePR = 3

// volumePR is the nth PR that feeds the demo rule's findings (beat D).
func volumePR(n int) DemoPR {
	files := map[string]string{}
	for i := range TodosPerVolumePR {
		name := fmt.Sprintf("internal/jobs/job%02d/step%d.go", n, i+1)
		files[name] = fmt.Sprintf(`// Package job%02d runs scheduled job %d.
package job%02d

// Step%d runs step %d of the job.
func Step%d() error {
	// TODO: retry this step when the database is busy.
	return nil
}
`, n, n, n, i+1, i+1, i+1)
	}
	return DemoPR{
		Beat: "D", Branch: fmt.Sprintf("demo/job-%02d", n),
		Title: fmt.Sprintf("Add scheduled job %d", n), Body: "Another nightly job for the reconciliation run.", Files: files,
	}
}

// TeachPrefix is where the owner says the demo rule does not matter.
const TeachPrefix = "internal/legacy"

// teachPR adds a TODO under TeachPrefix, for the owner's "not important
// here" (beat D, teach-the-bot).
func teachPR() DemoPR {
	return DemoPR{
		Beat: "D", Branch: "demo/legacy-export", Title: "Export the legacy report as CSV",
		Body: "Finance asked for a CSV of the old monthly report.",
		Files: map[string]string{path.Join(TeachPrefix, "export.go"): `package legacy

import "strings"

// CSV joins report lines with commas.
func CSV(lines []string) string {
	// TODO: quote fields that contain commas.
	return strings.Join(lines, ",")
}
`},
	}
}

// sortedKeys returns m's keys in order, so writes are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
