package compiled

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Golden output shape from `ast-grep scan --json=stream`.
const astgrepOut = `{"text":"db.Query(\"SELECT \" + id, x)","range":{"byteOffset":{"start":10,"end":40},"start":{"line":11,"column":1},"end":{"line":12,"column":4}},` +
	`"file":"internal/store/users.go","lines":"...","language":"Go","ruleId":"no-raw-sql-concat","severity":"error","message":"Build SQL with placeholders"}
{"text":"x","range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},"file":"vendor/other.go","message":"m"}
`

func TestParseASTGrep(t *testing.T) {
	got, err := ParseASTGrep([]byte(astgrepOut), []string{"internal/store/users.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1 (out-of-scope file dropped): %+v", len(got), got)
	}
	m := got[0]
	if m.Path != "internal/store/users.go" || m.StartLine != 12 || m.EndLine != 13 || m.Message == "" {
		t.Fatalf("match = %+v, want 1-based lines 12-13", m)
	}
	if _, err := ParseASTGrep([]byte("not json\n"), nil); err == nil {
		t.Fatal("garbage output accepted")
	}
}

func TestRunnerUnavailable(t *testing.T) {
	_, err := Runner{ASTGrep: "definitely-not-ast-grep"}.Run(t.Context(), t.TempDir(), "ast-grep", "id: x", []string{"a.go"})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("Run = %v, want ErrEngineUnavailable", err)
	}
	if _, err := (Runner{Semgrep: "definitely-not-semgrep"}).Run(t.Context(), t.TempDir(), "semgrep", "", nil); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("semgrep = %v, want ErrEngineUnavailable", err)
	}
}

