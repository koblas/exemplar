package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/goccy/go-yaml"
)

var errExample = errors.New("example config")

// renderConfig builds the demo's exemplar-config tree: the example tree
// (src) without its own repo files, the demo overlay, and global.yaml with
// what the beats need switched on (docs/plans/demo.md §1):
//   - auto_approve on, so beat B can approve and the check run lists every
//     gate reason;
//   - the Stage 2 categorizer on two families (claude and openai) and the
//     verifier on openai first, so verification is cross-family;
//   - every head sampled for the baseline, so the baseline board has data
//     after a dozen PRs.
func renderConfig(src fs.FS, v Vars) (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, "repos/") {
			return err
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("example config: %w", err)
	}
	overlay, err := tree("_config", v)
	if err != nil {
		return nil, err
	}
	for p, c := range overlay {
		if p == "repos/repo.yaml" {
			p = path.Join("repos", v.Org, v.Repo+".yaml")
		}
		out[p] = c
	}
	if out[path.Join("evals", DemoRule, "cases.yaml")], err = demoCases(v); err != nil {
		return nil, err
	}
	g, ok := out["global.yaml"]
	if !ok {
		return nil, fmt.Errorf("%w has no global.yaml", errExample)
	}
	if out["global.yaml"], err = demoGlobal(g); err != nil {
		return nil, err
	}
	return out, nil
}

func demoGlobal(src string) (string, error) {
	var g map[string]any
	if err := yaml.Unmarshal([]byte(src), &g); err != nil {
		return "", fmt.Errorf("decode global.yaml: %w", err)
	}
	g["auto_approve"] = true
	providers, ok := g["providers"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: global.yaml has no providers", errExample)
	}
	providers["categorizer"] = map[string]any{
		"a": map[string]any{"provider": "claude", "model": "claude-sonnet-5-5"},
		"b": map[string]any{"provider": "openai", "model": "gpt-5.5"},
	}
	providers["verifier"] = []any{
		map[string]any{"provider": "openai", "model": "gpt-5.5"},
		map[string]any{"provider": "claude", "model": "claude-opus-5-5"},
	}
	// The demo rule is noise by design, and the verifier's importance
	// check exists to hold noise back; beat D needs its findings posted so
	// people can reject them. 0.3 still holds back what the verifier
	// scores as unimportant; a real install keeps 0.5.
	verifier, ok := g["verifier"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: global.yaml has no verifier", errExample)
	}
	verifier["importance_min"] = DemoImportanceMin
	baseline, ok := g["baseline"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: global.yaml has no baseline", errExample)
	}
	baseline["sample_rate"] = 1
	b, err := yaml.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("encode global.yaml: %w", err)
	}
	return "# Rendered by examples/demo/kit from examples/exemplar-config for the demo.\n" + string(b), nil
}

// DemoImportanceMin is the demo config's verifier.importance_min.
const DemoImportanceMin = 0.3

// demoCaseCount is the example config's eval_thresholds.min_cases, so the
// demo rule's cases count in eval CI.
const demoCaseCount = 10

// demoCases are the demo rule's eval cases: synthetic patches against the
// sandbox repository's base, each adding a function with a comment after
// Total in internal/payments/payments.go. The comment is on line 25.
func demoCases(v Vars) (string, error) {
	type expect struct {
		Path  string `yaml:"path"`
		Lines []int  `yaml:"lines"`
	}
	type evalCase struct {
		Name      string   `yaml:"name"`
		Ref       string   `yaml:"ref"`
		Synthetic bool     `yaml:"synthetic"`
		Patch     string   `yaml:"patch"`
		Paths     []string `yaml:"paths,omitempty"`
		Expect    []expect `yaml:"expect"`
	}
	const file = "internal/payments/payments.go"
	comments := []string{
		"TODO: implement partial refunds.", "FIXME: this ignores currency.", "TODO(dana): batch these calls.",
		"TODO handle the retry case.", "FIXME: remove after the migration.", "TODO: log the refund id.",
		"Refund returns a payment to the card.", "Void cancels an authorisation.",
		"Capture settles an authorised payment.", "Fee is the processor's charge in cents.",
	}
	cases := make([]evalCase, 0, demoCaseCount)
	for i, c := range comments[:demoCaseCount] {
		patch := "--- a/" + file + "\n+++ b/" + file + "\n" +
			"@@ -21,3 +21,7 @@ func (s *Store) Total(ctx context.Context, invoiceID string) (int64, error) {\n" +
			" \t}\n \treturn cents, nil\n }\n+\n" +
			fmt.Sprintf("+// Op%d does one payment operation. %s\n+func (s *Store) Op%d(ctx context.Context, id string) error { return nil }\n+\n", i, c, i)
		ec := evalCase{Name: fmt.Sprintf("op%d", i), Ref: v.Org + "/" + v.Repo + "@" + v.BaseSHA, Synthetic: true, Patch: patch}
		if strings.Contains(c, "TODO") || strings.Contains(c, "FIXME") {
			ec.Expect = []expect{{Path: file, Lines: []int{25, 25}}}
		} else {
			ec.Paths, ec.Expect = []string{file}, []expect{}
		}
		cases = append(cases, ec)
	}
	b, err := yaml.Marshal(map[string]any{"rule": DemoRule, "cases": cases})
	if err != nil {
		return "", fmt.Errorf("encode demo eval cases: %w", err)
	}
	return string(b), nil
}
