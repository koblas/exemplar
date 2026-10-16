package index

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/query"

	"github.com/koblas/exemplar/internal/exclude"
	"github.com/koblas/exemplar/internal/search/mirror"
)

// Limits.
const (
	MaxReferences         = 50
	MaxReferencesDegraded = 25 // tighter cap for git grep
	// RequireDefinedIn: a name with more repo-wide hits needs defined_in.
	RequireDefinedIn = 500
	MaxDependentPkgs = 200
	MaxDependentsDep = 2
	// RefContext is the lines of context on each side of a reference.
	RefContext = 2
	// maxImportCandidates bounds the candidate import lines per package.
	maxImportCandidates = 2000
	// pathChunk bounds the files named in one scoped query.
	pathChunk = 100
)

// view is what one answer is computed from: the serving generation (nil
// in degraded mode), its commit and the repo's exclusions.
type view struct {
	g      *generation
	commit string
	rules  exclude.Rules
}

func (ix *Index) view(ctx context.Context, repo string) (view, func(), error) {
	if g := ix.acquire(repo); g != nil {
		return view{g: g, commit: g.meta.Commit, rules: g.rules}, g.refs.Done, nil
	}
	sha, err := ix.cfg.Mirrors.Snapshot(ctx, repo)
	if err != nil {
		return view{}, func() {}, err
	}
	rules, err := ix.cfg.Mirrors.Exclusions(ctx, repo, sha)
	if err != nil {
		return view{}, func() {}, err
	}
	return view{commit: sha, rules: rules}, func() {}, nil
}

// lineQuery is a line search: a whole-word literal (word) or a coarse
// pattern valid as RE2 and POSIX ERE. files, an RE2 over paths, and scope
// (files and directories, a directory meaning its subtree) narrow it.
type lineQuery struct {
	pattern string
	word    bool
	files   string
	scope   []string
	limit   int
}

// lines runs q against the index, or git grep in degraded mode. Excluded
// paths are dropped. It reports whether the limit cut the answer.
func (ix *Index) lines(ctx context.Context, repo string, v view, q lineQuery) ([]Match, bool, error) {
	var fileRE *regexp.Regexp
	if q.files != "" {
		fileRE = regexp.MustCompile(q.files)
	}
	keep := func(p string) bool { return v.rules.Reason(p) == "" && (fileRE == nil || fileRE.MatchString(p)) }
	chunks := [][]string{nil}
	if len(q.scope) > 0 {
		chunks = slices.Collect(slices.Chunk(q.scope, pathChunk))
	}
	var out []Match
	truncated := false
	for _, chunk := range chunks {
		var (
			ms  []Match
			cut bool
			err error
		)
		if v.g != nil {
			ms, cut, err = ix.indexLines(ctx, v.g, q, chunk)
		} else {
			ms, cut, err = ix.grepLines(ctx, repo, v.commit, q, chunk)
		}
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || cut
		for _, m := range ms {
			if keep(m.Path) {
				out = append(out, m)
			}
		}
		if len(out) >= q.limit {
			return out[:q.limit], true, nil
		}
	}
	return out, truncated, nil
}

func (ix *Index) indexLines(ctx context.Context, g *generation, q lineQuery, scope []string) ([]Match, bool, error) {
	pat := q.pattern
	if q.word {
		pat = `\b` + regexp.QuoteMeta(q.pattern) + `\b`
	}
	content, err := syntax.Parse(pat, syntax.Perl)
	if err != nil {
		return nil, false, fmt.Errorf("index: pattern: %w", err)
	}
	and := &query.And{Children: []query.Q{&query.Regexp{Regexp: content, Content: true, CaseSensitive: true}}}
	if len(scope) > 0 {
		var alts []string
		for _, s := range scope {
			alts = append(alts, regexp.QuoteMeta(strings.TrimSuffix(s, "/"))+`(/.*)?`)
		}
		file, err := syntax.Parse(`^(?:`+strings.Join(alts, "|")+`)$`, syntax.Perl)
		if err != nil {
			return nil, false, fmt.Errorf("index: scope: %w", err)
		}
		and.Children = append(and.Children, &query.Regexp{Regexp: file, FileName: true, CaseSensitive: true})
	}
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	res, err := g.s.Search(ctx, query.Simplify(and), &zoekt.SearchOptions{
		MaxWallTime: QueryTimeout, ShardMaxMatchCount: 5 * q.limit, TotalMaxMatchCount: 10 * q.limit, MaxDocDisplayCount: q.limit,
	})
	if err != nil {
		return nil, false, fmt.Errorf("index: search: %w", err)
	}
	var out []Match
	for _, f := range res.Files {
		for _, lm := range f.LineMatches {
			out = append(out, Match{Path: f.FileName, Line: lm.LineNumber, Text: strings.TrimRight(string(lm.Line), "\r\n")})
		}
	}
	return out, res.FilesSkipped > 0 || res.ShardsSkipped > 0, nil
}

