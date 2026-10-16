package index

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/koblas/exemplar/internal/search/mirror"
)

// Package scoping by imports: a file's package identity per
// language, and how to recognize a line that imports it. Each language
// gives a coarse pattern, written so it is valid both as RE2 (the index)
// and as POSIX ERE (git grep in degraded mode), and a precise check in Go
// that every candidate line must pass. Languages without rules here use
// directory proximity.

// pkgSpec is how to find the importers of one file's package.
type pkgSpec struct {
	lang string
	// dir is the package's directory: the file's own.
	dir string
	// coarse finds candidate import lines; files narrows the files searched
	// (an RE2 over paths, also applied in Go). Both are required.
	coarse, files string
	// imports is the precise check of a candidate line in importer.
	imports func(importer, line string) bool
}

// ereQuote escapes a literal for both RE2 and POSIX ERE.
func ereQuote(s string) string { return regexp.QuoteMeta(s) }

var (
	goExts   = `\.go$`
	pyExts   = `\.py$`
	jsExts   = `\.(ts|tsx|js|jsx|mjs|cjs|mts|cts)$`
	jvmExts  = `\.(java|kt|kts)$`
	cExts    = `\.(c|cc|cpp|cxx|h|hh|hpp|hxx|ipp|m|mm)$`
	jsExtRE  = regexp.MustCompile(jsExts)
	cExtRE   = regexp.MustCompile(cExts)
	cHeaders = []string{".h", ".hpp", ".hh", ".hxx"}

	jsSpecRE  = regexp.MustCompile(`(?:\bfrom\s+|\brequire\(\s*|\bimport\(\s*|^\s*import\s+)['"]([^'"]+)['"]`)
	includeRE = regexp.MustCompile(`^\s*#\s*(?:include|import)\s*[<"]([^>"]+)[>"]`)
	javaPkgRE = regexp.MustCompile(`^\s*package\s+([A-Za-z_][\w.]*)`)
	goModRE   = regexp.MustCompile(`(?m)^\s*module\s+"?([^\s"]+)"?`)
)

// packageSpec returns how to find importers of file's package at commit,
// or false when its language has no import rules (directory proximity).
func (ix *Index) packageSpec(ctx context.Context, repo, commit, file string, includeRoots []string) (pkgSpec, bool) {
	ext := strings.ToLower(path.Ext(file))
	switch {
	case ext == ".go":
		return ix.goSpec(ctx, repo, commit, file)
	case ext == ".py":
		return pySpec(file), true
	case jsExtRE.MatchString(file):
		return jsSpec(file), true
	case ext == ".java" || ext == ".kt" || ext == ".kts":
		return ix.jvmSpec(ctx, repo, commit, file)
	case cExtRE.MatchString(file):
		return cSpec(file, includeRoots), true
	}
	return pkgSpec{}, false
}

// goSpec: the import path is the nearest go.mod's module plus the
// directory relative to it.
func (ix *Index) goSpec(ctx context.Context, repo, commit, file string) (pkgSpec, bool) {
	dir := path.Dir(file)
	for d := dir; ; d = path.Dir(d) {
		mod := path.Join(d, "go.mod")
		if f, err := ix.cfg.Mirrors.ReadFile(ctx, repo, commit, mod, 1, mirror.MaxReadLines); err == nil {
			m := goModRE.FindStringSubmatch(f.Text)
			if m == nil {
				return pkgSpec{}, false
			}
			id := m[1]
			if d != dir {
				rel := strings.TrimPrefix(dir, d+"/")
				if d == "." {
					rel = dir
				}
				id += "/" + rel
			}
			line := regexp.MustCompile(`^\s*(?:import\s+)?(?:[\w.]+\s+)?"` + regexp.QuoteMeta(id) + `"`)
			return pkgSpec{
				lang: "Go", dir: dir, coarse: `"` + ereQuote(id) + `"`, files: goExts,
				imports: func(_, l string) bool { return line.MatchString(l) },
			}, true
		}
		if d == "." || d == "/" {
			return pkgSpec{}, false
		}
	}
}

// pySpec: the dotted module from the path, also without its leading
// segments (import roots are not known), matched by "from M import",
// "import M" and "from parent import leaf".
func pySpec(file string) pkgSpec {
	mod := strings.TrimSuffix(file, path.Ext(file))
	if path.Base(mod) == "__init__" {
		mod = path.Dir(mod)
	}
	parts := strings.Split(mod, "/")
	var alts []string
	for i := 0; i < len(parts) && i < 3; i++ {
		m := parts[i:]
		q := regexp.QuoteMeta(strings.Join(m, "."))
		alts = append(alts, `from\s+`+q+`(?:\.\w+)*\s+import\b`, `import\s+(?:.*[\s,])?`+q+`\b`)
		if len(m) > 1 {
			parent := regexp.QuoteMeta(strings.Join(m[:len(m)-1], "."))
			alts = append(alts, `from\s+`+parent+`\s+import\s+.*\b`+regexp.QuoteMeta(m[len(m)-1])+`\b`)
		}
	}
	line := regexp.MustCompile(`^\s*(?:` + strings.Join(alts, "|") + `)`)
	leaf := parts[len(parts)-1]
	return pkgSpec{
		lang: "Python", dir: path.Dir(file), coarse: `import.*` + ereQuote(leaf) + `|` + ereQuote(leaf) + `.*import`, files: pyExts,
		imports: func(_, l string) bool { return line.MatchString(l) },
	}
}

