// Package compiled runs deterministic rules compiled to ast-grep
// or Semgrep: the compiled-rule lane in workers and compile attempts in
// the learning plane. Engines run as subprocesses on the worker
// checkout, restricted to scoped files, with a timeout and no network.
package compiled

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Match is one engine hit, with 1-based lines.
type Match struct {
	Path      string
	StartLine int
	EndLine   int
	Message   string
}

// ErrEngineUnavailable means the engine binary is not installed. The rule
// run is recorded as failed, which keeps coverage below 100%.
var ErrEngineUnavailable = errors.New("compiled: engine binary not installed")

var (
	// ErrUnknownEngine is a rule engine other than ast-grep or semgrep.
	ErrUnknownEngine = errors.New("compiled: unknown engine")
	errTimedOut      = errors.New("timed out")
	errSemgrep       = errors.New("compiled: semgrep")
)

// Runner runs compiled rules.
type Runner struct {
	// ASTGrep and Semgrep are the engine binaries (default "ast-grep" and
	// "semgrep" on PATH).
	ASTGrep string
	Semgrep string
	Timeout time.Duration
	// Sandbox, when set, prefixes every engine command, e.g. a wrapper
	// that drops network access and sets CPU and memory limits (see
	// SandboxCommand). Compiled rules in workers relies on the pod's limits and
	// NetworkPolicy; the learning plane runs model-written rules and sets it.
	Sandbox []string
}

// SandboxCommand returns the Linux sandbox prefix: a new network
// namespace with no interfaces (unshare) and address-space and CPU-time
// limits (prlimit). ok is false when either tool is missing or the OS is
// not Linux.
func SandboxCommand(memBytes int64, cpuSeconds int) ([]string, bool) {
	if runtime.GOOS != "linux" {
		return nil, false
	}
	for _, t := range []string{"unshare", "prlimit"} {
		if _, err := exec.LookPath(t); err != nil {
			return nil, false
		}
	}
	return []string{
		"unshare", "--net", "--map-root-user", "--",
		"prlimit", fmt.Sprintf("--as=%d", memBytes), fmt.Sprintf("--cpu=%d", cpuSeconds), "--",
	}, true
}

// Run executes one rule body with the named engine over files in dir.
func (r Runner) Run(ctx context.Context, dir, engine, ruleBody string, files []string) ([]Match, error) {
	switch engine {
	case "ast-grep":
		out, err := r.runEngine(ctx, dir, r.astGrep(), ruleBody, func(rule string) []string {
			return append([]string{"scan", "--rule", rule, "--json=stream", "--"}, files...)
		})
		if err != nil {
			return nil, err
		}
		return ParseASTGrep(out, files)
	case "semgrep":
		out, err := r.runEngine(ctx, dir, r.semgrep(), ruleBody, func(rule string) []string {
			return append([]string{
				"scan", "--config", rule, "--json", "--metrics=off", "--disable-version-check",
				"--no-git-ignore", "--quiet", "--",
			}, files...)
		})
		if err != nil {
			return nil, err
		}
		return ParseSemgrep(out, files)
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownEngine, engine)
}