func (ix *Index) grepLines(ctx context.Context, repo, commit string, q lineQuery, scope []string) ([]Match, bool, error) {
	var (
		raw []mirror.GrepMatch
		cut bool
		err error
	)
	switch {
	case len(scope) > 0:
		raw, cut, err = ix.cfg.Mirrors.GrepIn(ctx, repo, commit, q.pattern, q.word, scope, 2*q.limit)
	case q.word:
		raw, cut, err = ix.cfg.Mirrors.GrepWord(ctx, repo, commit, q.pattern, 2*q.limit)
	default:
		raw, cut, err = ix.cfg.Mirrors.Grep(ctx, repo, commit, q.pattern, "", 2*q.limit)
	}
	out := make([]Match, len(raw))
	for i, m := range raw {
		out[i] = Match(m)
	}
	return out, cut, err
}

// DependentsResult is dependents(path, depth).
type DependentsResult struct {
	// Files import the package, directly or (depth 2) through an importer.
	Files []string `json:"files"`
	// Packages are the importing packages' directories.
	Packages []string `json:"packages"`
	Commit   string   `json:"commit"`
	Degraded bool     `json:"degraded"`
	// Proximity is set when the language has no import rules: there are
	// no dependents, and callers fall back to directory proximity.
	Proximity bool `json:"proximity"`
	Truncated bool `json:"truncated"`
}

// Dependents answers dependents(path, depth?): files whose
// imports name path's package, repeated for importing packages up to depth
// 2, at most 200 packages.
func (ix *Index) Dependents(ctx context.Context, repo, file string, depth int, includeRoots []string) (DependentsResult, error) {
	repo = strings.ToLower(repo)
	if err := mirror.CleanPath(file); err != nil {
		return DependentsResult{}, err
	}
	depth = min(max(depth, 1), MaxDependentsDep)
	v, done, err := ix.view(ctx, repo)
	defer done()
	if err != nil {
		return DependentsResult{}, err
	}
	return ix.dependents(ctx, repo, v, file, depth, includeRoots)
}

func (ix *Index) dependents(ctx context.Context, repo string, v view, file string, depth int, includeRoots []string) (DependentsResult, error) {
	res := DependentsResult{Files: []string{}, Packages: []string{}, Commit: v.commit, Degraded: v.g == nil}
	seenPkg := map[string]bool{path.Dir(file): true}
	seenFile := map[string]bool{}
	frontier := []string{file}
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		var next []string
		for _, f := range frontier {
			spec, ok := ix.packageSpec(ctx, repo, v.commit, f, includeRoots)
			if !ok {
				if d == 1 {
					res.Proximity = true
				}
				continue
			}
			cands, cut, err := ix.lines(ctx, repo, v, lineQuery{pattern: spec.coarse, files: spec.files, limit: maxImportCandidates})
			if err != nil {
				return res, err
			}
			res.Truncated = res.Truncated || cut
			for _, m := range cands {
				if seenFile[m.Path] || m.Path == f || !spec.imports(m.Path, m.Text) {
					continue
				}
				seenFile[m.Path] = true
				res.Files = append(res.Files, m.Path)
				if pkg := path.Dir(m.Path); !seenPkg[pkg] {
					if len(res.Packages) == MaxDependentPkgs {
						res.Truncated = true
						continue
					}
					seenPkg[pkg] = true
					res.Packages = append(res.Packages, pkg)
					next = append(next, m.Path)
				}
			}
		}
		frontier = next
	}
	slices.Sort(res.Files)
	slices.Sort(res.Packages)
	return res, nil
}

// RefQuery is find_references(name, defined_in).
type RefQuery struct {
	Name string
	// DefinedIn is the file defining Name. It may be left out only for
	// names with at most RequireDefinedIn hits repo-wide.
	DefinedIn    string
	IncludeRoots []string
	// Depth is the dependents depth searched (the repo's
	// context.dependents_depth): 1 by default, at most 2.
	Depth int
}

