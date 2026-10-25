package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/pathhistory"
	"github.com/koblas/exemplar/internal/redact"
)

// Tool limits.
const (
	maxReadLines   = 400
	maxGrepMatches = 200
	maxGrepPattern = 200
	maxDirEntries  = 500
	grepTimeout    = 5 * time.Second
)

// Tool errors. Their text is the tool result the model sees.
var (
	errNotReviewable   = errors.New("is not a reviewable changed file")
	errUnknownTool     = errors.New("unknown tool")
	errContentWithheld = errors.New("content withheld: it contains a secret that could not be redacted")
	errInvalidPath     = errors.New("invalid path")
	errOutsideRepo     = errors.New("is outside the repository")
	errBadRange        = errors.New("start and end must satisfy 1 <= start <= end")
	errPastEnd         = errors.New("lines")
	errCannotRead      = errors.New("cannot read")
	errBinaryFile      = errors.New("is a binary file")
	errPatternLength   = errors.New("pattern must be")
	errSearchTimedOut  = errors.New("search timed out")
	errSearchFailed    = errors.New("search failed")
	errCannotList      = errors.New("cannot list")
	errExcludedPath    = errors.New("is excluded from review")
)

// ToolCall is one logged tool invocation. Coverage is computed from this
// log, never from anything the model says.
type ToolCall struct {
	Name string
	Path string
	OK   bool
}

// toolbox executes the read-only tools for one agent. There is no shell,
// no network, no write and no code execution.
type toolbox struct {
	ws      *Workspace
	hunks   map[string]string
	scanner *redact.Scanner
	// history answers path_history; nil when no source is configured, in
	// which case the tool is not offered.
	history PathHistory
	repo    string
	baseSHA string
	// refs answers find_references at pin; nil when exemplar-search is
	// not configured or unreachable, in which case it is not offered.
	refs References
	pin  *searchPin
	// localRefs answers find_references from the checkout instead
	// (exemplar-search not configured).
	localRefs bool
	// overlay are the PR's reviewable files, searched at the head; touched
	// are every path it touches, whose index hits are stale.
	overlay, touched []string
	degraded         bool
	// exclusions are the repo's review exclusions: no tool returns an
	// excluded path's content or name.
	exclusions exclude.Rules

	mu  sync.Mutex
	log []ToolCall
	// usedContext is set once any file or search content was returned.
	usedContext bool
}

func newToolbox(p *Prepared, s *redact.Scanner, h PathHistory) *toolbox {
	return &toolbox{
		ws: p.Workspace, hunks: p.Hunks, scanner: s, history: h, repo: p.PR.Repo, baseSHA: p.PR.BaseSHA,
		exclusions: p.Exclusions,
	}
}

// PathHistory answers the path_history context tool:
// incidents, reverts and recent commits for a path as of the PR's base.
// internal/pathhistory implements it.
type PathHistory interface {
	History(ctx context.Context, repo, path, rev string) (pathhistory.Result, error)
}

var pathHistoryDef = toolDef{
	"path_history",
	"List a file's recent history before this pull request: incidents and reverts linked to it, and its recent commits. At most 20 entries, newest first.",
	map[string]any{"path": map[string]any{"type": "string"}},
	[]string{"path"},
}

// defs are the tools this agent is offered.
func (t *toolbox) defs() []toolDef {
	defs := slices.Clip(toolDefs)
	if t.history != nil {
		defs = append(defs, pathHistoryDef)
	}
	if t.refs != nil || t.localRefs {
		defs = append(defs, findReferencesDef)
	}
	return defs
}

func (t *toolbox) record(c ToolCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.log = append(t.log, c)
}

func (t *toolbox) calls() []ToolCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ToolCall(nil), t.log...)
}

// toolDef is one tool schema sent to the model.
type toolDef struct {
	Name, Description string
	Properties        map[string]any
	Required          []string
}

// toolDefs are the tool schemas every agent gets.
var toolDefs = []toolDef{
	{
		"get_hunk", "Return the diff hunks of a changed file in this pull request.",
		map[string]any{"path": map[string]any{"type": "string"}},
		[]string{"path"},
	},
	{
		"read_file", "Read lines of a file at the pull request head. At most 400 lines per call.",
		map[string]any{
			"path":  map[string]any{"type": "string"},
			"start": map[string]any{"type": "integer", "minimum": 1},
			"end":   map[string]any{"type": "integer", "minimum": 1},
		},
		[]string{"path", "start", "end"},
	},
	{
		"grep", "Search the checked-out files with a regular expression. Returns at most 200 matching lines.",
		map[string]any{
			"pattern": map[string]any{"type": "string"},
			"glob":    map[string]any{"type": "string"},
		},
		[]string{"pattern", "glob"},
	},
	{
		"list_dir", "List one directory level at the pull request head.",
		map[string]any{"path": map[string]any{"type": "string"}},
		[]string{"path"},
	},
}

