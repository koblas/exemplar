package pipeline

import (
	"slices"
	"strings"
	"testing"
)

func code(lines ...string) *string {
	s := strings.Join(lines, "\n") + "\n"
	return &s
}

func TestReanchor(t *testing.T) {
	// The anchor is line 3 ("b := f()") with two lines of context.
	old := []string{"package x", "a := 1", "b := f()", "c := 2", "d := 3"}
	anchor := code(old[0:5]...)
	open := []OpenFinding{
		{ID: 1, Side: "RIGHT", Path: "x.go", StartLine: 3, EndLine: 3, AnchorCode: anchor},    // moves down 2
		{ID: 2, Side: "RIGHT", Path: "same.go", StartLine: 3, EndLine: 3, AnchorCode: anchor}, // stays
		{ID: 3, Side: "RIGHT", Path: "gone.go", StartLine: 3, EndLine: 3, AnchorCode: anchor}, // code removed
		{ID: 4, Side: "RIGHT", Path: "deleted.go", StartLine: 3, EndLine: 3, AnchorCode: anchor},
		{ID: 5, Side: "RIGHT", Path: "x.go", StartLine: 1, EndLine: 1}, // no stored code: not planned
	}
	files := map[string][]string{
		"x.go":    append([]string{"// header", "// more"}, old...),
		"same.go": old,
		"gone.go": {"package x", "a := 1", "b := g()", "c := 2", "d := 3"},
	}
	plan := Reanchor(open, func(_, path string) ([]string, bool) {
		l, ok := files[path]
		return l, ok
	})
	if len(plan.Moves) != 1 || plan.Moves[0].Finding.ID != 1 || plan.Moves[0].Start != 5 || plan.Moves[0].End != 5 {
		t.Errorf("moves = %+v", plan.Moves)
	}
	var outdated []int64
	for _, c := range plan.Outdated {
		outdated = append(outdated, c.Finding.ID)
		if c.Reply != RebaseReply || c.State != "outdated" {
			t.Errorf("closure = %+v", c)
		}
	}
	if !slices.Equal(outdated, []int64{3, 4}) || len(plan.Kept) != 2 {
		t.Errorf("outdated = %v, kept = %+v", outdated, plan.Kept)
	}
}

// Two findings on identical code each take the match nearest their old
// line; a third with no match left is outdated.
func TestReanchorIdenticalCode(t *testing.T) {
	// The anchor is the middle line of the block, two lines of context
	// each side; the old file had the block at lines 1-5 and 11-15.
	block := []string{"for {", "x++", "y++", "z++", "}"}
	anchor := code(block...)
	var file []string
	for range 2 {
		file = append(file, "// pad", "// pad", "// pad")
		file = append(file, block...)
		file = append(file, "// pad", "// pad")
	}
	// The blocks now start at lines 4 and 14, so the anchors sit at 6 and 16.
	open := []OpenFinding{
		{ID: 1, Side: "RIGHT", Path: "f.go", StartLine: 13, EndLine: 13, AnchorCode: anchor},
		{ID: 2, Side: "RIGHT", Path: "f.go", StartLine: 3, EndLine: 3, AnchorCode: anchor},
		{ID: 3, Side: "RIGHT", Path: "f.go", StartLine: 40, EndLine: 40, AnchorCode: anchor},
	}
	plan := Reanchor(open, func(string, string) ([]string, bool) { return file, true })
	got := map[int64]int{}
	for _, m := range plan.Moves {
		got[m.Finding.ID] = m.Start
	}
	if got[2] != 6 || got[1] != 16 || len(plan.Outdated) != 1 || plan.Outdated[0].Finding.ID != 3 {
		t.Errorf("moves = %v, outdated = %+v", got, plan.Outdated)
	}
}
