// Package mutate generates bug-class recall cases by mutation:
// deterministic operators applied to clean code produce one small
// bug each, with no model involved. Each mutation is a set of line edits
// with the lines where the bug class should then report a finding.
//
// Operators, for Go (go/ast), one per class:
//   - bug.error-handling: remove an `if err != nil { … }` check that follows
//     the assignment of err;
//   - bug.off-by-one: turn `< len(x)` into `<= len(x)`;
//   - bug.resource-leak: drop a `defer x.Close()`;
//   - bug.concurrency: remove a `mu.Lock()` and its `mu.Unlock()` (or
//     `defer mu.Unlock()`) in one block;
//   - bug.nil-deref: remove an `if x == nil { return … }` guard.
//
// A mutation is kept only if the result still parses, it changes at most
// 15 lines, and it changes something.
package mutate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
)

// Classes the operators produce.
const (
	ErrorHandling = "bug.error-handling"
	OffByOne      = "bug.off-by-one"
	ResourceLeak  = "bug.resource-leak"
	Concurrency   = "bug.concurrency"
	NilDeref      = "bug.nil-deref"
)

// MaxChanged bounds a mutation's changed lines.
const MaxChanged = 15

// Mutation is one bug introduced into one file.
type Mutation struct {
	Class string
	Op    string
	Path  string
	// Edits replace (non-nil) or delete (nil) original lines, 1-based.
	Edits map[int]*string
	// Expect is the new file's line range where a finding is expected.
	Expect [2]int
}

// Apply returns the mutated lines.
func (m Mutation) Apply(lines []string) []string {
	var out []string
	for i, l := range lines {
		e, edited := m.Edits[i+1]
		switch {
		case !edited:
			out = append(out, l)
		case e != nil:
			out = append(out, *e)
		}
	}
	return out
}

// newLine maps an original line number to the mutated file's.
func (m Mutation) newLine(n int) int {
	deleted := 0
	for l, e := range m.Edits {
		if e == nil && l < n {
			deleted++
		}
	}
	return n - deleted
}

// Changed is the number of changed lines (deleted, plus replaced twice).
func (m Mutation) Changed() int {
	n := 0
	for _, e := range m.Edits {
		n++
		if e != nil {
			n++
		}
	}
	return n
}

