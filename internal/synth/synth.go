// Package synth builds small git repositories from inline files, for
// synthetic fixtures, adversarial cases and tests.
package synth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// errEscapes reports a fixture path outside the repository.
var errEscapes = errors.New("escapes the repository")

// Repo is a built repository with a base and a head commit.
type Repo struct {
	Dir     string
	BaseSHA string
	HeadSHA string
}

// cache holds one built repository per distinct fixture once UseCache is
// called. Commits have fixed authors and dates, so a copy of a cached
// repository has the same SHAs as a fresh build.
var cache struct {
	mu    sync.Mutex
	dir   string
	repos map[string]*cacheEntry
}

type cacheEntry struct {
	once sync.Once
	repo *Repo
	err  error
}

// UseCache makes Build keep each distinct fixture under dir and copy it for
// later builds of the same fixture into an empty directory, instead of
// running git again. For tests, where the same fixture is built many times;
// RunCached sets it up from TestMain. An empty dir turns the cache off.
func UseCache(dir string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.dir, cache.repos = dir, map[string]*cacheEntry{}
}

// RunCached runs tests with the fixture cache in a temporary directory and
// removes it afterwards: func TestMain(m *testing.M) { os.Exit(synth.RunCached(m.Run)) }.
func RunCached(run func() int) int {
	dir, err := os.MkdirTemp("", "synth-cache-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "synth: fixture cache disabled:", err)
		return run()
	}
	UseCache(dir)
	defer func() {
		UseCache("")
		_ = os.RemoveAll(dir)
	}()
	return run()
}

// Build creates a git repository in dir with a base commit holding base,
// and a head commit on branch "pr" that writes head over it and deletes
// deleted. File names come from fixtures, so they are confined to dir.
func Build(ctx context.Context, dir string, base, head map[string]string, deleted []string) (*Repo, error) {
	if e, path := cached(base, head, deleted); e != nil && emptyDir(dir) {
		e.once.Do(func() { e.repo, e.err = build(ctx, path, base, head, deleted) })
		// A failed cached build (a cancelled context, say) falls back to
		// building here rather than failing every later test.
		if e.err == nil {
			if err := copyTree(e.repo.Dir, dir); err != nil {
				return nil, fmt.Errorf("synth: copy cached fixture: %w", err)
			}
			return &Repo{Dir: dir, BaseSHA: e.repo.BaseSHA, HeadSHA: e.repo.HeadSHA}, nil
		}
	}
	return build(ctx, dir, base, head, deleted)
}

// cached returns the cache entry for a fixture and where it is built, or
// nil when the cache is off.
func cached(base, head map[string]string, deleted []string) (*cacheEntry, string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.dir == "" {
		return nil, ""
	}
	key := fixtureKey(base, head, deleted)
	e, ok := cache.repos[key]
	if !ok {
		e = &cacheEntry{}
		cache.repos[key] = e
	}
	return e, filepath.Join(cache.dir, key)
}

// fixtureKey hashes a fixture's files and deletions.
func fixtureKey(base, head map[string]string, deleted []string) string {
	h := sha256.New()
	for _, files := range []map[string]string{base, head} {
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(h, "%d:%s%d:%s", len(n), n, len(files[n]), files[n])
		}
		h.Write([]byte{0})
	}
	for _, p := range deleted {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// emptyDir reports whether dir is missing or empty: a cached copy is only
// the same as a build when nothing else is there to be committed.
func emptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return errors.Is(err, fs.ErrNotExist) || (err == nil && len(entries) == 0)
}

// copyTree copies regular files, directories and symlinks from src to dst.
// Both sides go through os.Root, so no path can leave either tree.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	from, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = from.Close() }()
	to, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = to.Close() }()
	err = fs.WalkDir(from.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		switch {
		case d.IsDir():
			if err := to.MkdirAll(path, info.Mode().Perm()|0o700); err != nil {
				return fmt.Errorf("create %s: %w", path, err)
			}
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := from.Readlink(path)
			if err != nil {
				return fmt.Errorf("read link %s: %w", path, err)
			}
			if err := to.Symlink(link, path); err != nil {
				return fmt.Errorf("link %s: %w", path, err)
			}
		default:
			b, err := from.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			if err := to.WriteFile(path, b, info.Mode().Perm()); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}

func build(ctx context.Context, dir string, base, head map[string]string, deleted []string) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("synth: create %s: %w", dir, err)
	}
	r := &Repo{Dir: dir}
	if err := r.git(ctx, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	if err := writeFiles(dir, base); err != nil {
		return nil, err
	}
	if err := r.commit(ctx, "base"); err != nil {
		return nil, err
	}
	sha, err := r.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	r.BaseSHA = sha
	if err := r.git(ctx, "checkout", "-q", "-b", "pr"); err != nil {
		return nil, err
	}
	if err := writeFiles(dir, head); err != nil {
		return nil, err
	}
	for _, p := range deleted {
		full, err := confined(dir, p)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(full); err != nil {
			return nil, fmt.Errorf("synth: delete %s: %w", p, err)
		}
	}
	if err := r.commit(ctx, "head"); err != nil {
		return nil, err
	}
	if r.HeadSHA, err = r.out(ctx, "rev-parse", "HEAD"); err != nil {
		return nil, err
	}
	return r, nil
}

func writeFiles(dir string, files map[string]string) error {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		full, err := confined(dir, n)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("synth: create dir for %s: %w", n, err)
		}
		if err := os.WriteFile(full, []byte(files[n]), 0o600); err != nil {
			return fmt.Errorf("synth: write %s: %w", n, err)
		}
	}
	return nil
}

func confined(dir, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) ||
		clean == ".git" || strings.HasPrefix(clean, ".git"+string(filepath.Separator)) {
		return "", fmt.Errorf("synth: path %q %w", rel, errEscapes)
	}
	return filepath.Join(dir, clean), nil
}

func (r *Repo) commit(ctx context.Context, msg string) error {
	if err := r.git(ctx, "add", "-A"); err != nil {
		return err
	}
	return r.git(ctx, "commit", "-q", "--allow-empty", "-m", msg)
}

func (r *Repo) git(ctx context.Context, args ...string) error {
	_, err := r.out(ctx, args...)
	return err
}

func (r *Repo) out(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("synth: git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("synth: git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
