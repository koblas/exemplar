package scope

import (
	"errors"
	"fmt"
)

// Side is the side of the diff a review comment attaches to.
type Side string

// Diff sides, as named by the GitHub review comments API.
const (
	SideRight Side = "RIGHT" // the head version: added and context lines
	SideLeft  Side = "LEFT"  // the base version: deleted and context lines
)

// Anchor is a proposed review comment position. Model output is the usual
// source, so every field is untrusted until ValidateAnchor accepts it.
type Anchor struct {
	Path      string
	Side      Side
	StartLine int
	EndLine   int
}

// Reasons an anchor is rejected. Use errors.Is to test for them.
var (
	ErrUnknownPath  = errors.New("path not in diff")
	ErrInvalidSide  = errors.New("invalid side")
	ErrInvalidRange = errors.New("invalid line range")
	ErrNotInHunk    = errors.New("lines not within a single diff hunk")
)

// ValidateAnchor reports whether GitHub would accept a as a review comment
// position. GitHub only allows comments on lines that appear in the diff, and
// a multi-line comment must start and end in the same hunk on the same side.
// Rejected anchors must be dropped, never adjusted.
func (d *Diff) ValidateAnchor(a Anchor) error {
	f, ok := d.File(a.Path)
	if !ok {
		return fmt.Errorf("anchor %s: %w", a.Path, ErrUnknownPath)
	}
	if a.Side != SideRight && a.Side != SideLeft {
		return fmt.Errorf("anchor %s: %w %q", a.Path, ErrInvalidSide, a.Side)
	}
	if a.StartLine < 1 || a.EndLine < a.StartLine {
		return fmt.Errorf("anchor %s:%d-%d: %w", a.Path, a.StartLine, a.EndLine, ErrInvalidRange)
	}
	for _, h := range f.Hunks {
		start, count := h.NewStart, h.NewLines
		if a.Side == SideLeft {
			start, count = h.OldStart, h.OldLines
		}
		if inRange(a.StartLine, start, count) && inRange(a.EndLine, start, count) {
			return nil
		}
	}
	return fmt.Errorf("anchor %s %s:%d-%d: %w", a.Path, a.Side, a.StartLine, a.EndLine, ErrNotInHunk)
}

// inRange reports whether line falls in start..start+count-1. It is written
// so that large values from a malformed hunk header cannot overflow.
func inRange(line, start, count int) bool {
	return count > 0 && line >= start && line-start < count
}