// jsSpec: a module is its path without extension (an index file is also
// its directory). Relative specifiers are resolved against the importer;
// others match by their last two segments.
func jsSpec(file string) pkgSpec {
	target := strings.TrimSuffix(file, path.Ext(file))
	stem := path.Base(target)
	isIndex := stem == "index"
	name := stem
	if isIndex {
		name = path.Base(path.Dir(target))
	}
	tail, tailDir := "/"+lastTwo(target), "/"+lastTwo(path.Dir(target))
	return pkgSpec{
		lang: "TypeScript", dir: path.Dir(file),
		coarse: `['"][^'"]*` + ereQuote(name) + `(/index)?(\.[a-z]+)?['"]`, files: jsExts,
		imports: func(importer, l string) bool {
			for _, m := range jsSpecRE.FindAllStringSubmatch(l, -1) {
				spec := m[1]
				if strings.HasPrefix(spec, ".") {
					r := path.Join(path.Dir(importer), spec)
					r = jsExtRE.ReplaceAllString(r, "")
					if r == target || (isIndex && r == path.Dir(target)) || r+"/index" == target {
						return true
					}
					continue
				}
				s := "/" + jsExtRE.ReplaceAllString(spec, "")
				if strings.HasSuffix(s, tail) || (isIndex && strings.HasSuffix(s, tailDir)) {
					return true
				}
			}
			return false
		},
	}
}

// lastTwo is p's last two path segments (one at the top level).
func lastTwo(p string) string {
	if d := path.Dir(p); d != "." && d != "/" {
		return path.Base(d) + "/" + path.Base(p)
	}
	return path.Base(p)
}

// jvmSpec: the package declaration, and imports of the file's class or
// the whole package.
func (ix *Index) jvmSpec(ctx context.Context, repo, commit, file string) (pkgSpec, bool) {
	f, err := ix.cfg.Mirrors.ReadFile(ctx, repo, commit, file, 1, 60)
	if err != nil {
		return pkgSpec{}, false
	}
	var pkg string
	for l := range strings.SplitSeq(f.Text, "\n") {
		if m := javaPkgRE.FindStringSubmatch(l); m != nil {
			pkg = strings.TrimSuffix(m[1], ";")
			break
		}
	}
	if pkg == "" {
		return pkgSpec{}, false
	}
	class := strings.TrimSuffix(path.Base(file), path.Ext(file))
	line := regexp.MustCompile(`^\s*import\s+(?:static\s+)?` + regexp.QuoteMeta(pkg) + `\.(?:` + regexp.QuoteMeta(class) + `\b|\*)`)
	return pkgSpec{
		lang: "Java", dir: path.Dir(file), coarse: `import[[:space:]]+(static[[:space:]]+)?` + ereQuote(pkg) + `\.`, files: jvmExts,
		imports: func(_, l string) bool { return line.MatchString(l) },
	}, true
}

// cSpec: the header (for a source file, same-stem headers beside it; a
// header kept elsewhere is not found from its source file) as included
// relative to an include root, as its own path, or relative to the
// including file's directory.
func cSpec(file string, includeRoots []string) pkgSpec {
	headers := []string{file}
	if ext := strings.ToLower(path.Ext(file)); !strings.HasPrefix(ext, ".h") {
		headers = make([]string, 0, len(cHeaders))
		for _, h := range cHeaders {
			headers = append(headers, strings.TrimSuffix(file, path.Ext(file))+h)
		}
	}
	accepted := map[string]bool{}
	bases := make([]string, 0, len(headers))
	for _, h := range headers {
		accepted[h] = true
		for _, root := range includeRoots {
			root = strings.TrimSuffix(path.Clean(root), "/") + "/"
			if rel, ok := strings.CutPrefix(h, root); ok {
				accepted[rel] = true
			}
		}
		bases = append(bases, ereQuote(path.Base(h)))
	}
	return pkgSpec{
		lang: "C++", dir: path.Dir(file),
		coarse: `#[[:space:]]*(include|import)[[:space:]]*[<"][^>"]*(` + strings.Join(bases, "|") + `)[>"]`, files: cExts,
		imports: func(importer, l string) bool {
			m := includeRE.FindStringSubmatch(l)
			if m == nil {
				return false
			}
			return accepted[m[1]] || accepted[path.Join(path.Dir(importer), m[1])]
		},
	}
}
