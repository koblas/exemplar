package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddedOrActivated(t *testing.T) {
	dir := copyConfig(t)
	// The base has wrap-errors-with-cause in shadow; the PR activates it.
	p := filepath.Join(dir, "rules/maintainability/wrap-errors-with-cause.yaml")
	data, _ := os.ReadFile(p)
	shadow := strings.Replace(string(data), "status: active", "status: shadow", 1)
	if err := os.WriteFile(p, []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-q", "-am", "shadow")
	base := git(t, dir, "rev-parse", "HEAD")
	_ = os.WriteFile(p, data, 0o644) // back to active: an activation
	// A tweak to an active rule is not a trigger.
	q := filepath.Join(dir, "rules/reliability/propagate-context.yaml")
	pc, _ := os.ReadFile(q)
	_ = os.WriteFile(q, append(pc, []byte("model_tier: high\n")...), 0o644)
	// A new shadow rule is.
	newRule := strings.Replace(strings.Replace(string(pc), "id: propagate-context", "id: new-rule-c1", 1), "status: active", "status: shadow", 1)
	_ = os.WriteFile(filepath.Join(dir, "rules/reliability/new-rule-c1.yaml"), []byte(newRule), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "change")

	sel, err := selectRules(t.Context(), dir, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := addedOrActivated(sel, sel.changed)
	if strings.Join(got, ",") != "new-rule-c1,wrap-errors-with-cause" {
		t.Errorf("addedOrActivated = %v (changed %v)", got, sel.changed)
	}
}

func TestPrecheckNoOps(t *testing.T) {
	dir := copyConfig(t)
	base := git(t, dir, "rev-parse", "HEAD")
	q := filepath.Join(dir, "rules/reliability/propagate-context.yaml")
	pc, _ := os.ReadFile(q)
	_ = os.WriteFile(q, append(pc, []byte("model_tier: high\n")...), 0o644)
	git(t, dir, "commit", "-q", "-am", "tweak")
	out, _, err := runCLI(t, "volume", "--config", dir, "--changed-since", base)
	if err != nil || !strings.Contains(out, "No rule is added or activated") {
		t.Errorf("volume: %q, %v", out, err)
	}
	out, _, err = runCLI(t, "linter-overlap", "--config", dir, "--changed-since", base)
	if err != nil || !strings.Contains(out, "No changed rule has eval cases") {
		t.Errorf("linter-overlap: %q, %v", out, err)
	}
}