// run executes a tool call and returns text for the tool_result. Errors are
// returned to the model as tool errors, not to the caller.
func (t *toolbox) run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	var in struct {
		Path      string `json:"path"`
		Start     int    `json:"start"`
		End       int    `json:"end"`
		Pattern   string `json:"pattern"`
		Glob      string `json:"glob"`
		Name      string `json:"name"`
		DefinedIn string `json:"defined_in"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		t.record(ToolCall{Name: name})
		return "", fmt.Errorf("invalid input: %w", err)
	}
	var (
		out string
		err error
	)
	switch name {
	case "get_hunk":
		h, ok := t.hunks[in.Path]
		if !ok {
			err = fmt.Errorf("%s %w", in.Path, errNotReviewable)
		}
		out = h
	case "read_file":
		out, err = t.readFile(ctx, in.Path, in.Start, in.End)
	case "grep":
		out, err = t.grep(ctx, in.Pattern, in.Glob)
		in.Path = ""
	case "list_dir":
		out, err = t.listDir(in.Path)
	case "path_history":
		if t.history == nil {
			err = fmt.Errorf("%w %q", errUnknownTool, name)
			break
		}
		var res pathhistory.Result
		if res, err = t.history.History(ctx, t.repo, in.Path, t.baseSHA); err == nil {
			out = pathhistory.Format(res)
		}
	case "find_references":
		switch {
		case t.localRefs:
			out, err = t.localReferences(ctx, in.Name, in.DefinedIn)
		case t.refs != nil:
			out, err = t.findReferences(ctx, in.Name, in.DefinedIn)
		default:
			err = fmt.Errorf("%w %q", errUnknownTool, name)
		}
		in.Path = ""
	default:
		err = fmt.Errorf("%w %q", errUnknownTool, name)
	}
	t.record(ToolCall{Name: name, Path: in.Path, OK: err == nil})
	if err != nil {
		return "", err
	}
	if name != "get_hunk" && name != "list_dir" {
		t.mu.Lock()
		t.usedContext = true
		t.mu.Unlock()
	}
	// Every context snippet is scanned like the diff.
	red, _, rerr := t.scanner.Redact(out)
	if rerr != nil {
		return "", errContentWithheld
	}
	return red, nil
}

// excluded reports whether rel, a repo-relative file or directory, is
// excluded from review. A directory is excluded when a file directly in
// it would be (vendor/** covers vendor).
func (t *toolbox) excluded(rel string, dir bool) bool {
	p := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "./")
	if p == "." || p == "" {
		return false
	}
	if t.exclusions.Reason(p) != "" {
		return true
	}
	return dir && t.exclusions.Reason(p+"/_") != ""
}

// resolve maps a repo-relative path to a file inside the worktree, refusing
// anything that escapes it, including through symlinks.
func (t *toolbox) resolve(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return "", fmt.Errorf("%w %q", errInvalidPath, rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".git" || strings.HasPrefix(clean, ".git"+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q %w", rel, errOutsideRepo)
	}
	root, err := filepath.EvalSymlinks(t.ws.Dir)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	full := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return full, fmt.Errorf("resolve %s: %w", rel, err) // may be outside the sparse cone; caller falls back to git
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q %w", rel, errOutsideRepo)
	}
	return resolved, nil
}

func (t *toolbox) readFile(ctx context.Context, rel string, start, end int) (string, error) {
	if start < 1 || end < start {
		return "", errBadRange
	}
	if end-start+1 > maxReadLines {
		end = start + maxReadLines - 1
	}
	if t.excluded(rel, false) {
		return "", fmt.Errorf("%s %w", rel, errExcludedPath)
	}
	full, err := t.resolve(rel)
	var data []byte
	switch {
	case err == nil:
		data, err = os.ReadFile(full)
	case errors.Is(err, os.ErrNotExist):
		// Outside the sparse cone: read the head commit from git.
		data, err = t.ws.Show(ctx, t.ws.HeadSHA, filepath.ToSlash(filepath.Clean(rel)))
	}
	if err != nil {
		return "", fmt.Errorf("%w %s", errCannotRead, rel)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s %w", rel, errBinaryFile)
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines) {
		return "", fmt.Errorf("%s has %d %w", rel, len(lines), errPastEnd)
	}
	end = min(end, len(lines))
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return b.String(), nil
}

func (t *toolbox) grep(ctx context.Context, pattern, glob string) (string, error) {
	if pattern == "" || len(pattern) > maxGrepPattern {
		return "", fmt.Errorf("%w 1-%d characters", errPatternLength, maxGrepPattern)
	}
	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	args := []string{"--no-follow", "--no-config", "--line-number", "--no-heading", "--color=never", "--max-columns=300", "-e", pattern}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, "--", ".")
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = t.ws.Dir
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "no matches", nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", errSearchTimedOut
		}
		return "", errSearchFailed
	}
	var lines []string
	for line := range strings.Lines(string(out)) {
		path, _, _ := strings.Cut(line, ":")
		if !t.excluded(path, false) {
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}
	}
	if len(lines) == 0 {
		return "no matches", nil
	}
	if len(lines) > maxGrepMatches {
		lines = append(lines[:maxGrepMatches], "(more matches omitted)")
	}
	return strings.Join(lines, "\n"), nil
}

func (t *toolbox) listDir(rel string) (string, error) {
	if rel == "" || rel == "." {
		rel = "."
	}
	full := t.ws.Dir
	if rel != "." {
		if t.excluded(rel, true) {
			return "", fmt.Errorf("%s %w", rel, errExcludedPath)
		}
		var err error
		if full, err = t.resolve(rel); err != nil {
			return "", fmt.Errorf("%w %s", errCannotList, rel)
		}
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", fmt.Errorf("%w %s", errCannotList, rel)
	}
	var b strings.Builder
	for i, e := range entries {
		if i == maxDirEntries {
			b.WriteString("(more entries omitted)\n")
			break
		}
		if e.Name() == ".git" || t.excluded(filepath.Join(rel, e.Name()), e.IsDir()) {
			continue
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		b.WriteString(name + "\n")
	}
	return b.String(), nil
}

// examined returns the scoped files the agent opened with get_hunk or
// read_file. Only these count toward coverage.
func examined(calls []ToolCall, scoped []string) map[string]bool {
	out := map[string]bool{}
	for _, c := range calls {
		if c.OK && (c.Name == "get_hunk" || c.Name == "read_file") {
			out[c.Path] = true
		}
	}
	for p := range out {
		if !containsString(scoped, p) {
			delete(out, p)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	return slices.Contains(list, s)
}