// fakeEngine writes an executable script that records its arguments and
// prints out, exiting with code.
func fakeEngine(t *testing.T, name, out string, code int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin, argsFile := filepath.Join(dir, name), filepath.Join(dir, "args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncat <<'EOF_OUT'\n%s\nEOF_OUT\nexit %d\n", argsFile, out, code)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

const semgrepOut = `{"results":[{"path":"svc/a.go","start":{"line":4},"end":{"line":5},"extra":{"message":"Use placeholders"}},` +
	`{"path":"other/b.go","start":{"line":1},"end":{"line":1},"extra":{"message":"x"}}],"errors":[]}`

func TestSemgrep(t *testing.T) {
	bin, argsFile := fakeEngine(t, "semgrep", semgrepOut, 1) // 1 = findings
	got, err := Runner{Semgrep: bin}.Run(t.Context(), t.TempDir(), "semgrep", "rules: []", []string{"svc/a.go"})
	if err != nil || len(got) != 1 || got[0].StartLine != 4 || got[0].EndLine != 5 || got[0].Message != "Use placeholders" {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	args, _ := os.ReadFile(argsFile)
	// --jobs 1: each Semgrep job reserves its own heap, and the default
	// is one per CPU of the node, so limits would depend on the node.
	for _, want := range []string{"--metrics=off", "--json", "--disable-version-check", "--jobs\n1", "svc/a.go"} {
		if !strings.Contains(string(args), want+"\n") {
			t.Errorf("semgrep args lack %s:\n%s", want, args)
		}
	}
	if _, err := ParseSemgrep([]byte(`{"results":[],"errors":[{"level":"error","message":"bad rule"}]}`), nil); err == nil {
		t.Error("rule errors not reported")
	}
}

func TestValidateAndSandbox(t *testing.T) {
	ok, argsFile := fakeEngine(t, "ast-grep", "", 0)
	r := Runner{ASTGrep: ok, Sandbox: []string{"/usr/bin/env"}}
	if err := r.Validate(t.Context(), "ast-grep", "id: x"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.HasPrefix(string(args), "scan\n--config\n") || !strings.Contains(string(args), "\n--rule\n") {
		t.Errorf("validate args = %q", args)
	}
	bad, _ := fakeEngine(t, "ast-grep", "", 2)
	if err := (Runner{ASTGrep: bad}).Validate(t.Context(), "ast-grep", "id: x"); err == nil || errors.Is(err, ErrEngineUnavailable) {
		t.Errorf("invalid rule err = %v", err)
	}
	if err := (Runner{}).Validate(t.Context(), "cobol", "x"); err == nil {
		t.Error("unknown engine accepted")
	}
	// A sandbox that is missing fails the run rather than running unsandboxed.
	r.Sandbox = []string{"/nonexistent/sandbox"}
	if err := r.Validate(t.Context(), "ast-grep", "id: x"); err == nil {
		t.Error("ran without its sandbox")
	}
}

// requireEngine skips without bin, unless EXEMPLAR_REQUIRE_ENGINES=1 (CI).
func requireEngine(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("EXEMPLAR_REQUIRE_ENGINES") == "1" {
			t.Fatalf("%s is required (EXEMPLAR_REQUIRE_ENGINES=1)", bin)
		}
		t.Skipf("%s not installed", bin)
	}
}

const goSource = "package svc\n\nimport \"net/http\"\n\nfunc f(url string) {\n\thttp.Get(url)\n}\n"

func writeSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "svc", "a.go"), []byte(goSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRealASTGrep(t *testing.T) {
	requireEngine(t, "ast-grep")
	dir := writeSource(t)
	// A bare http.Get($URL) pattern parses as a Go type conversion and never
	// matches; ast-grep needs the call in context.
	rule := "id: no-bare-get\nlanguage: go\nrule:\n  pattern:\n    context: \"func _() { http.Get($URL) }\"\n    selector: call_expression\nmessage: Set a timeout\nseverity: warning\n"
	r := Runner{}
	if err := r.Validate(t.Context(), "ast-grep", rule); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := r.Run(t.Context(), dir, "ast-grep", rule, []string{"svc/a.go"})
	if err != nil || len(got) != 1 || got[0].StartLine != 6 || got[0].Path != "svc/a.go" {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	if err := r.Validate(t.Context(), "ast-grep", "id: x\nlanguage: go\nrule:\n  nonsense: [\n"); err == nil {
		t.Error("ast-grep accepted a broken rule")
	}
}

// A scanned checkout is untrusted: ast-grep must not pick up its project
// config, whose customLanguages entries load shared libraries.
func TestRealASTGrepIgnoresCheckoutConfig(t *testing.T) {
	requireEngine(t, "ast-grep")
	dir := writeSource(t)
	hostile := "customLanguages:\n  evil:\n    libraryPath: ./missing-lib.so\n    extensions: [evil]\n"
	for _, p := range []string{"sgconfig.yml", "svc/sgconfig.yml"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(hostile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rule := "id: no-bare-get\nlanguage: go\nrule:\n  pattern:\n    context: \"func _() { http.Get($URL) }\"\n    selector: call_expression\nmessage: Set a timeout\n"
	got, err := Runner{}.Run(t.Context(), dir, "ast-grep", rule, []string{"svc/a.go"})
	if err != nil || len(got) != 1 {
		t.Fatalf("Run = %+v, %v; the checkout's sgconfig.yml was loaded", got, err)
	}
}

func TestRealSemgrep(t *testing.T) {
	requireEngine(t, "semgrep")
	dir := writeSource(t)
	rule := "rules:\n  - id: no-bare-get\n    languages: [go]\n    message: Set a timeout\n    severity: WARNING\n    pattern: http.Get(...)\n"
	r := Runner{Timeout: 3 * time.Minute}
	if err := r.Validate(t.Context(), "semgrep", rule); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := r.Run(t.Context(), dir, "semgrep", rule, []string{"svc/a.go"})
	if err != nil || len(got) != 1 || got[0].StartLine != 6 || got[0].Path != "svc/a.go" {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	if err := r.Validate(t.Context(), "semgrep", "rules:\n  - id: x\n    languages: [go]\n    message: m\n    severity: WARNING\n"); err == nil {
		t.Error("semgrep accepted a rule with no pattern")
	}
}

func TestLimitCommand(t *testing.T) {
	cmd, ok := LimitCommand(1<<30, 60)
	if runtime.GOOS != "linux" {
		if ok {
			t.Fatalf("LimitCommand on %s = %v", runtime.GOOS, cmd)
		}
		return
	}
	if _, err := exec.LookPath("prlimit"); err != nil {
		t.Skip("prlimit not installed")
	}
	if !ok || strings.Join(cmd, " ") != "prlimit --data=1073741824 --cpu=60 --" {
		t.Fatalf("LimitCommand = %v, %v", cmd, ok)
	}
}

func TestSandboxArgs(t *testing.T) {
	// A data-segment limit, as LimitCommand sets: Semgrep reserves more
	// address space than it uses, so an --as limit fails it first.
	want := "unshare --net --map-root-user -- prlimit --data=4294967296 --cpu=120 --"
	if got := strings.Join(sandboxArgs(EvalMemoryLimit, EvalCPULimit), " "); got != want {
		t.Fatalf("sandboxArgs = %s, want %s", got, want)
	}
}

// Both engines must still work under mode eval's limits and in the
// compile sandbox, over several files (Linux only; CI runs it).
func TestRealEnginesUnderLimits(t *testing.T) {
	lim, ok := LimitCommand(EvalMemoryLimit, EvalCPULimit)
	if !ok {
		t.Skip("prlimit not available")
	}
	requireEngine(t, "ast-grep")
	requireEngine(t, "semgrep")
	dir := writeSource(t)
	files := make([]string, 0, 13)
	files = append(files, "svc/a.go")
	for i := range 12 {
		name := fmt.Sprintf("svc/b%d.go", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(goSource), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, name)
	}
	sandboxes := map[string][]string{"limits": lim}
	if sb, ok := SandboxCommand(EvalMemoryLimit, EvalCPULimit); ok && exec.CommandContext(t.Context(), sb[0], append(sb[1:], "true")...).Run() == nil {
		sandboxes["sandbox"] = sb
	}
	astRule := "id: no-bare-get\nlanguage: go\nrule:\n  pattern:\n    context: \"func _() { http.Get($URL) }\"\n    selector: call_expression\nmessage: m\n"
	semRule := "rules:\n  - id: no-bare-get\n    languages: [go]\n    message: m\n    severity: WARNING\n    pattern: http.Get(...)\n"
	for name, sb := range sandboxes {
		r := Runner{Sandbox: sb, Timeout: 3 * time.Minute}
		for engine, rule := range map[string]string{"ast-grep": astRule, "semgrep": semRule} {
			got, err := r.Run(t.Context(), dir, engine, rule, files)
			if err != nil || len(got) != len(files) {
				t.Errorf("%s under %s = %d matches, %v", engine, name, len(got), err)
			}
		}
	}
}
