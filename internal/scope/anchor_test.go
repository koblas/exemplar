package scope

import (
	"errors"
	"strings"
	"testing"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

func TestValidateAnchor(t *testing.T) {
	d := mustParse(t, sampleDiff)

	tests := []struct {
		name    string
		anchor  Anchor
		wantErr error
	}{
		{"added line", Anchor{"src/net/fetch.go", SideRight, 13, 13}, nil},
		{"context line right", Anchor{"src/net/fetch.go", SideRight, 10, 10}, nil},
		{"whole first hunk right", Anchor{"src/net/fetch.go", SideRight, 10, 16}, nil},
		{"whole first hunk left", Anchor{"src/net/fetch.go", SideLeft, 10, 15}, nil},
		{"deleted line left", Anchor{"src/net/fetch.go", SideLeft, 41, 41}, nil},
		{"second hunk right", Anchor{"src/net/fetch.go", SideRight, 41, 43}, nil},
		{"new file", Anchor{"docs/new.md", SideRight, 1, 3}, nil},
		{"removed file left", Anchor{"old/gone.go", SideLeft, 1, 2}, nil},
		{"renamed file right", Anchor{"pkg/b.go", SideRight, 2, 2}, nil},

		{"unknown path", Anchor{"nope.go", SideRight, 1, 1}, ErrUnknownPath},
		{"old path of rename", Anchor{"pkg/a.go", SideLeft, 2, 2}, ErrUnknownPath},
		{"bad side", Anchor{"src/net/fetch.go", "BOTH", 13, 13}, ErrInvalidSide},
		{"empty side", Anchor{"src/net/fetch.go", "", 13, 13}, ErrInvalidSide},
		{"zero line", Anchor{"src/net/fetch.go", SideRight, 0, 13}, ErrInvalidRange},
		{"negative line", Anchor{"src/net/fetch.go", SideRight, -5, -1}, ErrInvalidRange},
		{"reversed range", Anchor{"src/net/fetch.go", SideRight, 14, 13}, ErrInvalidRange},
		{"line past hunk", Anchor{"src/net/fetch.go", SideRight, 17, 17}, ErrNotInHunk},
		{"line between hunks", Anchor{"src/net/fetch.go", SideRight, 30, 30}, ErrNotInHunk},
		{"range spans two hunks", Anchor{"src/net/fetch.go", SideRight, 15, 42}, ErrNotInHunk},
		{"range runs off hunk end", Anchor{"src/net/fetch.go", SideRight, 15, 17}, ErrNotInHunk},
		{"left side of new file", Anchor{"docs/new.md", SideLeft, 1, 1}, ErrNotInHunk},
		{"right side of removed file", Anchor{"old/gone.go", SideRight, 1, 1}, ErrNotInHunk},
		{"binary file", Anchor{"assets/logo.png", SideRight, 1, 1}, ErrNotInHunk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := d.ValidateAnchor(tt.anchor)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateAnchor(%+v) = %v, want nil", tt.anchor, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateAnchor(%+v) = %v, want %v", tt.anchor, err, tt.wantErr)
			}
		})
	}
}

// commentable is an independent oracle for anchor validity: it walks every
// line of every fragment and records, per side, which hunk each line number
// belongs to. It deliberately uses the per-line ops instead of hunk headers.
func commentable(t testing.TB, diff string) map[string]map[Side]map[int]int {
	t.Helper()
	files, _, err := gitdiff.Parse(strings.NewReader(diff))
	if err != nil {
		t.Fatalf("oracle parse: %v", err)
	}
	out := map[string]map[Side]map[int]int{}
	for _, f := range files {
		path := f.NewName
		if f.IsDelete {
			path = f.OldName
		}
		sides := map[Side]map[int]int{SideRight: {}, SideLeft: {}}
		for hi, frag := range f.TextFragments {
			oldLine, newLine := int(frag.OldPosition), int(frag.NewPosition)
			for _, l := range frag.Lines {
				switch l.Op {
				case gitdiff.OpContext:
					sides[SideLeft][oldLine] = hi
					sides[SideRight][newLine] = hi
					oldLine++
					newLine++
				case gitdiff.OpDelete:
					sides[SideLeft][oldLine] = hi
					oldLine++
				case gitdiff.OpAdd:
					sides[SideRight][newLine] = hi
					newLine++
				}
			}
		}
		out[path] = sides
	}
	return out
}

