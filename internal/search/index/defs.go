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

	"github.com/go-enry/go-enry/v2"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/query"

	"github.com/koblas/exemplar/internal/search/mirror"
)

// MaxDefinitions is find_definition's cap (spec §6.4.4).
const MaxDefinitions = 10

// maxCandidates bounds the lines inspected in regex mode.
const maxCandidates = 2000

// DefQuery is find_definition(name, near_path?). Name may be qualified
// (pkg.Func, Type.Method, ns::Class); the last segment is searched and the
// qualifier ranks results.
type DefQuery struct {
	Name     string
	NearPath string
}

// Definition is one candidate definition.
type Definition struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Kind   string `json:"kind,omitempty"`   // from ctags
	Parent string `json:"parent,omitempty"` // from ctags
}

// DefResult is a find_definition answer.
type DefResult struct {
	Definitions []Definition `json:"definitions"`
	Commit      string       `json:"commit"`
	// Symbols is true when answered from ctags symbols in the index;
	// otherwise per-language definition regexes were used and Degraded is
	// set (spec §6.4.4).
	Symbols  bool `json:"symbols"`
	Degraded bool `json:"degraded"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*((\.|::|#|/)[A-Za-z_$][A-Za-z0-9_$]*)*$`)

// splitName returns the searched base name and its qualifier segments.
func splitName(name string) (string, []string, error) {
	if len(name) > mirror.MaxPattern || !nameRE.MatchString(name) {
		return "", nil, fmt.Errorf("%w: %q is not an identifier or qualified name", mirror.ErrBadRequest, name)
	}
	parts := strings.FieldsFunc(strings.ReplaceAll(name, "::", "."), func(r rune) bool { return r == '.' || r == '#' || r == '/' })
	return parts[len(parts)-1], parts[:len(parts)-1], nil
}

// FindDefinition answers find_definition from ctags symbols when the
// serving generation has them, else with definition regexes over the
// index or, in degraded mode, git grep at the mirror commit.
func (ix *Index) FindDefinition(ctx context.Context, repo string, q DefQuery) (DefResult, error) {
	repo = strings.ToLower(repo)
	base, qual, err := splitName(q.Name)
	if err != nil {
		return DefResult{}, err
	}
	if q.NearPath != "" {
		if err := mirror.CleanPath(q.NearPath); err != nil {
			return DefResult{}, err
		}
	}
	lang := ""
	if q.NearPath != "" {
		lang = enry.GetLanguage(path.Base(q.NearPath), nil)
	}

	var (
		defs  []Definition
		res   = DefResult{Definitions: []Definition{}}
		rules interface{ Reason(path string) string }
	)
	g := ix.acquire(repo)
	switch {
	case g != nil && g.meta.Symbols:
		defer g.refs.Done()
		res.Commit, res.Symbols, rules = g.meta.Commit, true, g.rules
		if defs, err = ix.symbolDefs(ctx, g, base, lang); err != nil {
			return res, err
		}
	case g != nil:
		defer g.refs.Done()
		res.Commit, res.Degraded, rules = g.meta.Commit, true, g.rules
		if defs, err = ix.indexRegexDefs(ctx, g, base, lang, newDefiner(base)); err != nil {
			return res, err
		}
	default:
		res.Degraded = true
		if res.Commit, err = ix.cfg.Mirrors.Snapshot(ctx, repo); err != nil {
			return res, err
		}
		r, err := ix.cfg.Mirrors.Exclusions(ctx, repo, res.Commit)
		if err != nil {
			return res, err
		}
		rules = r
		raw, _, err := ix.cfg.Mirrors.GrepWord(ctx, repo, res.Commit, base, maxCandidates)
		def := newDefiner(base)
		if err != nil {
			return res, err
		}
		for _, m := range raw {
			if l := fileLang(m.Path); (lang == "" || sameFamily(l, lang)) && def.match(l, m.Text) {
				defs = append(defs, Definition{Path: m.Path, Line: m.Line, Text: m.Text})
			}
		}
	}
	defs = slices.DeleteFunc(defs, func(d Definition) bool { return rules.Reason(d.Path) != "" })
	rank(defs, qual, q.NearPath)
	res.Definitions = append(res.Definitions, defs[:min(len(defs), MaxDefinitions)]...)
	return res, nil
}

