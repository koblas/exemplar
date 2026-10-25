package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
)

// RoundSummaryMarker identifies the one round summary comment on a PR.
const RoundSummaryMarker = "<!-- exemplar:round-summary -->"

// RoundSummary is what a PR past its round cap is told (FR-C8): every
// finding still open, and the new ones held back instead of posted inline.
type RoundSummary struct {
	// Rounds is the PR's rounds since its last reset; Max is its cap.
	Rounds, Max int
	// Open are findings earlier pushes posted that are still open.
	Open []OpenFinding
	// Result is this push's review: findings it posted inline (blocking
	// and Security ones still post) and the summarized ones.
	Result *engine.Result
	// LinkBase is the repository's web URL ("https://github.com/org/repo")
	// and Head the commit links point at.
	LinkBase, Head string
}

// Body renders the summary comment.
func (s RoundSummary) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Exemplar: review rounds**\n\nThis PR has had %d review rounds that raised new findings since the last human review (the limit is %d). "+
		"New findings are listed here instead of inline. The change may need a different approach, or a reviewer's eyes; "+
		"a review or review comment from a person resets the count.\n", s.Rounds, s.Max)
	var open, held []string
	for _, f := range s.Open {
		open = append(open, fmt.Sprintf("- `%s` %s", f.RuleID, s.link(f.Path, int(f.StartLine), int(f.EndLine))))
	}
	if s.Result != nil {
		for _, f := range s.Result.Findings {
			switch f.State {
			case engine.StatePosted:
				open = append(open, fmt.Sprintf("- `%s` %s", f.RuleID, s.link(f.Anchor.Path, f.Anchor.StartLine, f.Anchor.EndLine)))
			case engine.StateSummarized:
				held = append(held, fmt.Sprintf("- `%s` %s: %s", f.RuleID, s.link(f.Anchor.Path, f.Anchor.StartLine, f.Anchor.EndLine), firstLine(f.Message)))
			}
		}
	}
	if len(open) > 0 {
		fmt.Fprintf(&b, "\n**Still open** (%d)\n\n%s\n", len(open), strings.Join(open, "\n"))
	}
	if len(held) > 0 {
		fmt.Fprintf(&b, "\n**New on this push, not posted inline** (%d)\n\n%s\n", len(held), strings.Join(held, "\n"))
	}
	return b.String()
}

// link is a path and line range linked to the head commit.
func (s RoundSummary) link(path string, start, end int) string {
	lines := fmt.Sprintf("L%d", start)
	if end > start {
		lines += fmt.Sprintf("-L%d", end)
	}
	return fmt.Sprintf("[%s:%s](%s/blob/%s/%s#%s)", path, strings.TrimPrefix(lines, "L"), s.LinkBase, s.Head, path, lines)
}

// firstLine is a finding message's first line, as a list item can hold it.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// PostRoundSummary creates or updates the PR's round summary comment.
func PostRoundSummary(ctx context.Context, poster *gh.Poster, pr gh.PR, s RoundSummary) error {
	if _, _, err := poster.UpsertComment(ctx, pr, RoundSummaryMarker, s.Body()); err != nil {
		return fmt.Errorf("round summary: %w", err)
	}
	return nil
}
