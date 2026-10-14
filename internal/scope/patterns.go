package scope

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Patterns is a compiled rule file_patterns list. Entries prefixed with "!"
// exclude paths. A path matches when it matches at least one include pattern
// and no exclude pattern; order does not matter.
type Patterns struct {
	include []string
	exclude []string
}

// ErrNoIncludePattern is returned for a pattern list that could never match.
var ErrNoIncludePattern = errors.New("file_patterns has no include pattern")

// CompilePatterns validates a file_patterns list. Paths are slash-separated
// and relative to the repository root, as in diffs and the GitHub API.
func CompilePatterns(patterns []string) (Patterns, error) {
	var p Patterns
	for _, raw := range patterns {
		pat, negated := strings.CutPrefix(raw, "!")
		if pat == "" || !doublestar.ValidatePattern(pat) {
			return Patterns{}, fmt.Errorf("invalid file pattern %q", raw)
		}
		if negated {
			p.exclude = append(p.exclude, pat)
		} else {
			p.include = append(p.include, pat)
		}
	}
	if len(p.include) == 0 {
		return Patterns{}, ErrNoIncludePattern
	}
	return p, nil
}

// Match reports whether path is in scope.
func (p Patterns) Match(path string) bool {
	return matchAny(p.include, path) && !matchAny(p.exclude, path)
}

// Filter returns the paths that are in scope, preserving order.
func (p Patterns) Filter(paths []string) []string {
	var out []string
	for _, path := range paths {
		if p.Match(path) {
			out = append(out, path)
		}
	}
	return out
}

func matchAny(patterns []string, path string) bool {
	for _, pat := range patterns {
		// Patterns are validated at compile time, so MatchUnvalidated cannot
		// fail on a bad pattern here.
		if doublestar.MatchUnvalidated(pat, path) {
			return true
		}
	}
	return false
}
