// Package docs checks the documentation site against the code, without
// building it: every EXEMPLAR_* variable the Go or Python code reads, every
// exemplar CLI subcommand and every top-level property of a schema in
// schema/ has an entry on its reference page; every public page is in
// mkdocs.yml's nav; and no public page cites an internal reference (spec
// sections, requirement ids, acceptance-test ids, delivery phases).
//
// Run it with go test ./docs; CI's docs job does, next to make docs.
package docs

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// internalDirs are the directories under docs/ the site excludes
// (mkdocs.yml exclude_docs); their pages are design documents.
var internalDirs = []string{"spec", "plans", "security"}

// read returns a file's contents, failing the test when it can't.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// publicPages lists every Markdown page the site publishes, relative to
// docs/, sorted.
func publicPages(t *testing.T) []string {
	t.Helper()
	var pages []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && slices.Contains(internalDirs, p) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".md") {
			pages = append(pages, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 20 {
		t.Fatalf("found only %d public pages", len(pages))
	}
	sort.Strings(pages)
	return pages
}

// mkdocsConfig parses mkdocs.yml. The superfences extension's
// !!python/name tag is replaced by a string first: the decoder stops at
// an unknown tag without an error.
func mkdocsConfig(t *testing.T) ([]any, []any) {
	t.Helper()
	src := regexp.MustCompile(`!!python/name:\S+`).ReplaceAllString(read(t, "../mkdocs.yml"), `"python-name"`)
	var cfg struct {
		Nav     []any `yaml:"nav"`
		Plugins []any `yaml:"plugins"`
	}
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("parse mkdocs.yml: %v", err)
	}
	if len(cfg.Nav) == 0 || len(cfg.Plugins) == 0 {
		t.Fatal("mkdocs.yml: nav or plugins is empty")
	}
	return cfg.Nav, cfg.Plugins
}

