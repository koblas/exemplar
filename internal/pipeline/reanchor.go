package pipeline

import (
	"cmp"
	"slices"
	"strings"
)

// Re-anchoring after a rebase or force-push: an open
// finding from a head the new one does not descend from is looked up by
// its normalized anchor code in the same file at the new head (the new
// base for a LEFT anchor). A match moves the finding there; no match makes
// it outdated, and its thread is resolved with "No longer present after
// rebase". The fingerprint (rule, path, code, k) stays valid when the code
// moves within the file, so later reviews still recognize it.

// Move is a finding whose code now sits at other lines.
type Move struct {
	Finding    OpenFinding
	Start, End int
}

// ReanchorPlan is what re-anchoring does.
type ReanchorPlan struct {
	Moves    []Move
	Outdated []Closure
	// Kept are matched findings, moved or not.
	Kept []OpenFinding
}

// RebaseReply is the thread reply for a finding a rebase removed.
const RebaseReply = "No longer present after rebase."

// Reanchor plans re-anchoring for findings of rewritten heads. content
// returns a file's lines at the new head (RIGHT) or new base (LEFT), and
// false when it no longer exists. Findings without stored anchor code
// (posted before it was kept) are left out of the plan entirely.
//
// Findings with identical code in one file each take the unused match
// nearest their old line, in line order, so two never land on one place.
func Reanchor(findings []OpenFinding, content func(side, path string) ([]string, bool)) ReanchorPlan {
	var plan ReanchorPlan
	type group struct {
		side, path string
		code       []string
		members    []OpenFinding
	}
	groups := map[string]*group{}
	var order []string
	for _, f := range findings {
		if f.AnchorCode == nil || *f.AnchorCode == "" {
			continue
		}
		k := f.Side + "\x00" + f.Path + "\x00" + *f.AnchorCode
		g, ok := groups[k]
		if !ok {
			g = &group{side: f.Side, path: f.Path, code: strings.Split(strings.TrimSuffix(*f.AnchorCode, "\n"), "\n")}
			groups[k] = g
			order = append(order, k)
		}
		g.members = append(g.members, f)
	}
	for _, k := range order {
		g := groups[k]
		slices.SortFunc(g.members, func(a, b OpenFinding) int { return cmp.Compare(a.StartLine, b.StartLine) })
		lines, ok := content(g.side, g.path)
		var matches []int // 1-based first line of each window that matches
		if ok {
			matches = windows(lines, g.code)
		}
		used := make([]bool, len(matches))
		for _, f := range g.members {
			before := min(2, int(f.StartLine)-1) // context lines above the anchor
			span := int(f.EndLine - f.StartLine)
			best := -1
			for i, m := range matches {
				if used[i] {
					continue
				}
				if best < 0 || abs(m+before-int(f.StartLine)) < abs(matches[best]+before-int(f.StartLine)) {
					best = i
				}
			}
			if best < 0 {
				plan.Outdated = append(plan.Outdated, Closure{Finding: f, Reason: ClosedOutdated, State: "outdated", Reply: RebaseReply})
				continue
			}
			used[best] = true
			start := matches[best] + before
			if start != int(f.StartLine) {
				plan.Moves = append(plan.Moves, Move{Finding: f, Start: start, End: start + span})
				f.StartLine, f.EndLine = int32(start), int32(start+span)
			}
			plan.Kept = append(plan.Kept, f)
		}
	}
	return plan
}

// windows returns where code (normalized lines) occurs in lines.
func windows(lines, code []string) []int {
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = strings.Join(strings.Fields(l), " ")
	}
	var out []int
	for i := 0; i+len(code) <= len(norm); i++ {
		if slices.Equal(norm[i:i+len(code)], code) {
			out = append(out, i+1)
		}
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
