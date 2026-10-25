package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/gitexec"
	"github.com/koblas/exemplar/internal/scope"
	"github.com/koblas/exemplar/internal/search/mirror"
)

// gitTimeout bounds each git command in a workspace.
const gitTimeout = 2 * time.Minute

var (
	// errInvalidGitArg is a commit, clone URL, revision or path that git
	// cannot be given safely.
	errInvalidGitArg  = errors.New("engine: invalid")
	errCommitNotFound = errors.New("not found after fetch")
	// ErrInvalidPatch is a patch ApplyPatch refuses.
	ErrInvalidPatch = errors.New("engine: invalid patch")
)

// Workspace is a worker checkout of one PR: a partial clone with the head
// commit checked out. The model's tools are confined to Dir.
type Workspace struct {
	Dir     string
	BaseSHA string
	HeadSHA string
	env     []string
}

// CheckoutOptions configures Checkout.
type CheckoutOptions struct {
	// CloneURL is the repository to fetch from: an https URL for GitHub, or
	// a local path for replay fixtures.
	CloneURL string
	BaseSHA  string
	HeadSHA  string
	// Token is a read-only installation token. It is passed to git through
	// environment config, never on the command line or in the URL.
	Token string
	// SparseDirs limits the working tree (cone mode). Nil checks out
	// everything. Files outside the cone are still readable from git.
	SparseDirs []string
}

// Checkout creates a workspace in dir, which must not exist or be empty.
func Checkout(ctx context.Context, dir string, opt CheckoutOptions) (*Workspace, error) {
	for _, sha := range []string{opt.BaseSHA, opt.HeadSHA} {
		if !isHexSHA(sha) {
			return nil, fmt.Errorf("%w commit %q", errInvalidGitArg, sha)
		}
	}
	if opt.CloneURL == "" || strings.HasPrefix(opt.CloneURL, "-") {
		return nil, fmt.Errorf("%w clone url %q", errInvalidGitArg, opt.CloneURL)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: create workspace: %w", err)
	}
	ws := &Workspace{Dir: dir, BaseSHA: opt.BaseSHA, HeadSHA: opt.HeadSHA, env: gitexec.CheckoutEnv(opt.Token)}

	if _, err := ws.git(ctx, "init", "-q"); err != nil {
		return nil, err
	}
	if _, err := ws.git(ctx, "remote", "add", "origin", opt.CloneURL); err != nil {
		return nil, err
	}
	// Fetch just the two commits, blobless. Some servers (local repos)
	// refuse fetching by SHA; fall back to fetching everything.
	if _, err := ws.git(ctx, "fetch", "-q", "--filter=blob:none", "--depth=1", "origin", opt.HeadSHA, opt.BaseSHA); err != nil {
		if _, err2 := ws.git(ctx, "fetch", "-q", "--filter=blob:none", "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/pull/*:refs/remotes/pull/*"); err2 != nil {
			return nil, fmt.Errorf("engine: fetch: %w (fallback: %w)", err, err2)
		}
	}
	for _, sha := range []string{opt.BaseSHA, opt.HeadSHA} {
		if _, err := ws.git(ctx, "cat-file", "-e", sha+"^{commit}"); err != nil {
			return nil, fmt.Errorf("engine: commit %s %w", sha, errCommitNotFound)
		}
	}
	if opt.SparseDirs != nil {
		args := append([]string{"sparse-checkout", "set", "--cone", "--"}, opt.SparseDirs...)
		if _, err := ws.git(ctx, args...); err != nil {
			return nil, err
		}
	}
	if _, err := ws.git(ctx, "checkout", "-q", "--detach", opt.HeadSHA); err != nil {
		return nil, err
	}
	return ws, nil
}

// Diff returns the PR diff (base..head) with rename detection.
func (ws *Workspace) Diff(ctx context.Context) (*scope.Diff, error) {
	out, err := ws.git(ctx, "diff", "--no-color", "--no-ext-diff", "-M", "--src-prefix=a/", "--dst-prefix=b/", ws.BaseSHA, ws.HeadSHA)
	if err != nil {
		return nil, err
	}
	d, err := scope.Parse(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("engine: parse diff: %w", err)
	}
	return d, nil
}

// Show returns a file's content at rev.
func (ws *Workspace) Show(ctx context.Context, rev, path string) ([]byte, error) {
	if strings.HasPrefix(path, "-") || strings.Contains(path, "\x00") {
		return nil, fmt.Errorf("%w path %q", errInvalidGitArg, path)
	}
	return ws.git(ctx, "show", rev+":"+path)
}

