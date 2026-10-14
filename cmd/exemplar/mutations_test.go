package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/evalrun"
)

const mutSample = `package svc

import "os"

func read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 10)
	_, err = f.Read(buf)
	return buf, err
}
`

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
}

func TestMutations(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	copyTree(t, "../../examples/exemplar-config", cfg)
	src := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", src}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	_ = os.MkdirAll(filepath.Join(src, "svc"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "svc", "read.go"), []byte(mutSample), 0o644)
	_ = os.WriteFile(filepath.Join(src, "svc", "read_test.go"), []byte("package svc\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "svc", "load.c"), []byte("int load(void) {\n    int rc = init();\n    if (rc != 0) {\n        return rc;\n    }\n    return run();\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "svc", "load_test.c"), []byte("int t(void) {\n    int rc = init();\n    if (rc != 0) {\n        return rc;\n    }\n    return 0;\n}\n"), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "x")
	sha := git("rev-parse", "HEAD")

	var out bytes.Buffer
	if err := runMutations(t.Context(), &out, cfg, src, "exemplar-sandbox/playground", "", []string{"svc/**"}, 1, 20, false); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	// The example config enables error handling (and nil-deref,
	// concurrency), not the resource-leak class.
	if !strings.Contains(out.String(), "bug.error-handling: 2 new cases (2 in all)") || strings.Contains(out.String(), "bug.resource-leak") {
		t.Errorf("out = %s", out.String())
	}
	snap, err := config.LoadSnapshot(os.DirFS(cfg), cfg)
	if err != nil {
		t.Fatalf("config with bug-class evals: %v", err)
	}
	ec := snap.Evals["bug.error-handling"]
	all, err := evalrun.Cases(os.DirFS(cfg), ec)
	if err != nil || len(all) != 2 {
		t.Fatalf("cases = %+v, %v", all, err)
	}
	byPath := map[string]evalrun.Case{}
	for _, c := range all {
		byPath[c.Expect[0].Path] = c
	}
	// The C file's case (C operators on tree-sitter); its test file has none.
	if c, ok := byPath["svc/load.c"]; !ok || c.Expect[0].Lines[0] != 2 || !strings.HasPrefix(c.Patch, "diff --git a/svc/load.c") {
		t.Errorf("C case = %+v", c)
	}
	c := byPath["svc/read.go"]
	if c.SHA != sha || !c.Synthetic || c.Expect[0].Lines[0] != 6 || !strings.HasPrefix(c.Patch, "diff --git a/svc/read.go") {
		t.Fatalf("Go case = %+v", c)
	}
	// The patches apply to the files at that commit.
	for i, c := range all {
		name := fmt.Sprintf("p%d.diff", i)
		_ = os.WriteFile(filepath.Join(src, name), []byte(c.Patch), 0o644)
		git("apply", "--check", name)
	}

	// Run again: nothing new.
	out.Reset()
	if err := runMutations(t.Context(), &out, cfg, src, "exemplar-sandbox/playground", "", []string{"svc/**"}, 1, 20, false); err != nil ||
		!strings.Contains(out.String(), "bug.error-handling: 0 new cases (2 in all)") {
		t.Errorf("again = %s, %v", out.String(), err)
	}
}
