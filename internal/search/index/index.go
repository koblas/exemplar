// Package index keeps exemplar-search's zoekt index: per
// repo, shards of the default branch at the mirror's latest commit, with
// ctags symbols when universal-ctags is available.
//
// Indexing works in generations. Each build writes a new generation
// directory; it serves only once complete and is swapped in atomically,
// and the previous one is closed after its in-flight queries finish.
// Builds are incremental: a new generation starts as hard links to the
// serving one and zoekt's delta build re-indexes only changed files (zoekt
// writes new shards and tombstones through rename, so the linked files of
// the serving generation are never modified). A full rebuild runs once a
// day and whenever the delta chain exceeds 500 builds.
//
// Excluded paths are dropped from every result. zoekt's
// git indexer has no external path filter, so they may be present in the
// shards but are never returned.
package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-enry/go-enry/v2"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/gitindex"
	zindex "github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/search/mirror"
)

// Limits and schedules for queries and index rebuilds.
const (
	MaxResults   = 100
	QueryTimeout = 2 * time.Second
	MaxDeltas    = 500
	FullEvery    = 24 * time.Hour
	// LagLimit is how long the index may trail the mirror before search
	// answers in degraded mode.
	LagLimit = time.Hour
)

const completeFile = "GENERATION.json"

// Config configures an Index.
type Config struct {
	Root    string
	Mirrors *mirror.Manager
	// CTags is the universal-ctags binary; empty disables symbols.
	CTags string
	// RequireCTags fails builds whose symbol extraction fails.
	RequireCTags bool
	Logger       *slog.Logger
	Now          func() time.Time
}

// FindCTags returns the universal-ctags binary on PATH, or "".
func FindCTags() string {
	for _, name := range []string{"universal-ctags", "ctags"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "--version").Output() //nolint:noctx // one-off probe at startup
		if err == nil && strings.Contains(string(out), "Universal Ctags") {
			return p
		}
	}
	return ""
}

// Meta describes a complete generation.
type Meta struct {
	Gen      int       `json:"gen"`
	Commit   string    `json:"commit"`
	BuiltAt  time.Time `json:"built_at"`
	LastFull time.Time `json:"last_full"`
	Deltas   int       `json:"deltas"` // delta builds since the last full one
	Symbols  bool      `json:"symbols"`
}

type generation struct {
	meta  Meta
	dir   string
	s     zoekt.Streamer
	rules exclude.Rules
	refs  sync.WaitGroup
}

type repoState struct {
	cur *generation
	// behindSince is when the mirror first had a commit the index lacks.
	behindSince time.Time
	behindSHA   string
}

// Index owns the generations under Root.
type Index struct {
	cfg   Config
	mu    sync.Mutex
	repos map[string]*repoState
	// build serializes builds per Index; fetches already serialize per repo.
	build sync.Mutex
}

// New returns an Index and opens the newest complete generation of each
// repo found under Root, so a restart serves at once.
func New(cfg Config) (*Index, error) {
	if cfg.Root == "" || cfg.Mirrors == nil {
		return nil, errors.New("index: root and mirrors are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := os.MkdirAll(cfg.Root, 0o750); err != nil {
		return nil, fmt.Errorf("index: root: %w", err)
	}
	ix := &Index{cfg: cfg, repos: map[string]*repoState{}}
	owners, _ := os.ReadDir(cfg.Root)
	for _, o := range owners {
		names, _ := os.ReadDir(filepath.Join(cfg.Root, o.Name()))
		for _, n := range names {
			repo := o.Name() + "/" + n.Name()
			if g, err := ix.openLatest(context.Background(), repo); err == nil && g != nil {
				ix.repos[repo] = &repoState{cur: g}
			}
		}
	}
	return ix, nil
}

func (ix *Index) repoDir(repo string) (string, error) {
	d, err := ix.cfg.Mirrors.Dir(repo) // validates and normalizes the name
	if err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(filepath.Dir(filepath.Dir(d)), d)
	return filepath.Join(ix.cfg.Root, rel), nil
}

func genName(n int) string { return fmt.Sprintf("gen-%06d", n) }

// openLatest opens repo's newest complete generation and removes the rest.
func (ix *Index) openLatest(ctx context.Context, repo string) (*generation, error) {
	dir, err := ix.repoDir(repo)
	if err != nil {
		return nil, err
	}
	entries, _ := os.ReadDir(dir)
	var best *Meta
	for _, e := range entries {
		m, err := readMeta(filepath.Join(dir, e.Name()))
		if err == nil && (best == nil || m.Gen > best.Gen) {
			best = &m
		}
	}
	for _, e := range entries {
		if best == nil || e.Name() != genName(best.Gen) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name())) // incomplete or superseded
		}
	}
	if best == nil {
		return nil, nil
	}
	return ix.open(ctx, repo, filepath.Join(dir, genName(best.Gen)), *best)
}

