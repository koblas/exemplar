package mirror

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/gitexec"
)

// Exclusions returns repo's exclusion rules from the
// .gitattributes and .exemplarignore at commit; missing files mean none.
func (m *Manager) Exclusions(ctx context.Context, repo, commit string) (exclude.Rules, error) {
	dir, err := m.mirrored(repo)
	if err != nil {
		return exclude.Rules{}, err
	}
	sha, err := m.commit(ctx, dir, commit)
	if err != nil {
		return exclude.Rules{}, err
	}
	read := func(p string) string {
		out, err := m.git(ctx, dir, "cat-file", "blob", sha+":"+p)
		if err != nil {
			return ""
		}
		return string(out)
	}
	return exclude.Load(read(".gitattributes"), read(".exemplarignore")), nil
}

// GrepMatch is one matching line.
type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// MaxPattern bounds a search pattern's length.
const MaxPattern = 200

// Grep runs git grep with an extended regexp at commit (degraded-mode
// search_text). pathGlob, when set, limits the paths (git
// glob pathspec). At most limit matches are returned; truncated reports
// whether more existed.
func (m *Manager) Grep(ctx context.Context, repo, commit, pattern, pathGlob string, limit int) (matches []GrepMatch, truncated bool, err error) {
	if strings.ContainsAny(pathGlob, "\x00\n") || strings.HasPrefix(pathGlob, ":") {
		return nil, false, fmt.Errorf("%w: bad path filter", ErrBadRequest)
	}
	var specs []string
	if pathGlob != "" {
		specs = []string{pathGlob}
	}
	return m.grep(ctx, repo, commit, "-E", pattern, specs, "GIT_GLOB_PATHSPECS=1", limit)
}

// GrepWord finds whole-word, fixed-string occurrences of word at commit
// (degraded-mode find_definition candidates).
func (m *Manager) GrepWord(ctx context.Context, repo, commit, word string, limit int) ([]GrepMatch, bool, error) {
	return m.grep(ctx, repo, commit, "-Fw", word, nil, "", limit)
}

// GrepIn runs git grep at commit restricted to paths, each a file or a
// directory (its subtree), taken literally. word true searches for the
// whole-word fixed string pattern; false, the extended regexp pattern.
// Degraded-mode find_references and dependents.
func (m *Manager) GrepIn(ctx context.Context, repo, commit, pattern string, word bool, paths []string, limit int) ([]GrepMatch, bool, error) {
	if len(paths) == 0 {
		return []GrepMatch{}, false, nil
	}
	for _, p := range paths {
		if err := CleanPath(strings.TrimSuffix(p, "/")); err != nil && p != "." {
			return nil, false, err
		}
	}
	mode := "-E"
	if word {
		mode = "-Fw"
	}
	return m.grep(ctx, repo, commit, mode, pattern, paths, "GIT_LITERAL_PATHSPECS=1", limit)
}

func (m *Manager) grep(ctx context.Context, repo, commit, mode, pattern string, pathspecs []string, specEnv string, limit int) (matches []GrepMatch, truncated bool, err error) {
	dir, err := m.mirrored(repo)
	if err != nil {
		return nil, false, err
	}
	if pattern == "" || len(pattern) > MaxPattern || strings.ContainsAny(pattern, "\x00\n") {
		return nil, false, fmt.Errorf("%w: pattern must be 1-%d characters on one line", ErrBadRequest, MaxPattern)
	}
	sha, err := m.commit(ctx, dir, commit)
	if err != nil {
		return nil, false, err
	}
	// -e keeps a pattern starting with "-" from being read as a flag;
	// -I skips binaries; --max-count bounds work per file.
	args := append([]string{"grep", "-n", "-I", mode, "--no-color", "--max-count=50", "-e", pattern, sha, "--"}, pathspecs...)
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Glob pathspecs for a filter (a leading ":", other magic, is refused
	// by Grep), or literal ones for GrepIn.
	cmd.Env = gitexec.Env("")
	if specEnv != "" {
		cmd.Env = append(cmd.Env, specEnv)
	}
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return []GrepMatch{}, false, nil // no matches
	}
	if err != nil {
		if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "regex") {
			return nil, false, fmt.Errorf("%w: invalid pattern", ErrBadRequest)
		}
		return nil, false, fmt.Errorf("mirror: git grep: %w", err)
	}
	matches = []GrepMatch{}
	prefix := sha + ":"
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		p, rest, ok1 := strings.Cut(rest, ":")
		num, text, ok2 := strings.Cut(rest, ":")
		n, err := strconv.Atoi(num)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		if len(matches) == limit {
			return matches, true, nil
		}
		matches = append(matches, GrepMatch{Path: p, Line: n, Text: text})
	}
	return matches, false, nil
}
