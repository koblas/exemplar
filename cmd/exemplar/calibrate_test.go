package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalibrateCmd(t *testing.T) {
	t.Parallel()
	dir := copyConfig(t)
	base := git(t, dir, "rev-parse", "HEAD")
	// Nothing verifier-related changed: not run.
	q := filepath.Join(dir, "rules/reliability/propagate-context.yaml")
	pc, _ := os.ReadFile(q)
	_ = os.WriteFile(q, append(pc, []byte("model_tier: high\n")...), 0o644)
	git(t, dir, "commit", "-q", "-am", "tweak")
	out, _, err := runCLI(t, "calibrate", "--config", dir, "--only-if-changed-since", base, "--scripted")
	if err != nil || !strings.Contains(out, "not run") {
		t.Fatalf("unrelated change: %q, %v", out, err)
	}
	// Tightening the verifier bar is a verifier input: it runs, and the
	// scripted model (which confirms everything) refutes no known-wrong label.
	g := filepath.Join(dir, "global.yaml")
	data, _ := os.ReadFile(g)
	_ = os.WriteFile(g, []byte(strings.Replace(string(data), "known_wrong_refuted_min: 0.90", "known_wrong_refuted_min: 0.95", 1)), 0o644)
	git(t, dir, "commit", "-q", "-am", "tighten")
	md := filepath.Join(t.TempDir(), "s.md")
	out, _, err = runCLI(t, "calibrate", "--config", dir, "--only-if-changed-since", base, "--scripted", "--markdown", md)
	if err == nil || !strings.Contains(out, "| Known wrong | 2 | 0 | 0% | ≥ 95% |") {
		t.Fatalf("calibration: %q, %v", out, err)
	}
	if written, _ := os.ReadFile(md); !strings.Contains(string(written), "### Verifier calibration") ||
		!strings.Contains(string(written), "No providers.categorizer") {
		t.Errorf("markdown = %q", written)
	}
	if _, _, err := runCLI(t, "calibrate", "--config", dir, "--scripted", "--stage", "categorizer"); err == nil ||
		!strings.Contains(err.Error(), "no providers.categorizer") {
		t.Errorf("--stage categorizer without providers: %v", err)
	}

	// Adding the categorizer pair is a categorizer input but not a verifier
	// one. The scripted model answers medium, so no safe PR is approved.
	base = git(t, dir, "rev-parse", "HEAD")
	data, _ = os.ReadFile(g)
	_ = os.WriteFile(g, []byte(strings.Replace(string(data), "verifier:\n  importance_min",
		"  categorizer:\n    a: { provider: claude, model: claude-opus-5-5 }\n    b: { provider: bedrock, model: anthropic.claude-opus-5-5 }\nverifier:\n  importance_min", 1)), 0o644)
	git(t, dir, "commit", "-q", "-am", "categorizer")
	out, _, err = runCLI(t, "calibrate", "--config", dir, "--only-if-changed-since", base, "--scripted")
	if err == nil || !strings.Contains(err.Error(), "categorizer calibration failed") ||
		!strings.Contains(out, "Nothing the verifier depends on changed") || !strings.Contains(out, "| Known safe | 2 | 0 | 0% | ≥ 50% |") {
		t.Fatalf("categorizer calibration: %q, %v", out, err)
	}
	if _, _, err := runCLI(t, "calibrate", "--config", dir, "--scripted", "--stage", "bogus"); err == nil {
		t.Error("--stage bogus accepted")
	}
}
