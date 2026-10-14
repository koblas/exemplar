// Package exports renders rule exports into coding-agent instruction files
// (spec §10.2, PRD FR-S6): each opted-in repo's active rules with
// export.agents get their one-line export.summary, grouped by directory,
// between BEGIN/END exemplar markers in the targets the repo chose.
// Anything outside the markers is the team's and is never touched.
package exports

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

// Targets (repo config exports.targets).
const (
	AgentsMD = "agents_md"
	ClaudeMD = "claude_md"
	Copilot  = "copilot"
	Cursor   = "cursor"
)

// Size budgets per file (spec §10.2): Copilot reads 4,000 characters of
// instructions for code review.
const (
	CopilotBudget = 3000
	DefaultBudget = 8000
)

// Begin and End delimit the generated block.
const (
	Begin = "<!-- BEGIN exemplar: generated from exemplar-config; edit the rules there, not here -->"
	End   = "<!-- END exemplar -->"
)

var blockRE = regexp.MustCompile(`(?s)<!-- BEGIN exemplar[^>]*-->.*?<!-- END exemplar -->`)

// File is one generated file: its block, and for a file Exemplar creates
// whole, the frontmatter that precedes it.
type File struct {
	Path        string
	Target      string
	Frontmatter string
	Block       string
	Rules       []string // rule ids, in order
	Dropped     int      // rules left out by the size budget
}

// Hash is the block's content hash, for drift and hand-edit detection.
func Hash(block string) string {
	s := sha256.Sum256([]byte(block))
	return hex.EncodeToString(s[:])
}

// area is the literal directory all of a rule's include patterns share
// ("" for the repo root): "src/net/**" and "src/net/*.cc" are "src/net".
func area(patterns []string) string {
	var prefix []string
	first := true
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") {
			continue
		}
		var lit []string
		for _, seg := range strings.Split(path.Dir(p), "/") {
			if seg == "." || strings.ContainsAny(seg, "*?[{") {
				break
			}
			lit = append(lit, seg)
		}
		if first {
			prefix, first = lit, false
			continue
		}
		n := 0
		for n < len(prefix) && n < len(lit) && prefix[n] == lit[n] {
			n++
		}
		prefix = prefix[:n]
	}
	return strings.Join(prefix, "/")
}

// slug names an area in a file name.
func slug(a string) string {
	if a == "" {
		return "repo"
	}
	return strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(a), "-"), "-")
}

func severityRank(s string) int {
	switch s {
	case schema.SeverityBlocking:
		return 0
	case schema.SeverityImportant:
		return 1
	}
	return 2
}

// Exportable are the rules a repo exports: active, with export.agents and
// a summary.
func Exportable(rules []config.Rule) []config.Rule {
	var out []config.Rule
	for _, r := range rules {
		if r.Status == schema.StatusActive && r.Export != nil && r.Export.Agents && strings.TrimSpace(r.Export.Summary) != "" {
			out = append(out, r)
		}
	}
	return out
}

// Render returns the files the repo's targets need. Rules are ordered by
// severity, then id; past the budget the rest are dropped with a note.
func Render(rules []config.Rule, targets []string) []File {
	byArea := map[string][]config.Rule{}
	for _, r := range rules {
		a := area(r.FilePatterns)
		byArea[a] = append(byArea[a], r)
	}
	areas := make([]string, 0, len(byArea))
	for a := range byArea {
		areas = append(areas, a)
	}
	slices.Sort(areas)
	var out []File
	for _, t := range targets {
		for _, a := range areas {
			rs := byArea[a]
			slices.SortFunc(rs, func(x, y config.Rule) int {
				return cmp.Or(cmp.Compare(severityRank(x.Severity), severityRank(y.Severity)), cmp.Compare(x.ID, y.ID))
			})
			f := File{Target: t}
			budget := DefaultBudget
			switch t {
			case AgentsMD, ClaudeMD:
				name := map[string]string{AgentsMD: "AGENTS.md", ClaudeMD: "CLAUDE.md"}[t]
				f.Path = path.Join(a, name)
			case Copilot:
				budget = CopilotBudget
				if a == "" {
					f.Path = ".github/copilot-instructions.md"
				} else {
					f.Path = ".github/instructions/" + slug(a) + ".instructions.md"
					f.Frontmatter = fmt.Sprintf("---\napplyTo: %q\n---\n\n", a+"/**")
				}
			case Cursor:
				if a == "" {
					f.Path = ".cursor/rules/exemplar.mdc"
					f.Frontmatter = "---\ndescription: Code review rules checked by Exemplar\nalwaysApply: true\n---\n\n"
				} else {
					f.Path = ".cursor/rules/exemplar-" + slug(a) + ".mdc"
					f.Frontmatter = fmt.Sprintf("---\ndescription: Code review rules for %s, checked by Exemplar\nglobs: %s\nalwaysApply: false\n---\n\n", a, a+"/**")
				}
			default:
				continue
			}
			f.Block, f.Rules, f.Dropped = block(a, rs, budget-len(f.Frontmatter))
			out = append(out, f)
		}
	}
	return out
}

func block(a string, rules []config.Rule, budget int) (string, []string, int) {
	head := Begin + "\n## Code review rules\n\n"
	if a == "" {
		head += "Exemplar checks these in code review. Follow them in this repository.\n\n"
	} else {
		head += fmt.Sprintf("Exemplar checks these in code review for `%s/`.\n\n", a)
	}
	tail := End + "\n"
	note := func(n int) string { return fmt.Sprintf("\n*%d more rules are not shown here (size budget).*\n", n) }
	lines := make([]string, len(rules))
	total := len(head) + len(tail)
	for i, r := range rules {
		lines[i] = fmt.Sprintf("- **%s**: %s (`%s`)\n", r.Severity, strings.TrimSpace(r.Export.Summary), r.ID)
		total += len(lines[i])
	}
	ids := make([]string, 0, len(rules))
	if total <= budget {
		for _, r := range rules {
			ids = append(ids, r.ID)
		}
		return head + strings.Join(lines, "") + tail, ids, 0
	}
	// Over budget: keep what fits beside the note.
	size := len(head) + len(tail) + len(note(len(rules)))
	kept := 0
	for kept < len(rules) && size+len(lines[kept]) <= budget {
		size += len(lines[kept])
		ids = append(ids, rules[kept].ID)
		kept++
	}
	return head + strings.Join(lines[:kept], "") + note(len(rules)-kept) + tail, ids, len(rules) - kept
}

// Current is a file's generated block as it is ("" when it has none).
func Current(content string) string {
	b := blockRE.FindString(content)
	if b == "" {
		return ""
	}
	return b + "\n"
}

// Merge puts block into content: in place of the existing block, or
// appended after the team's text. A file that does not exist yet gets the
// frontmatter too.
func Merge(content string, exists bool, f File) string {
	if !exists {
		return f.Frontmatter + f.Block
	}
	if loc := blockRE.FindStringIndex(content); loc != nil {
		rest := content[loc[1]:]
		rest = strings.TrimPrefix(rest, "\n")
		return content[:loc[0]] + f.Block + rest
	}
	switch {
	case strings.TrimSpace(content) == "":
		return f.Block
	case strings.HasSuffix(content, "\n"):
		return content + "\n" + f.Block
	}
	return content + "\n\n" + f.Block
}
