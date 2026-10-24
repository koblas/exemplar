package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing/fstest"
)

// A config tree is exemplar-config's files held in memory: what a
// Snapshot was loaded from, stored by version (config_snapshots) so a
// runner reviews with the version its job was dispatched with.

// MaxTreeBytes bounds a config tree. exemplar-config is YAML; anything
// near this is a misconfigured checkout.
const MaxTreeBytes = 64 << 20

// ErrTreeTooLarge is returned by ReadTree past MaxTreeBytes.
var ErrTreeTooLarge = errors.New("config tree too large")

// ReadTree copies fsys into memory, leaving out dot files and directories
// (git-sync's .git).
func ReadTree(fsys fs.FS) (fstest.MapFS, error) {
	tree := fstest.MapFS{}
	total := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if total += len(data); total > MaxTreeBytes {
			return fmt.Errorf("%w: over %d bytes", ErrTreeTooLarge, MaxTreeBytes)
		}
		tree[p] = &fstest.MapFile{Data: data, Mode: 0o444}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("config: read tree: %w", err)
	}
	return tree, nil
}

// EncodeTree is tree as stored: a JSON object of path to contents.
func EncodeTree(tree fstest.MapFS) ([]byte, error) {
	files := make(map[string][]byte, len(tree))
	for p, f := range tree {
		files[p] = f.Data
	}
	b, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("config: encode tree: %w", err)
	}
	return b, nil
}

// DecodeTree reverses EncodeTree.
func DecodeTree(b []byte) (fstest.MapFS, error) {
	var files map[string][]byte
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, fmt.Errorf("config: decode tree: %w", err)
	}
	tree := make(fstest.MapFS, len(files))
	for p, data := range files {
		if !fs.ValidPath(p) {
			return nil, fmt.Errorf("config: decode tree: %w: %q", fs.ErrInvalid, p)
		}
		tree[p] = &fstest.MapFile{Data: data, Mode: 0o444}
	}
	return tree, nil
}

// TreeHash names a tree by its contents, for checkouts that are not
// git-sync worktrees.
func TreeHash(tree fstest.MapFS) string {
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(tree[p].Data))
		h.Write(tree[p].Data)
	}
	return "tree-" + hex.EncodeToString(h.Sum(nil))[:16]
}

var commitName = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// LoadDir reads the exemplar-config checkout at dir into a validated
// Snapshot. git-sync keeps dir as a link to a worktree named by its
// commit, so the link is resolved once: the version is that commit and
// the files are read from that worktree, never half from the next one.
// Any other directory is versioned by TreeHash.
func LoadDir(dir string) (*Snapshot, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("config: resolve %s: %w", dir, err)
	}
	tree, err := ReadTree(os.DirFS(resolved))
	if err != nil {
		return nil, err
	}
	version := filepath.Base(resolved)
	if !commitName.MatchString(version) {
		version = TreeHash(tree)
	}
	return LoadSnapshot(tree, version)
}