func readMeta(dir string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(filepath.Join(dir, completeFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

func (ix *Index) open(ctx context.Context, repo, dir string, m Meta) (*generation, error) {
	s, err := search.NewDirectorySearcher(dir)
	if err != nil {
		return nil, fmt.Errorf("index: open %s: %w", dir, err)
	}
	rules, err := ix.cfg.Mirrors.Exclusions(ctx, repo, m.Commit)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("index: exclusions for %s@%s: %w", repo, m.Commit, err)
	}
	return &generation{meta: m, dir: dir, s: s, rules: rules}, nil
}

// Build indexes repo at the mirror's current snapshot, unless the serving
// generation already has it. It returns the serving generation's meta.
func (ix *Index) Build(ctx context.Context, repo string) (Meta, error) {
	repo = strings.ToLower(repo)
	ix.build.Lock()
	defer ix.build.Unlock()
	sha, err := ix.cfg.Mirrors.Snapshot(ctx, repo)
	if err != nil {
		return Meta{}, err
	}
	ix.mu.Lock()
	st := ix.repos[repo]
	if st == nil {
		st = &repoState{}
		ix.repos[repo] = st
	}
	cur := st.cur
	ix.mu.Unlock()
	if cur != nil && cur.meta.Commit == sha {
		return cur.meta, nil
	}

	now := ix.cfg.Now()
	m := Meta{Gen: 1, Commit: sha, BuiltAt: now, LastFull: now, Symbols: ix.cfg.CTags != ""}
	delta := false
	if cur != nil {
		m.Gen = cur.meta.Gen + 1
		delta = cur.meta.Deltas < MaxDeltas && now.Sub(cur.meta.LastFull) < FullEvery && cur.meta.Symbols == m.Symbols
		if delta {
			m.LastFull, m.Deltas = cur.meta.LastFull, cur.meta.Deltas+1
		}
	}
	rdir, err := ix.repoDir(repo)
	if err != nil {
		return Meta{}, err
	}
	if err := os.MkdirAll(rdir, 0o750); err != nil {
		return Meta{}, err
	}
	tmp, err := os.MkdirTemp(rdir, ".building-")
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // no-op once renamed
	if delta {
		if err := linkAll(cur.dir, tmp); err != nil {
			return Meta{}, err
		}
	}
	start := time.Now()
	if err := ix.index(ctx, repo, tmp, delta); err != nil {
		return Meta{}, err
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(tmp, completeFile), data, 0o640); err != nil {
		return Meta{}, err
	}
	final := filepath.Join(rdir, genName(m.Gen))
	if err := os.Rename(tmp, final); err != nil {
		return Meta{}, fmt.Errorf("index: install generation: %w", err)
	}
	g, err := ix.open(ctx, repo, final, m)
	if err != nil {
		_ = os.RemoveAll(final)
		return Meta{}, err
	}
	ix.mu.Lock()
	old := st.cur
	st.cur = g
	if st.behindSHA == sha || st.behindSHA == "" {
		st.behindSince, st.behindSHA = time.Time{}, ""
	}
	ix.mu.Unlock()
	if old != nil {
		go func() {
			old.refs.Wait() // queries that took the old generation
			old.s.Close()
			_ = os.RemoveAll(old.dir)
		}()
	}
	ix.cfg.Logger.Info("index generation serving", "repo", repo, "gen", m.Gen, "commit", sha,
		"delta", delta, "symbols", m.Symbols, "seconds", time.Since(start).Seconds())
	return m, nil
}

// index runs zoekt's git indexer over repo's mirror into dir.
func (ix *Index) index(ctx context.Context, repo, dir string, delta bool) error {
	mdir, err := ix.cfg.Mirrors.Dir(repo)
	if err != nil {
		return err
	}
	branch, err := defaultBranch(ctx, mdir)
	if err != nil {
		return err
	}
	opts := gitindex.Options{
		RepoDir:      mdir,
		BranchPrefix: "refs/heads/",
		Branches:     []string{branch},
		BuildOptions: zindex.Options{
			IndexDir:              dir,
			RepositoryDescription: zoekt.Repository{Name: repo},
			DisableCTags:          ix.cfg.CTags == "",
			CTagsPath:             ix.cfg.CTags,
			CTagsMustSucceed:      ix.cfg.RequireCTags,
			IsDelta:               delta,
		},
		DeltaShardNumberFallbackThreshold: MaxDeltas,
	}
	opts.BuildOptions.SetDefaults()
	if _, err := gitindex.IndexGitRepo(opts); err != nil {
		return fmt.Errorf("index: %s: %w", repo, err)
	}
	return nil
}

func defaultBranch(ctx context.Context, mirrorDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", mirrorDir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("index: default branch of %s: %w", mirrorDir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// linkAll hard-links every file of src into dst (same filesystem).
func linkAll(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == completeFile {
			continue
		}
		if err := os.Link(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("index: link %s: %w", e.Name(), err)
		}
	}
	return nil
}

// NoteMirror records that the mirror is at sha, for lag tracking.
func (ix *Index) NoteMirror(repo, sha string) {
	repo = strings.ToLower(repo)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := ix.repos[repo]
	if st == nil {
		st = &repoState{}
		ix.repos[repo] = st
	}
	if st.cur != nil && st.cur.meta.Commit == sha {
		st.behindSince, st.behindSHA = time.Time{}, ""
		return
	}
	if st.behindSince.IsZero() {
		st.behindSince = ix.cfg.Now()
	}
	st.behindSHA = sha
}

// Status is one repo's index state.
type Status struct {
	Repo     string    `json:"repo"`
	Commit   string    `json:"commit,omitempty"`
	Gen      int       `json:"gen,omitempty"`
	BuiltAt  time.Time `json:"built_at,omitzero"`
	Symbols  bool      `json:"symbols"`
	Degraded bool      `json:"degraded"`
	// Lag is how long the mirror has had a commit the index lacks.
	Lag time.Duration `json:"lag_ns"`
}

// Status reports repo's index.
func (ix *Index) Status(repo string) Status {
	repo = strings.ToLower(repo)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	s := Status{Repo: repo, Degraded: true}
	st := ix.repos[repo]
	if st == nil {
		return s
	}
	if !st.behindSince.IsZero() {
		s.Lag = ix.cfg.Now().Sub(st.behindSince)
	}
	if st.cur != nil {
		m := st.cur.meta
		s.Commit, s.Gen, s.BuiltAt, s.Symbols = m.Commit, m.Gen, m.BuiltAt, m.Symbols
		s.Degraded = s.Lag > LagLimit
	}
	return s
}

// Match is one search_text result line.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Result is a search answer.
type Result struct {
	Matches []Match `json:"matches"`
	// Commit is what the answer was computed from.
	Commit    string `json:"commit"`
	Truncated bool   `json:"truncated"`
	// Degraded is true when answered by git grep instead of the index.
	Degraded bool `json:"degraded"`
}

// TextQuery is search_text(pattern, path_filter?, lang?).
type TextQuery struct {
	Pattern string
	// PathFilter is a regular expression over file paths.
	PathFilter string
	Lang       string
}

var langRE = regexp.MustCompile(`^[A-Za-z0-9+#._ -]{1,40}$`)

// SearchText answers search_text from the serving generation, or with git
// grep at the mirror commit when the index is missing or too far behind.
func (ix *Index) SearchText(ctx context.Context, repo string, q TextQuery) (Result, error) {
	repo = strings.ToLower(repo)
	if q.Pattern == "" || len(q.Pattern) > mirror.MaxPattern || len(q.PathFilter) > mirror.MaxPattern {
		return Result{}, fmt.Errorf("%w: pattern must be 1-%d characters", mirror.ErrBadRequest, mirror.MaxPattern)
	}
	if q.Lang != "" && !langRE.MatchString(q.Lang) {
		return Result{}, fmt.Errorf("%w: bad language", mirror.ErrBadRequest)
	}
	content, err := syntax.Parse(q.Pattern, syntax.Perl)
	if err != nil {
		return Result{}, fmt.Errorf("%w: invalid pattern: %w", mirror.ErrBadRequest, err)
	}
	var file *syntax.Regexp
	if q.PathFilter != "" {
		if file, err = syntax.Parse(q.PathFilter, syntax.Perl); err != nil {
			return Result{}, fmt.Errorf("%w: invalid path filter: %w", mirror.ErrBadRequest, err)
		}
	}

	g := ix.acquire(repo)
	if g == nil {
		return ix.grep(ctx, repo, q)
	}
	defer g.refs.Done()
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	and := &query.And{Children: []query.Q{&query.Regexp{Regexp: content, Content: true, CaseSensitive: true}}}
	if file != nil {
		and.Children = append(and.Children, &query.Regexp{Regexp: file, FileName: true, CaseSensitive: true})
	}
	if q.Lang != "" {
		and.Children = append(and.Children, &query.Language{Language: q.Lang})
	}
	res, err := g.s.Search(ctx, query.Simplify(and), &zoekt.SearchOptions{
		MaxWallTime: QueryTimeout, ShardMaxMatchCount: 10 * MaxResults, TotalMaxMatchCount: 20 * MaxResults,
		MaxDocDisplayCount: MaxResults,
	})
	if err != nil {
		return Result{}, fmt.Errorf("index: search: %w", err)
	}
	out := Result{Commit: g.meta.Commit, Matches: []Match{}}
	files := res.Files
	slices.SortStableFunc(files, func(a, b zoekt.FileMatch) int { return strings.Compare(a.FileName, b.FileName) })
	for _, f := range files {
		if g.rules.Reason(f.FileName) != "" {
			continue
		}
		for _, lm := range f.LineMatches {
			if len(out.Matches) == MaxResults {
				out.Truncated = true
				return out, nil
			}
			out.Matches = append(out.Matches, Match{Path: f.FileName, Line: lm.LineNumber, Text: strings.TrimRight(string(lm.Line), "\r\n")})
		}
	}
	out.Truncated = out.Truncated || res.FilesSkipped > 0 || res.ShardsSkipped > 0
	return out, nil
}

// acquire returns the serving generation with a reference held, or nil
// when the repo must be answered in degraded mode.
func (ix *Index) acquire(repo string) *generation {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := ix.repos[repo]
	if st == nil || st.cur == nil {
		return nil
	}
	if !st.behindSince.IsZero() && ix.cfg.Now().Sub(st.behindSince) > LagLimit {
		return nil
	}
	st.cur.refs.Add(1)
	return st.cur
}

// grep is degraded-mode search_text: git grep at the mirror commit.
// PathFilter and Lang are applied to its results, Lang by file name
// (enry, as zoekt labels documents) since there is no index to ask.
func (ix *Index) grep(ctx context.Context, repo string, q TextQuery) (Result, error) {
	sha, err := ix.cfg.Mirrors.Snapshot(ctx, repo)
	if err != nil {
		return Result{}, err
	}
	var file *regexp.Regexp
	if q.PathFilter != "" {
		if file, err = regexp.Compile(q.PathFilter); err != nil {
			return Result{}, fmt.Errorf("%w: invalid path filter", mirror.ErrBadRequest)
		}
	}
	rules, err := ix.cfg.Mirrors.Exclusions(ctx, repo, sha)
	if err != nil {
		return Result{}, err
	}
	// Ask for more than we return: some lines will be filtered out.
	raw, truncated, err := ix.cfg.Mirrors.Grep(ctx, repo, sha, q.Pattern, "", 5*MaxResults)
	if err != nil {
		return Result{}, err
	}
	out := Result{Commit: sha, Matches: []Match{}, Degraded: true, Truncated: truncated}
	for _, m := range raw {
		if rules.Reason(m.Path) != "" || (file != nil && !file.MatchString(m.Path)) ||
			(q.Lang != "" && !strings.EqualFold(enry.GetLanguage(path.Base(m.Path), nil), q.Lang)) {
			continue
		}
		if len(out.Matches) == MaxResults {
			out.Truncated = true
			break
		}
		out.Matches = append(out.Matches, Match(m))
	}
	return out, nil
}

// Close closes every generation.
func (ix *Index) Close() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, st := range ix.repos {
		if st.cur != nil {
			st.cur.refs.Wait()
			st.cur.s.Close()
		}
	}
}
