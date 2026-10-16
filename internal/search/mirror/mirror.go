// Package mirror keeps exemplar-search's git mirrors: one
// full-object bare mirror per repo in scope (each opted-in repo plus its
// context.extra_repos), fetched on relayed default-branch pushes (at most
// once per 30 s per repo) and every 5 minutes as a backstop. It is the
// system's only git mirror. It answers read_file at a commit and git log
// for path_history, and never runs a build or any code from the repos.
//
// Mirrors live under <root>/<owner>/<name>. Each records the repo's
// visibility (public, private, internal) from GitHub so the API can keep
// context within one visibility tier.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/koblas/exemplar/internal/gitexec"
)

// Limits.
const (
	MaxReadLines = 400 // read_file
	MaxLog       = 20  // path_history
	maxSubject   = 200
	maxFileBytes = 5 << 20
	readTimeout  = 10 * time.Second
	fetchTimeout = 15 * time.Minute // a first clone of a monorepo is slow
)

// Errors callers map to responses.
var (
	ErrNotMirrored = errors.New("mirror: repo is not mirrored")
	ErrNoCommit    = errors.New("mirror: commit not in mirror")
	ErrNoFile      = errors.New("mirror: file not found at commit")
	ErrBadRequest  = errors.New("mirror: bad request")
)

// Credentials returns a read-only token for fetching repo, plus the
// repo's visibility as GitHub reports it ("public", "private" or
// "internal"). The CLI binds this to the review app's context token.
type Credentials func(ctx context.Context, repo string) (token, visibility string, err error)

// Config configures a Manager.
type Config struct {
	Root string
	// URL returns the clone URL for repo (default https://github.com/<repo>.git).
	URL func(repo string) string
	// Credentials, when nil, fetches without a token and records
	// visibility as "private" (local and test use).
	Credentials Credentials
}

// Manager owns the mirrors under Root.
type Manager struct {
	cfg Config
	mu  sync.Mutex
	// locks serializes fetches per repo; reads never wait on them.
	locks map[string]*sync.Mutex
}

// New returns a Manager, creating Root if needed.
func New(cfg Config) (*Manager, error) {
	if cfg.Root == "" {
		return nil, errors.New("mirror: root is required")
	}
	if err := os.MkdirAll(cfg.Root, 0o750); err != nil {
		return nil, fmt.Errorf("mirror: root: %w", err)
	}
	if cfg.URL == nil {
		cfg.URL = func(repo string) string { return "https://github.com/" + repo + ".git" }
	}
	return &Manager{cfg: cfg, locks: map[string]*sync.Mutex{}}, nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Dir returns repo's mirror directory, refusing names that could escape Root.
func (m *Manager) Dir(repo string) (string, error) {
	// GitHub names are case-insensitive; mirrors use one spelling.
	owner, name, ok := strings.Cut(strings.ToLower(repo), "/")
	for _, s := range []string{owner, name} {
		if !ok || !nameRE.MatchString(s) || s == "." || s == ".." {
			return "", fmt.Errorf("%w: repo %q must be owner/name", ErrBadRequest, repo)
		}
	}
	return filepath.Join(m.cfg.Root, owner, name), nil
}

func (m *Manager) lock(repo string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo = strings.ToLower(repo)
	l, ok := m.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		m.locks[repo] = l
	}
	return l
}

// Fetch creates repo's mirror if needed and fetches its branches and
// tags, then points HEAD at the remote's default branch. It returns the
// new snapshot SHA.
func (m *Manager) Fetch(ctx context.Context, repo string) (string, error) {
	dir, err := m.Dir(repo)
	if err != nil {
		return "", err
	}
	l := m.lock(repo)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	token, visibility := "", "private"
	if m.cfg.Credentials != nil {
		if token, visibility, err = m.cfg.Credentials(ctx, repo); err != nil {
			return "", fmt.Errorf("mirror: credentials for %s: %w", repo, err)
		}
	}
	env := gitexec.Env(token)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return "", err
		}
		// Build in a temp dir and rename, so a half-made mirror never serves.
		tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+"-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(tmp) }() // after the rename this is a no-op
		if _, err := gitexec.Run(ctx, tmp, env, "init", "-q", "--bare"); err != nil {
			return "", err
		}
		if _, err := gitexec.Run(ctx, tmp, env, "remote", "add", "origin", m.cfg.URL(repo)); err != nil {
			return "", err
		}
		if err := fetch(ctx, tmp, env); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, dir); err != nil {
			return "", fmt.Errorf("mirror: install %s: %w", repo, err)
		}
	} else if err := fetch(ctx, dir, env); err != nil {
		return "", err
	}
	if _, err := gitexec.Run(ctx, dir, env, "config", "exemplar.visibility", visibility); err != nil {
		return "", err
	}
	return m.Snapshot(ctx, repo)
}

// fetch updates heads and tags (pruning deleted ones) and HEAD.
func fetch(ctx context.Context, dir string, env []string) error {
	if _, err := gitexec.Run(ctx, dir, env, "fetch", "-q", "--prune", "origin",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return fmt.Errorf("mirror: fetch: %w", err)
	}
	out, err := gitexec.Run(ctx, dir, env, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return fmt.Errorf("mirror: default branch: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: "); ok {
			ref, _, _ = strings.Cut(ref, "\t")
			if strings.HasPrefix(ref, "refs/heads/") {
				_, err := gitexec.Run(ctx, dir, env, "symbolic-ref", "HEAD", ref)
				return err
			}
		}
	}
	return errors.New("mirror: remote reports no default branch")
}

