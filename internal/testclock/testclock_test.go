// Package testclock checks that tests wait on time only inside a
// testing/synctest bubble. A call to time.Sleep, After, AfterFunc, Tick,
// NewTicker, NewTimer, Since or Until, or to context.WithTimeout or
// WithDeadline, in a _test.go file passes when it sits inside the function
// given to synctest.Test, or in a helper whose every use in its directory
// does; anything else needs a
// "// wallclock: <reason>" comment on its line or the line above, for code
// that cannot run in a bubble (a real container, a goroutine parked in a
// syscall).
//
// Run it with go test ./internal/testclock.
package testclock

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// waits are, by import path, the functions that wait on or measure the clock.
var waits = map[string][]string{
	"time":    {"Sleep", "After", "AfterFunc", "Tick", "NewTicker", "NewTimer", "Since", "Until"},
	"context": {"WithTimeout", "WithTimeoutCause", "WithDeadline", "WithDeadlineCause"},
}

// skipDirs are directories under the module root holding no Go tests of ours.
var skipDirs = []string{".git", ".devenv", ".direnv", ".beads", "learning", "node_modules", "testdata", "vendor"}

var marker = regexp.MustCompile(`(?m)//\s*wallclock:[ \t]*(\S.*)?$`)

// finding is a clock wait outside a bubble.
type finding struct {
	pos  token.Position
	call string
	note string
}

func (f finding) String() string {
	return f.pos.String() + ": " + f.call + " " + f.note
}

// use is one clock wait or one reference to a helper.
type use struct {
	pos     token.Pos
	encl    string // enclosing top-level func or method name
	bubbled bool   // inside a synctest.Test closure
}

// wait is one clock wait and the comment on or above its line.
type wait struct {
	at      use
	name    string
	comment string
}

// scan reports the clock waits in files (one directory's _test.go files)
// that are not in a bubble or marked.
func scan(fset *token.FileSet, files []*ast.File) []finding {
	decls := map[string]bool{} // top-level func and method names
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				decls[fd.Name.Name] = true
			}
		}
	}
	var calls []wait
	refs := map[string][]use{}
	for _, f := range files {
		syncName := importName(f, "testing/synctest")
		names := map[string][]string{} // local package name -> its waits
		for path, fns := range waits {
			if n := importName(f, path); n != "" {
				names[n] = fns
			}
		}
		lines := commentLines(fset, f)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			var stack []ast.Node
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok && slices.Contains(names[x.Name], n.Sel.Name) {
						line := fset.Position(n.Pos()).Line
						c := lines[line]
						if c == "" {
							c = lines[line-1]
						}
						calls = append(calls, wait{use{n.Pos(), fd.Name.Name, inBubble(stack, syncName)}, x.Name + "." + n.Sel.Name, c})
					} else if decls[n.Sel.Name] {
						refs[n.Sel.Name] = append(refs[n.Sel.Name], use{n.Pos(), fd.Name.Name, inBubble(stack, syncName)})
					}
				case *ast.Ident:
					// A local that shares a helper's name counts as a use of
					// it: a false finding at worst, never a missed one.
					if decls[n.Name] {
						refs[n.Name] = append(refs[n.Name], use{n.Pos(), fd.Name.Name, inBubble(stack, syncName)})
					}
				}
				return true
			})
		}
	}
	bubbled := bubbledHelpers(refs)
	var out []finding
	for _, c := range calls {
		if c.at.bubbled || bubbled[c.at.encl] {
			continue
		}
		pos := fset.Position(c.at.pos)
		switch m := marker.FindStringSubmatch(c.comment); {
		case m == nil:
			out = append(out, finding{pos, c.name, "outside a synctest bubble: wrap the test in synctest.Test, or mark it // wallclock: <reason>"})
		case strings.TrimSpace(m[1]) == "":
			out = append(out, finding{pos, c.name, "has a wallclock marker without a reason"})
		}
	}
	return out
}

// bubbledHelpers returns the functions whose every reference is in a
// bubble or in another such function: the least fixed point, so a helper
// referenced only by itself is not one.
func bubbledHelpers(refs map[string][]use) map[string]bool {
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, us := range refs {
			if out[name] || len(us) == 0 {
				continue
			}
			if !slices.ContainsFunc(us, func(u use) bool { return !u.bubbled && !out[u.encl] }) {
				out[name], changed = true, true
			}
		}
	}
	return out
}