// symbolDefs runs a ctags symbol query for base.
func (ix *Index) symbolDefs(ctx context.Context, g *generation, base, lang string) ([]Definition, error) {
	re, _ := syntax.Parse("^"+regexp.QuoteMeta(base)+"$", syntax.Perl)
	and := &query.And{Children: []query.Q{&query.Symbol{Expr: &query.Regexp{Regexp: re, Content: true, CaseSensitive: true}}}}
	if q := langQuery(lang); q != nil {
		and.Children = append(and.Children, q)
	}
	files, err := ix.search(ctx, g, and)
	if err != nil {
		return nil, err
	}
	var defs []Definition
	for _, f := range files {
		for _, lm := range f.LineMatches {
			for _, frag := range lm.LineFragments {
				if s := frag.SymbolInfo; s != nil && s.Sym == base {
					defs = append(defs, Definition{
						Path: f.FileName, Line: lm.LineNumber,
						Text: strings.TrimRight(string(lm.Line), "\r\n"), Kind: s.Kind, Parent: s.Parent,
					})
					break
				}
			}
		}
	}
	return defs, nil
}

// indexRegexDefs finds whole-word occurrences in the index and keeps the
// lines that match a definition regex for their language.
func (ix *Index) indexRegexDefs(ctx context.Context, g *generation, base, lang string, def *definer) ([]Definition, error) {
	re, _ := syntax.Parse(`\b`+regexp.QuoteMeta(base)+`\b`, syntax.Perl)
	and := &query.And{Children: []query.Q{&query.Regexp{Regexp: re, Content: true, CaseSensitive: true}}}
	if q := langQuery(lang); q != nil {
		and.Children = append(and.Children, q)
	}
	files, err := ix.search(ctx, g, and)
	if err != nil {
		return nil, err
	}
	var defs []Definition
	for _, f := range files {
		l := f.Language
		if l == "" {
			l = fileLang(f.FileName)
		}
		for _, lm := range f.LineMatches {
			text := strings.TrimRight(string(lm.Line), "\r\n")
			if def.match(l, text) {
				defs = append(defs, Definition{Path: f.FileName, Line: lm.LineNumber, Text: text})
			}
		}
	}
	return defs, nil
}

func (ix *Index) search(ctx context.Context, g *generation, q query.Q) ([]zoekt.FileMatch, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	res, err := g.s.Search(ctx, query.Simplify(q), &zoekt.SearchOptions{
		MaxWallTime: QueryTimeout, ShardMaxMatchCount: maxCandidates, TotalMaxMatchCount: maxCandidates,
		MaxDocDisplayCount: maxCandidates,
	})
	if err != nil {
		return nil, fmt.Errorf("index: search: %w", err)
	}
	return res.Files, nil
}

// families group languages that share definitions across files: a .ts
// file may use a .js definition, a .cc file a .h one.
var families = [][]string{
	{"Go"},
	{"C", "C++", "Objective-C"},
	{"TypeScript", "TSX", "JavaScript"},
	{"Python"},
	{"Java", "Kotlin"},
}

func family(lang string) []string {
	for _, f := range families {
		if slices.Contains(f, lang) {
			return f
		}
	}
	if lang == "" {
		return nil
	}
	return []string{lang}
}

func sameFamily(a, b string) bool { return slices.Contains(family(b), a) }

// langQuery filters to lang's family; nil for no filter.
func langQuery(lang string) query.Q {
	f := family(lang)
	if len(f) == 0 {
		return nil
	}
	var or query.Or
	for _, l := range f {
		or.Children = append(or.Children, &query.Language{Language: l})
	}
	return &or
}

func fileLang(p string) string { return enry.GetLanguage(path.Base(p), nil) }

