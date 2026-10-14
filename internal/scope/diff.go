// Package scope parses pull request diffs, indexes their hunks, validates
// comment anchors against them, and matches rule file patterns.
//
// Everything here is deterministic and model-free: anchors proposed by a model
// are only ever accepted if GitHub would accept them as review comment
// positions (spec §6.3, §6.9).
package scope

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// Status is how a file changed in the diff.
type Status string

// File statuses, named after the GitHub pull request files API.
const (
	StatusAdded    Status = "added"
	StatusModified Status = "modified"
	StatusRemoved  Status = "removed"
	StatusRenamed  Status = "renamed"
	StatusCopied   Status = "copied"
)

// Hunk is one diff fragment. Lines on each side are contiguous: the new side
// covers NewStart..NewStart+NewLines-1 and the old side OldStart..OldStart+OldLines-1.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	// Added and Deleted count the hunk's + and - lines (context excluded).
	Added, Deleted int
	// Text is the fragment in unified diff form, including its @@ header.
	Text string
}

// File is one changed file in a diff.
type File struct {
	// Path is the file's path after the change, or before it for removed files.
	Path string
	// OldPath is the path before the change; it equals Path unless the file
	// was renamed or copied.
	OldPath string
	Status  Status
	Binary  bool
	Hunks   []Hunk
}

// Diff is a parsed diff with files indexed by path.
type Diff struct {
	files  []File
	byPath map[string]int
}

// Errors for diffs that parse but cannot be anchored against unambiguously.
var (
	ErrDuplicatePath = errors.New("duplicate path in diff")
	ErrMalformedHunk = errors.New("malformed hunk")
)

// Parse reads a unified git diff, such as `git diff` output or a pull request
// fetched with the application/vnd.github.diff media type.
func Parse(r io.Reader) (*Diff, error) {
	parsed, _, err := gitdiff.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}

	d := &Diff{
		files:  make([]File, 0, len(parsed)),
		byPath: make(map[string]int, len(parsed)),
	}
	for _, pf := range parsed {
		f := convertFile(pf)
		if f.Path == "" {
			return nil, errors.New("parse diff: file with no path")
		}
		if _, dup := d.byPath[f.Path]; dup {
			return nil, fmt.Errorf("parse diff: %w: %s", ErrDuplicatePath, f.Path)
		}
		if err := checkHunks(f.Hunks); err != nil {
			return nil, fmt.Errorf("parse diff: %s: %w", f.Path, err)
		}
		d.byPath[f.Path] = len(d.files)
		d.files = append(d.files, f)
	}
	return d, nil
}

func convertFile(pf *gitdiff.File) File {
	f := File{
		Path:    pf.NewName,
		OldPath: pf.OldName,
		Binary:  pf.IsBinary,
	}
	switch {
	case pf.IsNew:
		f.Status = StatusAdded
		f.OldPath = f.Path
	case pf.IsDelete:
		f.Status = StatusRemoved
		f.Path = pf.OldName
	case pf.IsRename:
		f.Status = StatusRenamed
	case pf.IsCopy:
		f.Status = StatusCopied
	default:
		f.Status = StatusModified
	}
	if f.OldPath == "" {
		f.OldPath = f.Path
	}

	f.Hunks = make([]Hunk, 0, len(pf.TextFragments))
	for _, tf := range pf.TextFragments {
		f.Hunks = append(f.Hunks, Hunk{
			OldStart: int(tf.OldPosition),
			OldLines: int(tf.OldLines),
			NewStart: int(tf.NewPosition),
			NewLines: int(tf.NewLines),
			Added:    int(tf.LinesAdded),
			Deleted:  int(tf.LinesDeleted),
			Text:     tf.String(),
		})
	}
	return f
}

// checkHunks rejects hunks git would never produce: lines numbered below 1,
// or hunks that overlap or run backwards on either side. Either would let one
// line number belong to two hunks, making same-hunk checks ambiguous.
func checkHunks(hunks []Hunk) error {
	prevOldEnd, prevNewEnd := 0, 0 // one past the last line of the previous hunk
	for i, h := range hunks {
		if h.OldStart < 0 || h.OldLines < 0 || h.NewStart < 0 || h.NewLines < 0 ||
			(h.OldLines > 0 && h.OldStart < 1) || (h.NewLines > 0 && h.NewStart < 1) ||
			h.OldLines > math.MaxInt-h.OldStart || h.NewLines > math.MaxInt-h.NewStart {
			return fmt.Errorf("%w: hunk %d has invalid line numbers", ErrMalformedHunk, i)
		}
		if h.OldStart < prevOldEnd || h.NewStart < prevNewEnd {
			return fmt.Errorf("%w: hunk %d overlaps or precedes hunk %d", ErrMalformedHunk, i, i-1)
		}
		prevOldEnd, prevNewEnd = h.OldStart+h.OldLines, h.NewStart+h.NewLines
	}
	return nil
}

// ChangedLines is the diff's added plus deleted lines; binary files count 0.
func (d *Diff) ChangedLines() int {
	n := 0
	for _, f := range d.files {
		for _, h := range f.Hunks {
			n += h.Added + h.Deleted
		}
	}
	return n
}

// Files returns the changed files in diff order. The slice must not be modified.
func (d *Diff) Files() []File {
	return d.files
}

// File returns the changed file at path.
func (d *Diff) File(path string) (File, bool) {
	i, ok := d.byPath[path]
	if !ok {
		return File{}, false
	}
	return d.files[i], true
}

// Paths returns the paths of all changed files in diff order.
func (d *Diff) Paths() []string {
	paths := make([]string, len(d.files))
	for i, f := range d.files {
		paths[i] = f.Path
	}
	return paths
}
