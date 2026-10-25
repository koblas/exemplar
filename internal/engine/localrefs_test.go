package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/synth"
)

// localToolbox checks out a head with files and returns a toolbox that
// answers find_references from it.
func localToolbox(t *testing.T, files map[string]string) *toolbox {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	repo, err := synth.Build(t.Context(), src, map[string]string{"README.md": "x\n"}, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := Checkout(t.Context(), filepath.Join(t.TempDir(), "ws"), CheckoutOptions{CloneURL: src, BaseSHA: repo.BaseSHA, HeadSHA: repo.HeadSHA})
	if err != nil {
		t.Fatal(err)
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	return &toolbox{ws: ws, scanner: sc, localRefs: true}
}

func TestLocalReferences(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	tb := localToolbox(t, map[string]string{
		"pkg/store/store.go":  "package store\n\n// Save writes it.\nfunc Save(x int) error {\n\treturn nil\n}\n",
		"pkg/store/batch.go":  "package store\n\nfunc batch() {\n\t_ = Save(1)\n}\n",
		"cmd/app/main.go":     "package main\n\nfunc main() {\n\t_ = store.Save(2)\n\tSaveAll()\n}\n",
		"internal/x/other.go": "package x\n\nfunc f() { store.Save(3) }\n",
		"docs/notes.md":       "Saved and SaveAll are not Save.\n",
	})
	out, err := tb.run(t.Context(), "find_references", []byte(`{"name":"store.Save","defined_in":"pkg/store/store.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	// The doc comment and the prose in notes.md name Save too.
	if !strings.HasPrefix(out, "6 references to Save (this repository only; other repositories were not searched)") {
		t.Fatalf("header: %q", out)
	}
	// Word boundaries: SaveAll and Saved are not references.
	if strings.Contains(out, "SaveAll()") && !strings.Contains(out, "cmd/app/main.go:4") {
		t.Errorf("matched a longer word: %s", out)
	}
	var order []string
	for line := range strings.Lines(out) {
		if p, _, ok := strings.Cut(line, " ("); ok && strings.Contains(p, ".") && strings.Contains(p, ":") && !strings.Contains(p, "\t") {
			order = append(order, strings.TrimSpace(line))
		}
	}
	if len(order) != 6 {
		t.Fatalf("sites = %v", order)
	}
	if _, err := exec.LookPath("ctags"); err == nil {
		// The definition first, then its own file, then the same package,
		// then the rest by path.
		want := []string{"pkg/store/store.go:4 (definition)", "pkg/store/store.go:3 (use)", "pkg/store/batch.go:4 (use)"}
		if strings.Join(order[:3], "|") != strings.Join(want, "|") {
			t.Errorf("order = %v", order)
		}
	}
	if !strings.Contains(out, "3\t// Save writes it.") {
		t.Errorf("no context lines: %s", out)
	}

	// A name that looks like a flag is matched literally.
	if out, err := tb.run(t.Context(), "find_references", []byte(`{"name":"--files"}`)); err != nil || !strings.HasPrefix(out, "0 references") {
		t.Errorf("flag-like name = %q, %v", out, err)
	}
}

func TestLocalReferencesCapAndExclusions(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	files := map[string]string{"gen/secret.go": "package gen\n\nvar _ = Widget\n"}
	for i := range 60 {
		files[fmt.Sprintf("use/u%02d.go", i)] = "package use\n\nvar _ = Widget\n"
	}
	tb := localToolbox(t, files)
	tb.exclusions = exclude.Load("", "gen/\n")
	out, err := tb.localReferences(t.Context(), "Widget", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "50 references to Widget") || !strings.Contains(out, "(more exist; narrow with defined_in)") {
		t.Errorf("cap: %q", out[:min(len(out), 200)])
	}
	if strings.Contains(out, "gen/secret.go") {
		t.Error("an excluded file was returned")
	}
}

func TestLocalReferencesWithoutCtags(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	tb := localToolbox(t, map[string]string{"a.go": "package a\n\nfunc Run() {}\n", "b.go": "package a\n\nvar _ = Run\n"})
	// Only rg on PATH: every site is a use.
	bin := t.TempDir()
	if err := os.Symlink(rg, filepath.Join(bin, "rg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out, err := tb.localReferences(t.Context(), "Run", "")
	if err != nil || !strings.HasPrefix(out, "2 references") || strings.Contains(out, "(definition)") {
		t.Errorf("without ctags = %q, %v", out, err)
	}
}

// gitRepo commits each of steps in turn (path -> content) and returns
// the commits, oldest first.
func gitRepo(t *testing.T, steps []map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	shas := make([]string, 0, len(steps))
	for i, step := range steps {
		for p, c := range step {
			full := filepath.Join(dir, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", "-A")
		git("commit", "-q", "-m", fmt.Sprintf("step %d", i))
		shas = append(shas, git("rev-parse", "HEAD"))
	}
	return dir, shas
}

func TestWorkspaceLog(t *testing.T) {
	t.Parallel()
	src, shas := gitRepo(t, []map[string]string{
		{"a.go": "1"}, {"a.go": "2"}, {"b.go": "1"}, {"a.go": "3"}, {"c.go": "head"},
	})
	base, head := shas[3], shas[4]
	ws, err := Checkout(t.Context(), filepath.Join(t.TempDir(), "ws"), CheckoutOptions{CloneURL: src, BaseSHA: base, HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	// A local remote refuses fetching by SHA, so the checkout already has
	// the full history and Deepen may fail; the log reads what is there.
	_ = ws.Deepen(t.Context(), LocalHistoryDepth)
	commits, err := ws.Log(t.Context(), base, "a.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0].Subject != "step 3" || commits[2].Subject != "step 0" {
		t.Errorf("log = %+v", commits)
	}
	for _, bad := range []struct{ rev, path string }{{"HEAD", "a.go"}, {base, "../x"}, {base, "-a"}, {base, "/etc/passwd"}} {
		if _, err := ws.Log(t.Context(), bad.rev, bad.path, 5); err == nil {
			t.Errorf("Log(%q, %q) accepted", bad.rev, bad.path)
		}
	}
	// The local context deepens once and answers path_history commits.
	lc := &localContext{ws: ws}
	got, err := lc.Commits(context.WithoutCancel(t.Context()), "acme/app", base, "a.go", 2)
	if err != nil || len(got) != 2 {
		t.Errorf("local commits = %+v, %v", got, err)
	}
}