// Reference is one reference site with context.
type Reference struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// Context is lines Line-RefContext..Line+RefContext, from ContextStart.
	Context      string `json:"context"`
	ContextStart int    `json:"context_start"`
}

// RefResult is a find_references answer.
type RefResult struct {
	References []Reference `json:"references"`
	Commit     string      `json:"commit"`
	Degraded   bool        `json:"degraded"`
	Truncated  bool        `json:"truncated"`
	// Packages is how many importing packages were searched besides the
	// defining one; Proximity is set when there were no import rules and
	// the defining directory's subtree was searched instead.
	Packages  int  `json:"packages"`
	Proximity bool `json:"proximity"`
}

// FindReferences answers find_references: a word-boundary
// search for Name restricted to the defining package's directory plus the
// files of dependents(defined_in), ranked by proximity to defined_in, at
// most 50 sites (25 in degraded mode) with two lines of context.
func (ix *Index) FindReferences(ctx context.Context, repo string, q RefQuery) (RefResult, error) {
	repo = strings.ToLower(repo)
	base, _, err := splitName(q.Name)
	if err != nil {
		return RefResult{}, err
	}
	if q.DefinedIn != "" {
		if err := mirror.CleanPath(q.DefinedIn); err != nil {
			return RefResult{}, err
		}
	}
	v, done, err := ix.view(ctx, repo)
	defer done()
	if err != nil {
		return RefResult{}, err
	}
	res := RefResult{References: []Reference{}, Commit: v.commit, Degraded: v.g == nil}
	limit := MaxReferences
	if res.Degraded {
		limit = MaxReferencesDegraded
	}

	var hits []Match
	if q.DefinedIn == "" {
		var cut bool
		if hits, cut, err = ix.lines(ctx, repo, v, lineQuery{pattern: base, word: true, limit: RequireDefinedIn + 1}); err != nil {
			return res, err
		}
		if len(hits) > RequireDefinedIn || cut {
			return res, fmt.Errorf("%w: %s has more than %d matches; pass defined_in", mirror.ErrBadRequest, base, RequireDefinedIn)
		}
	} else {
		dep, err := ix.dependents(ctx, repo, v, q.DefinedIn, min(max(q.Depth, 1), MaxDependentsDep), q.IncludeRoots)
		if err != nil {
			return res, err
		}
		res.Packages, res.Proximity, res.Truncated = len(dep.Packages), dep.Proximity, dep.Truncated
		pkgDir := path.Dir(q.DefinedIn)
		scope, want := append([]string{pkgDir}, dep.Files...), 4*MaxReferences
		if pkgDir == "." {
			// A top-level package has no directory to scope by: search
			// the repo and keep the in-scope sites below.
			scope, want = nil, RequireDefinedIn
		}
		inScope := func(p string) bool {
			if res.Proximity {
				return pkgDir == "." || strings.HasPrefix(p, pkgDir+"/")
			}
			return path.Dir(p) == pkgDir || slices.Contains(dep.Files, p)
		}
		all, cut, err := ix.lines(ctx, repo, v, lineQuery{pattern: base, word: true, scope: scope, limit: want})
		if err != nil {
			return res, err
		}
		res.Truncated = res.Truncated || cut
		for _, m := range all {
			if inScope(m.Path) {
				hits = append(hits, m)
			}
		}
	}

	near := q.DefinedIn
	slices.SortStableFunc(hits, func(a, b Match) int {
		return cmp.Or(
			cmp.Compare(proximity(b.Path, near), proximity(a.Path, near)),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
		)
	})
	if len(hits) > limit {
		hits, res.Truncated = hits[:limit], true
	}
	for _, h := range hits {
		ref := Reference{Path: h.Path, Line: h.Line, Text: h.Text}
		start := max(1, h.Line-RefContext)
		if f, err := ix.cfg.Mirrors.ReadFile(ctx, repo, v.commit, h.Path, start, h.Line+RefContext); err == nil {
			ref.Context, ref.ContextStart = f.Text, start
		}
		res.References = append(res.References, ref)
	}
	return res, nil
}

// proximity scores how close p is to near: the shared leading path
// segments, plus one for the same directory.
func proximity(p, near string) int {
	if near == "" {
		return 0
	}
	a, b := strings.Split(path.Dir(p), "/"), strings.Split(path.Dir(near), "/")
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	if path.Dir(p) == path.Dir(near) {
		n++
	}
	return n
}
