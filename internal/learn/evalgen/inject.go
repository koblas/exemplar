package evalgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/redact"
)

// Limits on injected violations.
const (
	maxDiffLines   = 15  // changed lines (added + removed) in an injected patch
	maxShownLines  = 400 // file lines shown to the model
	contextLines   = 3
	maxReplacement = 12
)

// Edit is the model's proposed change: replace lines [StartLine, EndLine]
// of the file with Replacement.
type Edit struct {
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Replacement string `json:"replacement"`
	Explanation string `json:"explanation"`
}

// Rule is what the injector tells the model about the constraint.
type Rule struct {
	Title       string
	Description string
}

// Model proposes the minimal edit that violates the rule. Implementations
// call a model through the gateway; content arrives already redacted.
type Model interface {
	ProposeViolation(ctx context.Context, rule Rule, path, numbered string) (Edit, error)
}

// Files reads a file at a commit (from a mirror or a checkout).
type Files interface {
	FileAt(ctx context.Context, repo, sha, path string) ([]byte, error)
}

// Parser reports whether source parses. Keyed by file extension.
type Parser func(path string, src []byte) error

// DefaultParsers covers languages with a parser available in Go. Files in
// other languages are not injected into: an unvalidated synthetic case is
// worse than none. tree-sitter grammars widen this later.
var DefaultParsers = map[string]Parser{
	".go": func(p string, src []byte) error {
		_, err := parser.ParseFile(token.NewFileSet(), p, src, parser.AllErrors)
		return err
	},
}

// Injector creates injected-violation cases.
type Injector struct {
	Model   Model
	Files   Files
	Scanner *redact.Scanner
	Parsers map[string]Parser
}

// ErrRejected wraps every reason an injection is discarded.
var ErrRejected = errors.New("evalgen: injection rejected")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

// Inject produces an injected case from a negative case's target, or an
// ErrRejected error saying why not.
func (in *Injector) Inject(ctx context.Context, rule Rule, base Case, t InjectTarget) (*Case, error) {
	parsers := in.Parsers
	if parsers == nil {
		parsers = DefaultParsers
	}
	parse, ok := parsers[strings.ToLower(path.Ext(t.Path))]
	if !ok {
		return nil, reject("no parser for %s files", path.Ext(t.Path))
	}
	src, err := in.Files.FileAt(ctx, t.Repo, t.SHA, t.Path)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(src, 0) >= 0 {
		return nil, reject("%s is binary", t.Path)
	}
	// The patch must apply to the real file, so a file that needs
	// redaction cannot be shown to the model and patched faithfully.
	if m := in.Scanner.Scan(string(src)); len(m) > 0 {
		return nil, reject("%s contains a secret", t.Path)
	}
	lines := strings.Split(string(src), "\n")
	// n counts real lines: a file ending in a newline splits into a
	// trailing empty element that is not a line.
	n := len(lines)
	trailingNewline := n > 0 && lines[n-1] == ""
	if trailingNewline {
		n--
	}
	shown := min(n, maxShownLines)
	var numbered strings.Builder
	for i := range shown {
		fmt.Fprintf(&numbered, "%d\t%s\n", i+1, lines[i])
	}

	edit, err := in.Model.ProposeViolation(ctx, rule, t.Path, numbered.String())
	if err != nil {
		return nil, err
	}
	if edit.StartLine < 1 || edit.EndLine < edit.StartLine || edit.EndLine > shown {
		return nil, reject("edit range %d-%d outside the %d lines shown", edit.StartLine, edit.EndLine, shown)
	}
	repl := strings.Split(strings.TrimRight(edit.Replacement, "\n"), "\n")
	if edit.Replacement == "" {
		repl = nil
	}
	if len(repl) > maxReplacement {
		return nil, reject("replacement has %d lines, more than %d", len(repl), maxReplacement)
	}
	if !trailingNewline && edit.EndLine+contextLines >= n {
		return nil, reject("edit too close to the end of a file without a final newline")
	}
	removed := lines[edit.StartLine-1 : edit.EndLine]
	if equalLines(removed, repl) {
		return nil, reject("edit changes nothing")
	}
	if changed := countChanged(removed, repl); changed > maxDiffLines {
		return nil, reject("diff changes %d lines, more than %d", changed, maxDiffLines)
	}

	newLines := append(append(append([]string{}, lines[:edit.StartLine-1]...), repl...), lines[edit.EndLine:]...)
	newSrc := []byte(strings.Join(newLines, "\n"))
	if err := parse(t.Path, newSrc); err != nil {
		return nil, reject("edited file does not parse: %v", err)
	}
	patch := unifiedDiff(t.Path, lines[:n], edit.StartLine, edit.EndLine, repl)
	if err := checkApplies(ctx, t.Path, src, patch); err != nil {
		return nil, reject("patch does not apply: %v", err)
	}

	end := edit.StartLine + max(len(repl), 1) - 1
	return &Case{
		ID:     "i-" + strings.TrimPrefix(base.ID, "n-"),
		Kind:   KindInjected,
		Name:   fmt.Sprintf("injected violation in %s (from %s#%d)", t.Path, t.Repo, t.PR),
		Ref:    base.Ref,
		Patch:  patch,
		Expect: []Expect{{Path: t.Path, Lines: [2]int{edit.StartLine, end}}},
	}, nil
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// countChanged counts removed plus added lines after trimming the common
// prefix and suffix of the two blocks.
func countChanged(old, repl []string) int {
	p := 0
	for p < len(old) && p < len(repl) && old[p] == repl[p] {
		p++
	}
	s := 0
	for s < len(old)-p && s < len(repl)-p && old[len(old)-1-s] == repl[len(repl)-1-s] {
		s++
	}
	return (len(old) - p - s) + (len(repl) - p - s)
}

// unifiedDiff renders a single-hunk git-style diff replacing old lines
// [start, end] with repl.
func unifiedDiff(file string, lines []string, start, end int, repl []string) string {
	ctxStart := max(1, start-contextLines)
	ctxEnd := min(len(lines), end+contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", file, file, file, file)
	oldCount := ctxEnd - ctxStart + 1
	newCount := oldCount - (end - start + 1) + len(repl)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", ctxStart, oldCount, ctxStart, newCount)
	for i := ctxStart; i < start; i++ {
		b.WriteString(" " + lines[i-1] + "\n")
	}
	for i := start; i <= end; i++ {
		b.WriteString("-" + lines[i-1] + "\n")
	}
	for _, l := range repl {
		b.WriteString("+" + l + "\n")
	}
	for i := end + 1; i <= ctxEnd; i++ {
		b.WriteString(" " + lines[i-1] + "\n")
	}
	return b.String()
}

// checkApplies runs `git apply --check` against the original file.
func checkApplies(ctx context.Context, file string, src []byte, patch string) error {
	dir, err := os.MkdirTemp("", "exemplar-evalgen-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	full := filepath.Join(dir, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, src, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "apply", "--check", "-")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	cmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
