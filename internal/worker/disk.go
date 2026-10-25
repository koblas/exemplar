package worker

import (
	"context"
	"io/fs"
	"path/filepath"
)

// dirBytes is the size of the files under dir, the checkout's .git
// included; unreadable entries are skipped.
func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a vanished or unreadable entry adds nothing
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// DiskSizing sizes a runner's checkout volume (spec §2.4.2 C6).
type DiskSizing struct {
	// Known is the repo's recorded checkout size in bytes, 0 when none.
	Known func(ctx context.Context, repo string) int64
	// Min and Max bound a sized volume, in bytes.
	Min, Max int64
}

const gib = int64(1) << 30

// size is the volume for a repo whose checkouts reached known bytes:
// twice that plus 1 GiB, rounded up to a GiB and kept within Min and
// Max; 0 (the runner template's default) when nothing is recorded.
func (s DiskSizing) size(known int64) int64 {
	if known <= 0 {
		return 0
	}
	want := known*2 + gib
	want = (want + gib - 1) / gib * gib
	return min(max(want, s.Min), s.Max)
}
