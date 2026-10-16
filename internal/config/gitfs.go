package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing/fstest"
	"time"
)

// gitTimeout bounds each git invocation that builds a GitFS.
const gitTimeout = 30 * time.Second

var (
	// ErrInvalidRevision is returned by GitFS for an empty or option-like revision.
	ErrInvalidRevision = errors.New("gitfs: invalid revision")
	errLsTreeLine      = errors.New("gitfs: unexpected ls-tree line")
)

// GitFS returns the files under the given path prefixes as they are at rev
// in the git repository at dir. Reading config through it, with rev set to
// the PR's base commit, is what keeps PR heads from changing their own
// review configuration.
func GitFS(ctx context.Context, dir, rev string, prefixes ...string) (fstest.MapFS, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return nil, fmt.Errorf("%w %q", ErrInvalidRevision, rev)
	}
	args := make([]string, 0, 8+len(prefixes))
	args = append(args, "-C", dir, "ls-tree", "-r", "-z", "--full-tree", rev, "--")
	args = append(args, prefixes...)
	out, err := runGit(ctx, args...)
	if err != nil {
		return nil, err
	}
	fsys := fstest.MapFS{}
	for entry := range bytes.SplitSeq(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		// "<mode> <type> <object>\t<path>"
		meta, p, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("%w %q", errLsTreeLine, entry)
		}
		fields := strings.Fields(string(meta))
		if len(fields) != 3 || fields[1] != "blob" {
			continue // submodules and symlinks are not config
		}
		if fields[0] == "120000" {
			continue
		}
		data, err := runGit(ctx, "-C", dir, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, err
		}
		fsys[string(p)] = &fstest.MapFile{Data: data, Mode: 0o444}
	}
	return fsys, nil
}

func runGit(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("git %s: %w: %s", args[2], err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("git %s: %w", args[2], err)
	}
	return out, nil
}
