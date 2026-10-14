// Package codeowners parses GitHub CODEOWNERS files. Ingest uses it for
// path owners and codeowner approval; Stage 2 uses it to check that every
// touched path's owners are auto-approvable (spec §6.13).
package codeowners

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Locations are where GitHub looks for CODEOWNERS, in its order: the
// first that exists is the only one used.
var Locations = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// File is a parsed CODEOWNERS file. The last matching rule wins, as on
// GitHub. Owners are "@user", "@org/team" or an email address.
type File struct{ rules []rule }

type rule struct {
	pattern string
	// dirOnly: the pattern ends in "/*", which GitHub matches against a
	// directory's direct children only.
	dirOnly bool
	owners  []string
}

// Parse parses CODEOWNERS text; lines it cannot read are skipped.
func Parse(text string) File {
	var f File
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pat, ok := pattern(fields[0])
		if !ok {
			continue
		}
		f.rules = append(f.rules, rule{pattern: pat, dirOnly: strings.HasSuffix(pat, "/*"), owners: fields[1:]})
	}
	return f
}

// Empty reports whether the file has no rules (including no file at all).
func (f File) Empty() bool { return len(f.rules) == 0 }

// Owners returns the owners of p (nil when no rule matches, or the last
// matching rule names no owner).
func (f File) Owners(p string) []string {
	for i := len(f.rules) - 1; i >= 0; i-- {
		r := f.rules[i]
		if doublestar.MatchUnvalidated(r.pattern, p) || (!r.dirOnly && doublestar.MatchUnvalidated(r.pattern+"/**", p)) {
			return r.owners
		}
	}
	return nil
}

// pattern converts a CODEOWNERS (gitignore-style) pattern.
func pattern(p string) (string, bool) {
	anchored := strings.HasPrefix(p, "/") || strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.Trim(p, "/")
	if p == "" || p == "*" {
		return "**", true
	}
	if !anchored {
		p = "**/" + p
	}
	return p, doublestar.ValidatePattern(p)
}
