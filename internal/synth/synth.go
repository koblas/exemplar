// Package synth builds small git repositories from inline files, for
// synthetic fixtures, adversarial cases and tests.
package synth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// Build creates a git repository in dir with a base commit holding base,
// and a head commit on branch "pr" that writes head over it and deletes
// deleted. File names come from fixtures, so they are confined to dir.
func Build(ctx context.Context, dir string, base, head map[string]string, deleted []string) (*Repo, error) {
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
