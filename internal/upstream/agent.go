// Package upstream measures whether rule exports work: it
// recognises PRs written with a coding agent, and which
// exported instruction files that agent reads, so violation rates can be
// compared between PRs whose agent received a rule's export and PRs
// that did not.
package upstream

import (
	"regexp"
	"slices"
	"strings"
)

// Agents, and the export targets (internal/exports) each reads.
var reads = map[string][]string{
	"claude_code": {"claude_md"},
	"copilot":     {"copilot", "agents_md"},
	"cursor":      {"cursor", "agents_md"},
	"codex":       {"agents_md"},
	"devin":       {"agents_md"},
	"gemini":      {"agents_md"},
}

// Reads returns the export targets agent reads. An agent known only by a
// label reads AGENTS.md, the common format.
func Reads(agent string) []string {
	if agent == "" {
		return nil
	}
	if r, ok := reads[agent]; ok {
		return slices.Clone(r)
	}
	return []string{"agents_md"}
}

// markers name an agent by a substring of a co-author trailer, a bot
// login or a label, checked in order.
var markers = []struct{ sub, agent string }{
	{"anthropic.com", "claude_code"}, {"claude", "claude_code"},
	{"copilot", "copilot"},
	{"cursor", "cursor"},
	{"codex", "codex"}, {"openai.com", "codex"},
	{"devin", "devin"},
	{"gemini", "gemini"}, {"jules", "gemini"},
}

var trailer = regexp.MustCompile(`(?im)^co-authored-by:\s*(.+)$`)

var labelAgent = regexp.MustCompile(`^(?:ai-)?agent[:/]\s*([a-z0-9_.-]+)$`)

// Detect names the coding agent a PR was written with, or "": from a
// bot author, a Co-authored-by trailer, or a label ("copilot",
// "agent:cursor"). Only these explicit marks count; the PR's text is not
// guessed at.
func Detect(author string, labels, commitMessages []string) string {
	match := func(s string) string {
		s = strings.ToLower(s)
		for _, m := range markers {
			if strings.Contains(s, m.sub) {
				return m.agent
			}
		}
		return ""
	}
	if a := strings.ToLower(author); strings.HasSuffix(a, "[bot]") || a == "copilot" {
		if agent := match(a); agent != "" {
			return agent
		}
	}
	for _, msg := range commitMessages {
		for _, m := range trailer.FindAllStringSubmatch(msg, -1) {
			if agent := match(m[1]); agent != "" {
				return agent
			}
		}
	}
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if m := labelAgent.FindStringSubmatch(l); m != nil {
			if agent := match(m[1]); agent != "" {
				return agent
			}
			return m[1]
		}
		if agent := match(l); agent != "" && len(l) <= len("claude-code") {
			return agent
		}
	}
	return ""
}