// Diff renders the mutation as a git-style unified diff, one hunk per
// group of edits with 3 lines of context.
func (m Mutation) Diff(lines []string) string {
	const ctx = 3
	edited := make([]int, 0, len(m.Edits))
	for l := range m.Edits {
		edited = append(edited, l)
	}
	slices.Sort(edited)
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", m.Path, m.Path, m.Path, m.Path)
	shift := 0 // new minus old line numbers so far
	for i := 0; i < len(edited); {
		// A hunk: edits closer than 2*ctx lines apart, with context.
		j := i
		for j+1 < len(edited) && edited[j+1]-edited[j] <= 2*ctx {
			j++
		}
		from, to := max(1, edited[i]-ctx), min(len(lines), edited[j]+ctx)
		var body strings.Builder
		oldN, newN := 0, 0
		for l := from; l <= to; l++ {
			e, ok := m.Edits[l]
			switch {
			case !ok:
				body.WriteString(" " + lines[l-1] + "\n")
				oldN++
				newN++
			case e == nil:
				body.WriteString("-" + lines[l-1] + "\n")
				oldN++
			default:
				body.WriteString("-" + lines[l-1] + "\n+" + *e + "\n")
				oldN++
				newN++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n%s", from, oldN, from+shift, newN, body.String())
		shift += newN - oldN
		i = j + 1
	}
	return b.String()
}

// Go returns the mutations of a Go file, at most perOp per operator, in
// file order. Only mutations whose result parses are returned.
func Go(path string, src []byte, perOp int) ([]Mutation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("mutate: %s does not parse: %w", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
	line := func(p token.Pos) int { return fset.Position(p).Line }
	var out []Mutation
	count := map[string]int{}
	keep := func(m Mutation) {
		if count[m.Op] >= perOp || m.Changed() == 0 || m.Changed() > MaxChanged {
			return
		}
		mutated := strings.Join(m.Apply(lines), "\n") + "\n"
		if _, err := parser.ParseFile(token.NewFileSet(), path, mutated, parser.ParseComments); err != nil {
			return
		}
		m.Path = path
		count[m.Op]++
		out = append(out, m)
	}
	deleteLines := func(from, to int) map[int]*string {
		e := map[int]*string{}
		for l := from; l <= to; l++ {
			e[l] = nil
		}
		return e
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BlockStmt:
			for i, st := range n.List {
				switch st := st.(type) {
				case *ast.IfStmt:
					// Remove `if err != nil { … }` after err is assigned.
					if i > 0 && st.Init == nil && st.Else == nil && isNilCompare(st.Cond, token.NEQ, "err") && assigns(n.List[i-1], "err") {
						prev := n.List[i-1]
						keep(Mutation{
							Class: ErrorHandling, Op: "drop-err-check", Edits: deleteLines(line(st.Pos()), line(st.End())),
							Expect: [2]int{line(prev.Pos()), line(prev.End())},
						})
					}
					// Remove a `if x == nil { return … }` guard.
					if st.Init == nil && st.Else == nil && len(st.Body.List) == 1 && i+1 < len(n.List) {
						if _, ret := st.Body.List[0].(*ast.ReturnStmt); ret && isNilCompare(st.Cond, token.EQL, "") {
							from := line(st.Pos())
							m := Mutation{Class: NilDeref, Op: "drop-nil-guard", Edits: deleteLines(from, line(st.End()))}
							next := line(n.List[i+1].Pos())
							m.Expect = [2]int{m.newLine(next), m.newLine(line(n.List[i+1].End()))}
							keep(m)
						}
					}
				case *ast.DeferStmt:
					// Drop `defer x.Close()`; the leak is where x was acquired.
					if sel, ok := st.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" && len(st.Call.Args) == 0 {
						if id, ok := sel.X.(*ast.Ident); ok {
							for k := i - 1; k >= 0; k-- {
								if assigns(n.List[k], id.Name) {
									acq := n.List[k]
									keep(Mutation{
										Class: ResourceLeak, Op: "drop-defer-close", Edits: deleteLines(line(st.Pos()), line(st.End())),
										Expect: [2]int{line(acq.Pos()), line(acq.End())},
									})
									break
								}
							}
						}
					}
				case *ast.ExprStmt:
					// Remove `mu.Lock()` and its unlock in this block.
					recv, ok := method(st.X, "Lock")
					if !ok {
						continue
					}
					for k := i + 1; k < len(n.List); k++ {
						var unlock ast.Expr
						switch u := n.List[k].(type) {
						case *ast.ExprStmt:
							unlock = u.X
						case *ast.DeferStmt:
							unlock = u.Call
						}
						if r, ok := method(unlock, "Unlock"); ok && r == recv {
							lock, unl := line(st.Pos()), line(n.List[k].Pos())
							m := Mutation{Class: Concurrency, Op: "drop-lock", Edits: map[int]*string{lock: nil, unl: nil}}
							// The guarded statements, now unguarded.
							last := len(n.List) - 1
							if _, isDefer := n.List[k].(*ast.DeferStmt); !isDefer {
								last = k - 1
							}
							first := i + 1
							if first == k {
								first = k + 1
							}
							if first <= last && first < len(n.List) {
								m.Expect = [2]int{m.newLine(line(n.List[first].Pos())), m.newLine(line(n.List[last].End()))}
								keep(m)
							}
							break
						}
					}
				}
			}
		case *ast.BinaryExpr:
			// `< len(x)` becomes `<= len(x)`.
			if n.Op == token.LSS {
				if call, ok := n.Y.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "len" {
						p := fset.Position(n.OpPos)
						l := lines[p.Line-1]
						if p.Column-1 < len(l) && l[p.Column-1] == '<' {
							mutated := l[:p.Column-1] + "<=" + l[p.Column:]
							keep(Mutation{
								Class: OffByOne, Op: "lt-to-le", Edits: map[int]*string{p.Line: &mutated},
								Expect: [2]int{p.Line, p.Line},
							})
						}
					}
				}
			}
		}
		return true
	})
	slices.SortStableFunc(out, func(a, b Mutation) int { return a.Expect[0] - b.Expect[0] })
	return out, nil
}

// isNilCompare reports whether e is `<name> <op> nil` (any name when name
// is empty).
func isNilCompare(e ast.Expr, op token.Token, name string) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != op {
		return false
	}
	x, ok := b.X.(*ast.Ident)
	y, ok2 := b.Y.(*ast.Ident)
	return ok && ok2 && y.Name == "nil" && (name == "" || x.Name == name)
}

// assigns reports whether st assigns (or declares) name.
func assigns(st ast.Stmt, name string) bool {
	a, ok := st.(*ast.AssignStmt)
	if !ok {
		return false
	}
	return slices.ContainsFunc(a.Lhs, func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == name
	})
}

// method returns the receiver of a call `<recv>.<name>()`.
func method(e ast.Expr, name string) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return "", false
	}
	return exprName(sel.X), true
}

// exprName renders a receiver expression for comparison (a.b.mu).
func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}