// Snapshot returns the default branch's commit in repo's mirror.
func (m *Manager) Snapshot(ctx context.Context, repo string) (string, error) {
	dir, err := m.mirrored(repo)
	if err != nil {
		return "", err
	}
	out, err := m.git(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s has no default branch yet", ErrNotMirrored, repo)
	}
	return strings.TrimSpace(string(out)), nil
}

// Visibility returns the visibility recorded at the last fetch.
func (m *Manager) Visibility(ctx context.Context, repo string) (string, error) {
	dir, err := m.mirrored(repo)
	if err != nil {
		return "", err
	}
	out, err := m.git(ctx, dir, "config", "--get", "exemplar.visibility")
	if err != nil {
		return "", fmt.Errorf("%w: %s has no recorded visibility", ErrNotMirrored, repo)
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *Manager) mirrored(repo string) (string, error) {
	dir, err := m.Dir(repo)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotMirrored, repo)
	}
	return dir, nil
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return gitexec.Run(ctx, dir, m.env(), args...)
}

// env is the environment for reads: no token and literal pathspecs (Grep
// uses glob pathspecs for its filter instead).
func (m *Manager) env() []string {
	return append(gitexec.Env(""), "GIT_LITERAL_PATHSPECS=1")
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// CleanPath accepts clean repo-relative paths only.
func CleanPath(p string) error {
	if p == "" || strings.ContainsAny(p, "\x00\n:") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") ||
		path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("%w: %q is not a clean repo-relative path", ErrBadRequest, p)
	}
	return nil
}

func (m *Manager) commit(ctx context.Context, dir, commit string) (string, error) {
	if !shaRE.MatchString(commit) {
		return "", fmt.Errorf("%w: commit must be a SHA", ErrBadRequest)
	}
	out, err := m.git(ctx, dir, "rev-parse", "--verify", "--quiet", commit+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNoCommit, commit)
	}
	return strings.TrimSpace(string(out)), nil
}

// File is a read_file answer.
type File struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Start  int    `json:"start"`
	End    int    `json:"end"`   // last line returned
	Total  int    `json:"total"` // lines in the file
	Text   string `json:"text"`
}

// ReadFile returns lines start..end (1-based, inclusive, at most 400) of
// p at commit.
func (m *Manager) ReadFile(ctx context.Context, repo, commit, p string, start, end int) (File, error) {
	dir, err := m.mirrored(repo)
	if err != nil {
		return File{}, err
	}
	if err := CleanPath(p); err != nil {
		return File{}, err
	}
	if start < 1 || end < start {
		return File{}, fmt.Errorf("%w: bad line range %d-%d", ErrBadRequest, start, end)
	}
	end = min(end, start+MaxReadLines-1)
	sha, err := m.commit(ctx, dir, commit)
	if err != nil {
		return File{}, err
	}
	size, err := m.git(ctx, dir, "cat-file", "-s", sha+":"+p)
	if err != nil {
		return File{}, fmt.Errorf("%w: %s", ErrNoFile, p)
	}
	if n, _ := strconv.Atoi(strings.TrimSpace(string(size))); n > maxFileBytes {
		return File{}, fmt.Errorf("%w: %s is larger than %d bytes", ErrBadRequest, p, maxFileBytes)
	}
	out, err := m.git(ctx, dir, "cat-file", "blob", sha+":"+p)
	if err != nil {
		return File{}, fmt.Errorf("%w: %s is not a file", ErrNoFile, p)
	}
	lines := strings.SplitAfter(string(out), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	f := File{Commit: sha, Path: p, Start: start, Total: len(lines)}
	if start > len(lines) {
		f.End = start - 1
		return f, nil
	}
	f.End = min(end, len(lines))
	f.Text = strings.Join(lines[start-1:f.End], "")
	return f, nil
}

// Commit is one git log entry. No author identity is returned.
type Commit struct {
	SHA     string    `json:"sha"`
	At      time.Time `json:"at"`
	Subject string    `json:"subject"`
}

// Log returns up to n (≤ 20) non-merge commits touching p, newest first,
// as of commit.
func (m *Manager) Log(ctx context.Context, repo, commit, p string, n int) ([]Commit, error) {
	dir, err := m.mirrored(repo)
	if err != nil {
		return nil, err
	}
	if err := CleanPath(p); err != nil {
		return nil, err
	}
	n = min(max(n, 1), MaxLog)
	sha, err := m.commit(ctx, dir, commit)
	if err != nil {
		return nil, err
	}
	out, err := m.git(ctx, dir, "log", "--no-merges", "-n"+strconv.Itoa(n), "--format=%H%x1f%cI%x1f%s", sha, "--", p)
	if err != nil {
		return nil, err
	}
	commits := []Commit{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		at, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue
		}
		subject := parts[2]
		if r := []rune(subject); len(r) > maxSubject {
			subject = string(r[:maxSubject]) + "…"
		}
		commits = append(commits, Commit{SHA: parts[0], At: at, Subject: subject})
	}
	return commits, nil
}