// Size returns a file's size in bytes at rev.
func (ws *Workspace) Size(ctx context.Context, rev, path string) (int64, error) {
	out, err := ws.git(ctx, "cat-file", "-s", rev+":"+path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("engine: parse size of %s: %w", path, err)
	}
	return n, nil
}

func (ws *Workspace) git(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := gitexec.Run(ctx, ws.Dir, ws.env, args...)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return out, nil
}

// LocalHistoryDepth is how far Deepen extends the checkout's history.
const LocalHistoryDepth = 200

// Deepen extends the history of the base commit by n commits (trees and
// commits only), for path_history without exemplar-search. It fails on a
// remote that refuses fetching by SHA; the log then reads what is there.
func (ws *Workspace) Deepen(ctx context.Context, n int) error {
	_, err := ws.git(ctx, "fetch", "-q", "--filter=blob:none", "--deepen="+strconv.Itoa(n), "origin", ws.BaseSHA)
	return err
}

// Log lists commits touching path as of rev, newest first, at most n
// (mirror.MaxLog), from the checkout's own history.
func (ws *Workspace) Log(ctx context.Context, rev, path string, n int) ([]mirror.Commit, error) {
	if !isHexSHA(rev) {
		return nil, fmt.Errorf("%w commit %q", errInvalidGitArg, rev)
	}
	if err := mirror.CleanPath(path); err != nil {
		return nil, fmt.Errorf("%w path %q: %w", errInvalidGitArg, path, err)
	}
	out, err := ws.git(ctx, mirror.LogArgs(rev, path, n)...)
	if err != nil {
		return nil, err
	}
	return mirror.ParseLog(out), nil
}

func isHexSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ChangedSince lists the paths whose content differs between rev (an
// earlier head of the PR) and the head: the interdiff's files.
// rev is fetched (trees only) if the checkout lacks it; after a force-push
// it may be gone, which is an error and means a full review.
func (ws *Workspace) ChangedSince(ctx context.Context, rev string) ([]string, error) {
	if !isHexSHA(rev) {
		return nil, fmt.Errorf("%w revision %q", errInvalidGitArg, rev)
	}
	if _, err := ws.git(ctx, "cat-file", "-e", rev+"^{commit}"); err != nil {
		if _, err := ws.git(ctx, "fetch", "-q", "--filter=blob:none", "--depth=1", "origin", rev); err != nil {
			return nil, fmt.Errorf("engine: fetch %s: %w", rev, err)
		}
	}
	// Names only, no rename detection: a rename is both paths changing.
	out, err := ws.git(ctx, "diff", "--name-only", "--no-renames", "-z", rev, ws.HeadSHA)
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range bytes.SplitSeq(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, nil
}

// MaxPatch bounds a patch ApplyPatch accepts.
const MaxPatch = 1 << 20

// ApplyPatch commits patch (a git diff against the checked-out base) on top of
// the head, and makes that commit the workspace's head: how a working-tree
// diff from `exemplar review` becomes a reviewable change. git apply refuses
// paths outside the tree and through symlinks; the commit runs no hooks.
func (ws *Workspace) ApplyPatch(ctx context.Context, patch []byte) error {
	if len(patch) == 0 || len(patch) > MaxPatch {
		return fmt.Errorf("%w: patch must be 1 to %d bytes", ErrInvalidPatch, MaxPatch)
	}
	f, err := os.CreateTemp(filepath.Dir(ws.Dir), "patch-*.diff")
	if err != nil {
		return fmt.Errorf("engine: create patch file: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(patch); err != nil {
		_ = f.Close()
		return fmt.Errorf("engine: write patch file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("engine: close patch file: %w", err)
	}
	if _, err := ws.git(ctx, "apply", "--index", "--whitespace=nowarn", f.Name()); err != nil {
		return fmt.Errorf("engine: the patch does not apply to %s: %w", ws.HeadSHA, err)
	}
	if out, err := ws.git(ctx, "diff", "--cached", "--name-only"); err != nil {
		return err
	} else if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("%w: the patch changes nothing", ErrInvalidPatch)
	}
	if _, err := ws.git(ctx, "commit", "-q", "--no-verify", "-m", "exemplar review"); err != nil {
		return err
	}
	out, err := ws.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	ws.HeadSHA = strings.TrimSpace(string(out))
	return nil
}