// TestEveryPageIsInNav fails when a public page is missing from
// mkdocs.yml's nav, so a new page can't be published unreachable.
func TestEveryPageIsInNav(t *testing.T) {
	nav, _ := mkdocsConfig(t)
	inNav := map[string]bool{}
	var walk func(n any)
	walk = func(n any) {
		switch v := n.(type) {
		case string:
			inNav[v] = true
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(nav)
	for _, p := range publicPages(t) {
		if !inNav[p] {
			t.Errorf("docs/%s is not in mkdocs.yml nav", p)
		}
	}
}

// internalRefs are the references that belong to the design documents,
// not to the public site: the spec's section numbers, PRD requirement
// ids, Phase 0 acceptance tests and delivery phases.
var internalRefs = []*regexp.Regexp{
	regexp.MustCompile(`§`),
	regexp.MustCompile(`\bFR-[A-Z]+\d*\b`),
	regexp.MustCompile(`\bT-0\.\d\b`),
	regexp.MustCompile(`\bPhase \d\b`),
	regexp.MustCompile(`\bPRD\b`),
}

// TestNoInternalReferences fails when a public page cites the spec by
// section, a requirement id, an acceptance test or a delivery phase.
// Link the spec file on GitHub instead where the long form is needed.
func TestNoInternalReferences(t *testing.T) {
	for _, p := range publicPages(t) {
		for i, line := range strings.Split(read(t, p), "\n") {
			for _, re := range internalRefs {
				if re.MatchString(line) {
					t.Errorf("docs/%s:%d cites an internal reference (%s): %s", p, i+1, re, strings.TrimSpace(line))
				}
			}
		}
	}
}

// codeEnvVars returns every EXEMPLAR_* name the Go code in cmd/ and
// internal/ or the Python code in learning/src reads, as deploy's
// manifest test finds them: a quoted name in a non-test source file.
// Names that end in "_" are prefixes (EXEMPLAR_GH_ in a message), not
// variables.
func codeEnvVars(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`"(EXEMPLAR_[A-Z0-9_]+)"`)
	seen := map[string]bool{}
	for _, root := range []string{"../cmd", "../internal", "../learning/src"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".py") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			for _, m := range re.FindAllStringSubmatch(read(t, p), -1) {
				if !strings.HasSuffix(m[1], "_") {
					seen[m[1]] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	if len(names) < 10 {
		t.Fatalf("found only %d env names", len(names))
	}
	sort.Strings(names)
	return names
}

// TestEnvironmentVariablesDocumented fails when the code reads an
// EXEMPLAR_* variable that reference/environment-variables.md doesn't
// name in backticks.
func TestEnvironmentVariablesDocumented(t *testing.T) {
	page := read(t, "reference/environment-variables.md")
	for _, name := range codeEnvVars(t) {
		if !strings.Contains(page, "`"+name+"`") {
			t.Errorf("%s is read by the code but not on docs/reference/environment-variables.md", name)
		}
	}
}

// cliCommands returns every cobra command's name under cmd/exemplar:
// the first word of each Use: field, with "exemplar" itself left out.
func cliCommands(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`\bUse:\s*"([a-z][a-z0-9-]*)`)
	seen := map[string]bool{}
	files, err := filepath.Glob("../cmd/exemplar/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(read(t, f), -1) {
			if m[1] != "exemplar" {
				seen[m[1]] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	if len(names) < 10 {
		t.Fatalf("found only %d CLI commands", len(names))
	}
	sort.Strings(names)
	return names
}

// TestCLICommandsDocumented fails when a subcommand of cmd/exemplar has
// no heading on reference/cli.md. A heading documents a command when it
// reads "exemplar <name>" or "exemplar <parent> <name>".
func TestCLICommandsDocumented(t *testing.T) {
	var headings []string
	for line := range strings.SplitSeq(read(t, "reference/cli.md"), "\n") {
		if strings.HasPrefix(line, "#") {
			headings = append(headings, line)
		}
	}
	for _, name := range cliCommands(t) {
		re := regexp.MustCompile(`exemplar( [a-z-]+)? ` + regexp.QuoteMeta(name) + `\b`)
		if !slices.ContainsFunc(headings, re.MatchString) {
			t.Errorf("exemplar %s has no heading on docs/reference/cli.md", name)
		}
	}
}

// TestSchemaPropertiesDocumented fails when a top-level property of a
// schema in schema/ is not named in backticks on its page,
// reference/config/<name>.md.
func TestSchemaPropertiesDocumented(t *testing.T) {
	files, err := filepath.Glob("../schema/*.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 5 {
		t.Fatalf("found only %d schemas", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".schema.json")
		page := filepath.Join("reference", "config", name+".md")
		t.Run(name, func(t *testing.T) {
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal([]byte(read(t, f)), &schema); err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			if len(schema.Properties) == 0 {
				t.Fatalf("%s has no properties", f)
			}
			b, err := os.ReadFile(page)
			if err != nil {
				t.Fatalf("schema/%s.schema.json has no page docs/%s: %v", name, page, err)
			}
			for prop := range schema.Properties {
				if !strings.Contains(string(b), "`"+prop+"`") {
					t.Errorf("schema/%s.schema.json property %q is not on docs/%s", name, prop, page)
				}
			}
		})
	}
}

// TestRedirectsResolve fails when a redirect in mkdocs.yml points at a
// page that doesn't exist, so a moved page keeps its old URL working.
func TestRedirectsResolve(t *testing.T) {
	_, plugins := mkdocsConfig(t)
	var maps map[string]any
	for _, p := range plugins {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if r, ok := m["redirects"].(map[string]any); ok {
			maps, _ = r["redirect_maps"].(map[string]any)
		}
	}
	if len(maps) == 0 {
		t.Fatal("mkdocs.yml has no redirects plugin with redirect_maps")
	}
	pages := publicPages(t)
	for from, to := range maps {
		target, _ := to.(string)
		if !slices.Contains(pages, target) {
			t.Errorf("redirect %s -> %s: target is not a public page", from, target)
		}
		if slices.Contains(pages, from) {
			t.Errorf("redirect %s: the source still exists as a page", from)
		}
		if _, err := os.Stat(from); err == nil {
			t.Errorf("redirect %s: a file with that name exists and would shadow it", from)
		}
	}
}
