// Package exclude decides which paths are never reviewed, mined or indexed:
// lockfiles, minified assets, paths
// marked linguist-generated or linguist-vendored in .gitattributes, and
// patterns from the repo's .exemplarignore. Both files are read from the
// default branch. Review, ingest and indexing share these rules so the
// same paths are excluded everywhere.
package exclude

import (
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

var lockfiles = []string{
	"go.sum", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml",
	"Cargo.lock", "poetry.lock", "Pipfile.lock", "uv.lock", "Gemfile.lock", "composer.lock",
}

// Rules are one repo's exclusions.
type Rules struct {
	attrs   []attr
	ignores []string
}

type attr struct{ pattern, name string }

// Load parses a root .gitattributes and .exemplarignore (either may be empty).
func Load(gitattributes, exemplarignore string) Rules {
	return Rules{attrs: parseLinguist(gitattributes), ignores: parseIgnore(exemplarignore)}
}

// Reason returns why p is excluded, or "" when it is not.
func (r Rules) Reason(p string) string {
	switch {
	case slices.Contains(lockfiles, path.Base(p)):
		return "lockfile"
	case strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".min.css"):
		return "minified"
	}
	for _, a := range r.attrs {
		if doublestar.MatchUnvalidated(a.pattern, p) {
			return a.name
		}
	}
	for _, pat := range r.ignores {
		if doublestar.MatchUnvalidated(pat, p) {
			return ".exemplarignore"
		}
	}
	return ""
}

// parseLinguist reads linguist-generated and linguist-vendored entries.
// Patterns follow gitignore rules: a pattern without a slash matches at any
// depth. Only the root .gitattributes is read.
func parseLinguist(text string) []attr {
	var out []attr
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		for _, a := range fields[1:] {
			for _, name := range []string{"linguist-generated", "linguist-vendored"} {
				if a == name || a == name+"=true" {
					out = append(out, attr{gitPattern(fields[0]), name})
				}
			}
		}
	}
	return out
}

func parseIgnore(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		out = append(out, gitPattern(line))
	}
	return out
}

// gitPattern converts a gitignore-style pattern to a doublestar pattern.
func gitPattern(p string) string {
	dirOnly := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(p, "/")
	} else if !strings.Contains(p, "/") {
		p = "**/" + p
	}
	if dirOnly {
		p += "/**"
	}
	if !doublestar.ValidatePattern(p) {
		return "\x00invalid" // matches nothing
	}
	return p
}