// Validate checks a rule body with the engine itself
// (ast-grep scan over no files, semgrep --validate).
func (r Runner) Validate(ctx context.Context, engine, ruleBody string) error {
	empty, err := os.MkdirTemp("", "exemplar-validate-*")
	if err != nil {
		return fmt.Errorf("compiled: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(empty) }()
	switch engine {
	case "ast-grep":
		_, err = r.runEngine(ctx, empty, r.astGrep(), ruleBody, func(rule string) []string {
			return []string{"scan", "--rule", rule, "--json=stream", "."}
		})
	case "semgrep":
		_, err = r.runEngine(ctx, empty, r.semgrep(), ruleBody, func(rule string) []string {
			return []string{"scan", "--validate", "--config", rule, "--metrics=off", "--disable-version-check", "--quiet"}
		})
	default:
		err = fmt.Errorf("%w %q", ErrUnknownEngine, engine)
	}
	return err
}

func (r Runner) astGrep() string { return cmpOr(r.ASTGrep, "ast-grep") }
func (r Runner) semgrep() string { return cmpOr(r.Semgrep, "semgrep") }

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// runEngine writes the rule to a temp file and runs bin with args(rule)
// in dir. Exit status 1 means "findings reported" for both engines.
func (r Runner) runEngine(ctx context.Context, dir, bin, ruleBody string, args func(rule string) []string) ([]byte, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, ErrEngineUnavailable
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ruleFile, err := os.CreateTemp("", "exemplar-rule-*.yml")
	if err != nil {
		return nil, fmt.Errorf("compiled: create rule file: %w", err)
	}
	defer func() { _ = os.Remove(ruleFile.Name()) }()
	if _, err := ruleFile.WriteString(ruleBody); err != nil {
		_ = ruleFile.Close()
		return nil, fmt.Errorf("compiled: write rule file: %w", err)
	}
	if err := ruleFile.Close(); err != nil {
		return nil, fmt.Errorf("compiled: close rule file: %w", err)
	}

	argv := append(append(slices.Clone(r.Sandbox), bin), args(ruleFile.Name())...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(),
		"SEMGREP_SEND_METRICS=off", "SEMGREP_ENABLE_VERSION_CHECK=0",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("compiled: %s %w after %s", filepath.Base(bin), errTimedOut, timeout)
			}
			return nil, fmt.Errorf("compiled: %s: %w: %s", filepath.Base(bin), err, strings.TrimSpace(stderr.String()))
		}
	}
	return out, nil
}

// ParseSemgrep parses `semgrep scan --json` output. Matches in files other
// than the scoped ones are discarded; reported rule errors fail the run.
func ParseSemgrep(out []byte, scoped []string) ([]Match, error) {
	allowed := map[string]bool{}
	for _, f := range scoped {
		allowed[filepath.ToSlash(filepath.Clean(f))] = true
	}
	var res struct {
		Results []struct {
			Path  string `json:"path"`
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
			End struct {
				Line int `json:"line"`
			} `json:"end"`
			Extra struct {
				Message string `json:"message"`
			} `json:"extra"`
		} `json:"results"`
		Errors []struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("compiled: parse semgrep output: %w", err)
	}
	for _, e := range res.Errors {
		if e.Level == "error" {
			return nil, fmt.Errorf("%w: %s", errSemgrep, e.Message)
		}
	}
	var matches []Match
	for _, m := range res.Results {
		p := filepath.ToSlash(filepath.Clean(m.Path))
		if !allowed[p] {
			continue
		}
		// semgrep lines are 1-based.
		matches = append(matches, Match{Path: p, StartLine: m.Start.Line, EndLine: m.End.Line, Message: m.Extra.Message})
	}
	return matches, nil
}

// ParseASTGrep parses `ast-grep scan --json=stream` output. Matches in files
// other than the scoped ones are discarded.
func ParseASTGrep(out []byte, scoped []string) ([]Match, error) {
	allowed := map[string]bool{}
	for _, f := range scoped {
		allowed[filepath.ToSlash(filepath.Clean(f))] = true
	}
	var matches []Match
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m struct {
			File  string `json:"file"`
			Range struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
				End struct {
					Line int `json:"line"`
				} `json:"end"`
			} `json:"range"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("compiled: parse ast-grep output: %w", err)
		}
		p := filepath.ToSlash(filepath.Clean(m.File))
		if !allowed[p] {
			continue
		}
		// ast-grep lines are 0-based.
		matches = append(matches, Match{Path: p, StartLine: m.Range.Start.Line + 1, EndLine: m.Range.End.Line + 1, Message: m.Message})
	}
	if err := sc.Err(); err != nil {
		return matches, fmt.Errorf("compiled: read ast-grep output: %w", err)
	}
	return matches, nil
}
