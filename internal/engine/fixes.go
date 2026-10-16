package engine

import (
	"bytes"
	"context"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/syntax"
)

// Fix validation (spec §6.11, FR-F1–F2). A suggested fix is replacement
// text for exactly the anchored lines, posted as a GitHub suggestion block
// the author can commit in one click. It is checked in code before it is
// posted: a fix that fails is dropped silently and the finding posts
// without it.

// maxFixLines bounds a suggestion's replacement lines.
const maxFixLines = 30

// Reasons a fix is rejected, for logs and tests.
const (
	FixNotAllowed  = "rule does not allow fixes"
	FixLeftSide    = "anchored on removed lines"
	FixTooLong     = "replacement over 30 lines"
	FixFence       = "contains a code fence"
	FixRedacted    = "contains a redacted secret"
	FixSecret      = "contains a possible secret"
	FixUnchanged   = "identical to the original"
	FixOutside     = "repeats a line outside the anchored range"
	FixNoFile      = "file not readable at head"
	FixBadRange    = "anchor outside the file"
	FixParse       = "introduces a parse error"
	FixFormat      = "not formatted (gofmt, clang-format or prettier)"
	FixNoParser    = "no parser for the file's language"
	FixParserError = "parser failed"
)

// fixAllowed reports whether a finding may carry a suggestion: rules with
// fix_policy suggest, and bug classes.
func fixAllowed(f *Finding, policy map[string]string) bool {
	return f.Lane == LaneBug || policy[f.RuleID] == schema.FixSuggest
}

// checkFix validates f.Fix against the head file and sets f.FixValid. It
// returns why a fix was rejected ("" when valid).
func (e *Engine) checkFix(ctx context.Context, p *Prepared, f *Finding, policy map[string]string) string {
	reason := e.fixProblem(ctx, p, f, policy)
	valid := reason == ""
	f.FixValid = &valid
	return reason
}

// SuggestionText is f's fix as posted in a suggestion block: the full
// replacement of the anchored lines, without a final newline.
func SuggestionText(f Finding) string {
	return strings.TrimSuffix(strings.ReplaceAll(f.Fix, "\r\n", "\n"), "\n")
}

func (e *Engine) fixProblem(ctx context.Context, p *Prepared, f *Finding, policy map[string]string) string {
	fix := SuggestionText(*f)
	fixLines := strings.Split(fix, "\n")
	switch {
	case !fixAllowed(f, policy):
		return FixNotAllowed
	case f.Anchor.Side != scope.SideRight:
		return FixLeftSide // a suggestion replaces lines of the head
	case len(fixLines) > maxFixLines:
		return FixTooLong
	case strings.Contains(fix, "```"):
		return FixFence // it would end the suggestion block
	case strings.Contains(fix, redact.Placeholder):
		return FixRedacted // the model saw a redacted secret; never write the placeholder into code
	}
	if e.scanner != nil && len(e.scanner.Scan(fix)) > 0 {
		return FixSecret
	}
	before, err := p.Workspace.Show(ctx, p.PR.HeadSHA, f.Anchor.Path)
	if err != nil {
		return FixNoFile
	}
	lines := strings.Split(string(before), "\n")
	trailing := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailing {
		lines = lines[:len(lines)-1]
	}
	s, end := f.Anchor.StartLine, f.Anchor.EndLine
	if s < 1 || end < s || end > len(lines) {
		return FixBadRange
	}
	if strings.Join(lines[s-1:end], "\n") == fix {
		return FixUnchanged
	}
	// A suggestion replaces exactly the anchored lines. A fix that starts
	// with the line above or ends with the line below implies an edit
	// outside the range (the model repeated context), which GitHub would
	// apply as a duplicated line.
	if s > 1 && strings.TrimSpace(lines[s-2]) != "" && fixLines[0] == lines[s-2] {
		return FixOutside
	}
	if end < len(lines) && strings.TrimSpace(lines[end]) != "" && fixLines[len(fixLines)-1] == lines[end] {
		return FixOutside
	}
	after := append(append(append([]string{}, lines[:s-1]...), fixLines...), lines[end:]...)
	afterText := strings.Join(after, "\n")
	if trailing {
		afterText += "\n"
	}
	if problem := e.checkSyntax(ctx, f.Anchor.Path, before, []byte(afterText)); problem != "" {
		return problem
	}
	return e.checkFormat(ctx, p.Workspace.Dir, f.Anchor.Path, before, []byte(afterText))
}

// checkSyntax rejects a change that introduces parse errors. Go uses
// go/parser, plus gofmt when the original was formatted. C, C++, C#, Java,
// JavaScript, TypeScript, Python, Ruby and Rust use tree-sitter: the fix
// may not add ERROR or MISSING nodes, so errors already in the file are
// not blamed on it. Other languages' fixes are not posted.
func (e *Engine) checkSyntax(ctx context.Context, path string, before, after []byte) string {
	if filepath.Ext(path) != ".go" {
		if !syntax.Supported(path) {
			return FixNoParser
		}
		nb, err := syntax.Errors(ctx, path, before)
		if err != nil {
			return FixParserError
		}
		na, err := syntax.Errors(ctx, path, after)
		if err != nil {
			return FixParserError
		}
		if na > nb {
			return FixParse
		}
		return ""
	}
	fset := token.NewFileSet()
	_, errBefore := parser.ParseFile(fset, path, before, parser.ParseComments)
	if _, err := parser.ParseFile(fset, path, after, parser.ParseComments); err != nil && errBefore == nil {
		return FixParse
	}
	if formatted, err := format.Source(before); err == nil && bytes.Equal(formatted, before) {
		if formatted, err := format.Source(after); err != nil || !bytes.Equal(formatted, after) {
			return FixFormat
		}
	}
	return ""
}

// Formatter checks (spec §6.11: "where cheap and available"): when the
// original file is clean under the formatter and the fixed one is not,
// the fix is rejected. clang-format reads the repo's .clang-format (plain
// YAML) through --assume-filename; prettier runs with --no-config, because
// its config files may be JavaScript from the pull request, so a repo
// whose style differs from prettier's defaults is not checked at all.
// Formatters missing from PATH are skipped.

var formatters = []struct {
	bin  string
	exts []string
	args func(path string) []string
}{
	{
		"clang-format",
		[]string{".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx", ".ipp", ".inl"},
		func(p string) []string { return []string{"--assume-filename=" + p} },
	},
	{
		"prettier",
		[]string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".css", ".json", ".yaml", ".yml"},
		func(p string) []string { return []string{"--no-config", "--no-editorconfig", "--stdin-filepath", p} },
	},
}

// formatTimeout bounds one formatter run.
const formatTimeout = 10 * time.Second

// checkFormat runs the file's formatter, if one is installed, on before
// and after. dir is the checkout, so clang-format finds the repo's style.
func (e *Engine) checkFormat(ctx context.Context, dir, path string, before, after []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	for _, f := range formatters {
		if !slices.Contains(f.exts, ext) {
			continue
		}
		bin, err := exec.LookPath(f.bin)
		if err != nil {
			return ""
		}
		run := func(content []byte) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, formatTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, f.args(path)...)
			cmd.Dir = dir
			cmd.Stdin = bytes.NewReader(content)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
			return cmd.Output()
		}
		fb, err := run(before)
		if err != nil || !bytes.Equal(fb, before) {
			return "" // the original is not clean (or the formatter failed): nothing to hold the fix to
		}
		fa, err := run(after)
		if err != nil || !bytes.Equal(fa, after) {
			return FixFormat
		}
		return ""
	}
	return ""
}