// defPatterns are per-language definition regexes (degraded mode, spec
// §6.4.4); %s is the quoted name. They aim for recall on common forms,
// not a parse: ranking and the 10-result cap do the rest.
var defPatterns = map[string][]string{
	"Go": {
		`^\s*func\s+(\([^)]*\)\s*)?%s\s*[\[(]`,
		`^\s*type\s+%s\b`,
		`^\s*(var|const)\s+%s\b`,
	},
	"C": {
		`^\s*(typedef\s+)?(struct|enum|union)\s+%s\b`,
		`^\s*#\s*define\s+%s\b`,
		`^[A-Za-z_][\w\s\*&:<>,]*\b%s\s*\([^;]*$`,
		`\}\s*%s\s*;`, // typedef struct {...} name;
	},
	"C++": {
		`^\s*(template\s*<[^>]*>\s*)?(class|struct|enum(\s+class)?|union|namespace)\s+%s\b`,
		`^\s*#\s*define\s+%s\b`,
		`^[A-Za-z_][\w\s\*&:<>,]*\b(\w+::)*%s\s*\([^;]*$`,
		`^\s*using\s+%s\s*=`,
	},
	"TypeScript": {
		`\b(function\*?|class|interface|type|enum|namespace)\s+%s\b`,
		`\b(const|let|var)\s+%s\b`,
		`^\s*((public|private|protected|static|async|readonly|abstract|override)\s+)*%s\s*(<[^>]*>)?\([^)]*\)\s*[:{]`,
	},
	"Python": {
		`^\s*(async\s+)?def\s+%s\s*\(`,
		`^\s*class\s+%s\b`,
		`^%s\s*(:[^=]*)?=`,
	},
	"Java": {
		`\b(class|interface|enum|record|@interface)\s+%s\b`,
		`^\s*((public|private|protected|static|final|abstract|synchronized|native|default)\s+)*[\w<>\[\],.?]+\s+%s\s*\(`,
	},
	"Kotlin": {
		`\b(class|interface|object|typealias)\s+%s\b`,
		`\bfun\s+(<[^>]*>\s*)?([\w.]+\.)?%s\s*\(`,
		`\b(val|var)\s+%s\b`,
	},
}

func init() {
	defPatterns["Objective-C"] = defPatterns["C"]
	defPatterns["TSX"] = defPatterns["TypeScript"]
	defPatterns["JavaScript"] = defPatterns["TypeScript"]
}

// statementRE marks lines that use a name rather than define it, which
// the looser patterns (a type followed by name and "(") would accept.
var statementRE = regexp.MustCompile(`^\s*(return|throw|new|else|case|yield|await|if|for|while|switch|catch|delete|go|defer)\b`)

// definer matches definition lines for one name, compiling each
// language's patterns once per query.
type definer struct {
	name  string
	cache map[string][]*regexp.Regexp
}

func newDefiner(name string) *definer {
	return &definer{name: regexp.QuoteMeta(name), cache: map[string][]*regexp.Regexp{}}
}

// match reports whether line defines the name in lang. Languages without
// patterns use all of them.
func (d *definer) match(lang, line string) bool {
	if statementRE.MatchString(line) {
		return false
	}
	res, ok := d.cache[lang]
	if !ok {
		pats, known := defPatterns[lang]
		if !known {
			for _, p := range defPatterns {
				pats = append(pats, p...)
			}
		}
		for _, p := range pats {
			res = append(res, regexp.MustCompile(fmt.Sprintf(p, d.name)))
		}
		d.cache[lang] = res
	}
	for _, re := range res {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// rank orders definitions: qualified-name match first, then proximity to
// nearPath (shared leading directories), then shorter and earlier paths.
func rank(defs []Definition, qual []string, nearPath string) {
	near := strings.Split(path.Dir(nearPath), "/")
	if nearPath == "" {
		near = nil
	}
	score := func(d Definition) (int, int) {
		q := 0
		if len(qual) > 0 {
			last := qual[len(qual)-1]
			dir := path.Dir(d.Path)
			stem := strings.TrimSuffix(path.Base(d.Path), path.Ext(d.Path))
			if d.Parent == last || path.Base(dir) == last || stem == last {
				q = 1
			}
		}
		shared := 0
		for i, seg := range strings.Split(path.Dir(d.Path), "/") {
			if i >= len(near) || near[i] != seg {
				break
			}
			shared++
		}
		return q, shared
	}
	slices.SortStableFunc(defs, func(a, b Definition) int {
		qa, sa := score(a)
		qb, sb := score(b)
		return cmp.Or(cmp.Compare(qb, qa), cmp.Compare(sb, sa),
			cmp.Compare(strings.Count(a.Path, "/"), strings.Count(b.Path, "/")),
			strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
}