// oracleValid reports whether every line in the anchor's range is commentable
// on its side and all of them belong to the same hunk.
func oracleValid(lines map[string]map[Side]map[int]int, a Anchor) bool {
	sides, ok := lines[a.Path]
	if !ok || a.StartLine < 1 || a.EndLine < a.StartLine {
		return false
	}
	byLine, ok := sides[a.Side]
	if !ok {
		return false
	}
	hunk, ok := byLine[a.StartLine]
	if !ok {
		return false
	}
	// Bound the walk; fuzzed ranges can be huge but valid ones are short.
	if a.EndLine-a.StartLine > len(byLine) {
		return false
	}
	// Count offsets rather than incrementing n, which would overflow at MaxInt.
	for off := 0; off <= a.EndLine-a.StartLine; off++ {
		if h, ok := byLine[a.StartLine+off]; !ok || h != hunk {
			return false
		}
	}
	return true
}

// FuzzValidateAnchor checks ValidateAnchor against the line-walking oracle on
// a fixed diff: an anchor is accepted exactly when the oracle says GitHub
// would accept it.
func FuzzValidateAnchor(f *testing.F) {
	d := mustParse(f, sampleDiff)
	oracle := commentable(f, sampleDiff)
	paths := d.Paths()

	f.Add(uint8(0), true, 13, 13)
	f.Add(uint8(0), true, 15, 42)
	f.Add(uint8(0), false, 41, 41)
	f.Add(uint8(1), false, 1, 1)
	f.Add(uint8(2), true, 1, 2)
	f.Add(uint8(4), true, 1, 1)
	f.Add(uint8(0), true, -1<<62, 1<<62)

	f.Fuzz(func(t *testing.T, pathIdx uint8, right bool, start, end int) {
		a := Anchor{Path: paths[int(pathIdx)%len(paths)], Side: SideLeft, StartLine: start, EndLine: end}
		if right {
			a.Side = SideRight
		}
		got := d.ValidateAnchor(a) == nil
		if want := oracleValid(oracle, a); got != want {
			t.Fatalf("ValidateAnchor(%+v) valid=%v, oracle says %v", a, got, want)
		}
	})
}

// FuzzParse feeds arbitrary input to Parse. It must never panic, and any diff
// it accepts must agree with the oracle on anchors at every hunk boundary.
func FuzzParse(f *testing.F) {
	f.Add(sampleDiff)
	f.Add("")
	f.Add("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")
	f.Add("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -9223372036854775807,1 +9223372036854775807,1 @@\n-a\n+b\n")

	f.Fuzz(func(t *testing.T, input string) {
		d, err := Parse(strings.NewReader(input))
		if err != nil {
			return
		}
		oracle := commentable(t, input)
		for _, file := range d.Files() {
			for _, h := range file.Hunks {
				for _, side := range []Side{SideRight, SideLeft} {
					start, count := h.NewStart, h.NewLines
					if side == SideLeft {
						start, count = h.OldStart, h.OldLines
					}
					for _, a := range []Anchor{
						{file.Path, side, start, start},
						{file.Path, side, start, start + count - 1},
						{file.Path, side, start + count - 1, start + count},
						{file.Path, side, start - 1, start},
					} {
						got := d.ValidateAnchor(a) == nil
						if want := oracleValid(oracle, a); got != want {
							t.Fatalf("ValidateAnchor(%+v) valid=%v, oracle says %v", a, got, want)
						}
					}
				}
			}
		}
	})
}