// inBubble reports whether the innermost node of stack is inside a
// function literal passed to synctest.Test.
func inBubble(stack []ast.Node, syncName string) bool {
	if syncName == "" {
		return false
	}
	for i := len(stack) - 2; i >= 0; i-- {
		c, ok := stack[i].(*ast.CallExpr)
		if !ok {
			continue
		}
		s, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != "Test" {
			continue
		}
		if x, ok := s.X.(*ast.Ident); ok && x.Name == syncName && slices.ContainsFunc(c.Args, func(a ast.Expr) bool { return a == stack[i+1] }) {
			if _, ok := stack[i+1].(*ast.FuncLit); ok {
				return true
			}
		}
	}
	return false
}

// importName returns the name file f refers to package path by, or "".
func importName(f *ast.File, path string) string {
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == path {
			if im.Name != nil {
				return im.Name.Name
			}
			return path[strings.LastIndex(path, "/")+1:]
		}
	}
	return ""
}

// commentLines maps each line of f a comment block ends on to the
// block's text, one comment per line.
func commentLines(fset *token.FileSet, f *ast.File) map[int]string {
	out := map[int]string{}
	for _, g := range f.Comments {
		var text []string
		for _, c := range g.List {
			text = append(text, c.Text)
		}
		out[fset.Position(g.End()).Line] = strings.Join(text, "\n")
	}
	return out
}

// moduleRoot returns the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestWaitsInBubbles scans every _test.go file in the module.
func TestWaitsInBubbles(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	byDir := map[string][]*ast.File{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (slices.Contains(skipDirs, d.Name()) || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byDir) < 10 {
		t.Fatalf("scanned only %d test directories under %s", len(byDir), root)
	}
	for _, files := range byDir {
		for _, f := range scan(fset, files) {
			t.Error(f)
		}
	}
}

func TestScan(t *testing.T) {
	t.Parallel()
	const head = "package p\n\nimport (\n\t\"context\"\n\t\"testing\"\n\t\"testing/synctest\"\n\t\"time\"\n)\n\n"
	for name, tc := range map[string]struct {
		src  string
		want []string // "line call" of each finding
	}{
		"in a bubble": {`func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { time.Sleep(time.Second) })
}`, nil},
		"outside a bubble": {`func TestA(t *testing.T) {
	time.Sleep(time.Second)
	<-time.After(time.Second)
}`, []string{"11 time.Sleep", "12 time.After"}},
		"before the bubble": {`func TestA(t *testing.T) {
	d := time.Until(time.Time{})
	synctest.Test(t, func(t *testing.T) { _ = d })
}`, []string{"11 time.Until"}},
		"helper called in a bubble": {`func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { wait(t) })
}

func wait(t *testing.T) { deeper() }

func deeper() { time.Sleep(time.Second) }`, nil},
		"helper called outside too": {`func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { wait() })
	wait()
}

func wait() { time.Sleep(time.Second) }`, []string{"15 time.Sleep"}},
		"method helper": {`type c struct{}

func (c) wait() { time.Sleep(time.Second) }

func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { c{}.wait() })
}`, nil},
		"helper never called": {`func wait() { time.Sleep(time.Second) }`, []string{"10 time.Sleep"}},
		"marked": {`func TestA(t *testing.T) {
	time.Sleep(time.Second) // wallclock: a real container
	// wallclock: a real container,
	// whose wait runs in wall time
	time.Sleep(time.Second)
}`, nil},
		"marked without a reason": {`func TestA(t *testing.T) {
	time.Sleep(time.Second) // wallclock:
}`, []string{"11 time.Sleep"}},
		"context deadline": {`func TestA(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = ctx
}`, []string{"11 context.WithTimeout"}},
		"durations and dates": {`func TestA(t *testing.T) {
	_ = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Hour)
	_ = time.Now()
}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "p_test.go", head+tc.src, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, fd := range scan(fset, []*ast.File{f}) {
				got = append(got, strconv.Itoa(fd.pos.Line)+" "+fd.call)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScanAliasedImports(t *testing.T) {
	t.Parallel()
	src := "package p\n\nimport (\n\t\"testing\"\n\tst \"testing/synctest\"\n\tclock \"time\"\n)\n\n" +
		"func TestA(t *testing.T) {\n\tst.Test(t, func(t *testing.T) { clock.Sleep(1) })\n\tclock.Sleep(1)\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if got := scan(fset, []*ast.File{f}); len(got) != 1 || got[0].pos.Line != 11 {
		t.Errorf("findings = %v, want one on line 11", got)
	}
}
