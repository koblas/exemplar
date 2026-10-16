package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/evaluate"
)

// copyConfig copies the example exemplar-config into a fresh git repo.
func copyConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := "../../examples/exemplar-config"
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestChangedRules(t *testing.T) {
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "x")
	if err != nil {
		t.Fatal(err)
	}
	got := changedRules(snap, []string{
		"rules/reliability/propagate-context.yaml", // a rule file
		"evals/wrap-errors-with-cause/cases.yaml",  // a rule's cases
		"evals/_adversarial/diff-false-approval.yaml",
		"rules/security/gone.yaml", // deleted
		"global.yaml",
	})
	slices.Sort(got)
	if strings.Join(got, ",") != "gone,propagate-context,wrap-errors-with-cause" {
		t.Errorf("changedRules = %v", got)
	}
}

func TestEvalChangedSince(t *testing.T) {
	dir := copyConfig(t)
	base := git(t, dir, "rev-parse", "HEAD")
	// Nothing changed: nothing to evaluate.
	out, _, err := runCLI(t, "eval", "--config", dir, "--changed-since", base)
	if err != nil || !strings.Contains(out, "no rules to evaluate") {
		t.Fatalf("no changes: %q, %v", out, err)
	}
	// A changed rule without eval cases fails the gate.
	p := filepath.Join(dir, "rules/reliability/propagate-context.yaml")
	data, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(data, []byte("model_tier: high\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-q", "-am", "tune rule")
	md := filepath.Join(t.TempDir(), "summary.md")
	out, _, err = runCLI(t, "eval", "--config", dir, "--changed-since", base, "--scripted", "--markdown", md)
	if err == nil || !strings.Contains(out, "`propagate-context`") || !strings.Contains(out, "no evals/propagate-context/cases.yaml") {
		t.Fatalf("rule without cases: %q, %v", out, err)
	}
	if written, _ := os.ReadFile(md); !strings.Contains(string(written), "### Exemplar eval") {
		t.Errorf("markdown summary = %q", written)
	}
}

func TestEvalRemote(t *testing.T) {
	dir := copyConfig(t)
	base := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "remote", "add", "origin", "git@github.com:acme/exemplar-config.git")
	p := filepath.Join(dir, "rules/reliability/propagate-context.yaml")
	data, _ := os.ReadFile(p)
	_ = os.WriteFile(p, append(data, []byte("model_tier: high\n")...), 0o644)
	git(t, dir, "commit", "-q", "-am", "tune rule")
	head := git(t, dir, "rev-parse", "HEAD")

	pass := false
	var got evaluate.Request
	polls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer svc.token.sig" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id": "e1", "status": "queued"}`))
			return
		}
		polls++
		if polls == 1 {
			_, _ = w.Write([]byte(`{"id": "e1", "status": "running"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(evaluate.Status{ID: "e1", Status: evaluate.StatusDone, Result: &evaluate.Result{Eval: &evaluate.EvalResult{
			Reports: json.RawMessage(`[{"rule": "propagate-context", "pass": false}]`), Markdown: "### Exemplar eval\n\n| `propagate-context` |\n", Pass: pass,
			Rules: []string{"propagate-context"},
		}}})
	}))
	defer api.Close()
	t.Setenv("EXEMPLAR_TOKEN", "svc.token.sig")
	t.Setenv("EXEMPLAR_API_URL", api.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	remotePoll = time.Millisecond
	defer func() { remotePoll = 10 * time.Second }()

	jsonOut, md := filepath.Join(t.TempDir(), "r.json"), filepath.Join(t.TempDir(), "s.md")
	out, _, err := runCLI(t, "eval", "--config", dir, "--changed-since", base, "--remote", "--rules", "wrap-errors-with-cause", "--json", jsonOut, "--markdown", md)
	if err == nil || !strings.Contains(err.Error(), "below eval_thresholds") || !strings.Contains(out, "`propagate-context`") {
		t.Fatalf("remote eval = %q, %v", out, err)
	}
	if got.Mode != evaluate.ModeEval || got.Repo != "acme/exemplar-config" || got.BaseSHA != base || got.HeadSHA != head || got.Diff != "" ||
		strings.Join(got.Rules, ",") != "wrap-errors-with-cause" {
		t.Errorf("request = %+v", got)
	}
	if b, _ := os.ReadFile(jsonOut); !strings.Contains(string(b), `"rule": "propagate-context"`) {
		t.Errorf("json = %s", b)
	}
	if b, _ := os.ReadFile(md); !strings.HasPrefix(string(b), "### Exemplar eval") {
		t.Errorf("markdown = %s", b)
	}
	pass, polls = true, 0
	if _, _, err := runCLI(t, "eval", "--config", dir, "--changed-since", base, "--remote"); err != nil {
		t.Errorf("passing remote eval = %v", err)
	}
	if _, _, err := runCLI(t, "eval", "--config", dir, "--remote"); err == nil || !strings.Contains(err.Error(), "--changed-since") {
		t.Errorf("no base = %v", err)
	}
}
