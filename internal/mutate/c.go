package mutate

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/koblas/exemplar/internal/syntax"
)

// C/C++ operators (tree-sitter), one per class like Go's:
//   - bug.error-handling: remove an `if (rc …) { return/goto … }` right
//     after rc is assigned from a call (null checks are nil-deref's);
//   - bug.off-by-one: a for loop's `i < n` becomes `i <= n`;
//   - bug.resource-leak: drop a `free(x)`, `fclose(x)`, `close(x)` … of
//     something assigned from a call earlier in the block;
//   - bug.concurrency: remove `pthread_mutex_lock(&m)` / `m.lock()` and
//     its unlock in one block, or a `std::lock_guard` (and friends);
//   - bug.nil-deref: remove an `if (p == NULL)` / `if (!p)` guard whose
//     body only returns.
//
// Statements are removed as whole lines, so an operator applies only to a
// statement alone on its lines. A mutation is kept only if the result
// parses cleanly.

var (
	releaseFns = []string{"free", "fclose", "close", "closedir", "pclose", "munmap", "freeaddrinfo"}
	lockFns    = map[string]string{"pthread_mutex_lock": "pthread_mutex_unlock", "pthread_rwlock_wrlock": "pthread_rwlock_unlock",
		"pthread_rwlock_rdlock": "pthread_rwlock_unlock", "pthread_spin_lock": "pthread_spin_unlock"}
	guardTypes = regexp.MustCompile(`^(std::)?(lock_guard|unique_lock|scoped_lock)\b`)
)

// C returns the mutations of a C or C++ file, at most perOp per operator,
// in file order.
func C(ctx context.Context, path string, src []byte, perOp int) ([]Mutation, error) {
	tree, err := syntax.Parse(ctx, path, src)
	if err != nil {
		return nil, fmt.Errorf("mutate: %s: %w", path, err)
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return nil, fmt.Errorf("mutate: %s does not parse", path)
	}
	lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
	var out []Mutation
	count := map[string]int{}
	keep := func(m Mutation) {
		if count[m.Op] >= perOp || m.Changed() == 0 || m.Changed() > MaxChanged || m.Expect[0] < 1 || m.Expect[1] < m.Expect[0] {
			return
		}
		mutated := strings.Join(m.Apply(lines), "\n") + "\n"
		if n, err := syntax.Errors(ctx, path, []byte(mutated)); err != nil || n > 0 {
			return
		}
		m.Path = path
		count[m.Op]++
		out = append(out, m)
	}
	text := func(n *sitter.Node) string { return n.Utf8Text(src) }
	// span is a node's 1-based line range, when it is alone on its lines.
	span := func(n *sitter.Node) (int, int, bool) {
		s, e := n.StartPosition(), n.EndPosition()
		if int(s.Row) >= len(lines) || int(e.Row) >= len(lines) {
			return 0, 0, false
		}
		before := lines[s.Row][:min(int(s.Column), len(lines[s.Row]))]
		after := lines[e.Row][min(int(e.Column), len(lines[e.Row])):]
		if strings.TrimSpace(before) != "" || strings.TrimSpace(after) != "" {
			return 0, 0, false
		}
		return int(s.Row) + 1, int(e.Row) + 1, true
	}
	deleteLines := func(from, to int) map[int]*string {
		e := map[int]*string{}
		for l := from; l <= to; l++ {
			e[l] = nil
		}
		return e
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Kind() {
		case "compound_statement":
			stmts := namedChildren(n)
			for i, st := range stmts {
				switch st.Kind() {
				case "if_statement":
					if st.ChildByFieldName("alternative") != nil {
						break
					}
					from, to, ok := span(st)
					if !ok {
						break
					}
					cond, body := st.ChildByFieldName("condition"), st.ChildByFieldName("consequence")
					if cond == nil || body == nil {
						break
					}
					if v, isNull := nullCheck(cond, src); isNull && onlyReturns(body) && i+1 < len(stmts) && v != "" {
						m := Mutation{Class: NilDeref, Op: "drop-null-guard", Edits: deleteLines(from, to)}
						if f, t, ok := span(stmts[i+1]); ok {
							m.Expect = [2]int{m.newLine(f), m.newLine(t)}
							keep(m)
						}
					} else if i > 0 && exits(body) {
						if v := assignedFromCall(stmts[i-1], src); v != "" && mentions(cond, v, src) {
							if f, t, ok := span(stmts[i-1]); ok {
								keep(Mutation{Class: ErrorHandling, Op: "drop-err-check", Edits: deleteLines(from, to), Expect: [2]int{f, t}})
							}
						}
					}
				case "expression_statement":
					call := firstNamed(st)
					if call == nil || call.Kind() != "call_expression" {
						break
					}
					fn, args := call.ChildByFieldName("function"), call.ChildByFieldName("arguments")
					if fn == nil || args == nil {
						break
					}
					from, to, ok := span(st)
					if !ok {
						break
					}
					name, recv := callee(fn, src)
					// A release of something acquired earlier in the block.
					if slices.Contains(releaseFns, name) && recv == "" && args.NamedChildCount() == 1 {
						x := text(args.NamedChild(0))
						for k := i - 1; k >= 0; k-- {
							if assignedFromCall(stmts[k], src) == x {
								if f, t, ok := span(stmts[k]); ok {
									keep(Mutation{Class: ResourceLeak, Op: "drop-release", Edits: deleteLines(from, to), Expect: [2]int{f, t}})
								}
								break
							}
						}
					}
					// A lock and its unlock in this block.
					unlock, key := "", ""
					if u, ok := lockFns[name]; ok && args.NamedChildCount() == 1 {
						unlock, key = u, text(args.NamedChild(0))
					} else if name == "lock" && recv != "" && args.NamedChildCount() == 0 {
						unlock, key = "unlock", recv
					}
					if unlock == "" {
						break
					}
					for k := i + 1; k < len(stmts); k++ {
						c := firstNamed(stmts[k])
						if stmts[k].Kind() != "expression_statement" || c == nil || c.Kind() != "call_expression" {
							continue
						}
						un, ur := callee(c.ChildByFieldName("function"), src)
						ua := c.ChildByFieldName("arguments")
						match := un == unlock && ((recv == "" && ua != nil && ua.NamedChildCount() == 1 && text(ua.NamedChild(0)) == key) || (recv != "" && ur == key))
						if !match {
							continue
						}
						uf, ut, ok := span(stmts[k])
						if ok && k > i+1 {
							edits := deleteLines(from, to)
							for l := uf; l <= ut; l++ {
								edits[l] = nil
							}
							m := Mutation{Class: Concurrency, Op: "drop-lock", Edits: edits}
							gf, _, ok1 := span(stmts[i+1])
							_, gt, ok2 := span(stmts[k-1])
							if ok1 && ok2 {
								m.Expect = [2]int{m.newLine(gf), m.newLine(gt)}
								keep(m)
							}
						}
						break
					}
				case "declaration":
					// A scoped guard: everything after it in the block loses the lock.
					typ := st.ChildByFieldName("type")
					if typ == nil || !guardTypes.MatchString(text(typ)) || i+1 >= len(stmts) {
						break
					}
					from, to, ok := span(st)
					if !ok {
						break
					}
					m := Mutation{Class: Concurrency, Op: "drop-lock-guard", Edits: deleteLines(from, to)}
					gf, _, ok1 := span(stmts[i+1])
					_, gt, ok2 := span(stmts[len(stmts)-1])
					if ok1 && ok2 {
						m.Expect = [2]int{m.newLine(gf), m.newLine(gt)}
						keep(m)
					}
				}
			}
		case "for_statement":
			cond := n.ChildByFieldName("condition")
			if cond != nil && cond.Kind() == "binary_expression" {
				if op := cond.ChildByFieldName("operator"); op != nil && op.Kind() == "<" {
					p := op.StartPosition()
					l := lines[p.Row]
					if int(p.Column)+1 <= len(l) && l[p.Column] == '<' {
						mutated := l[:p.Column] + "<=" + l[p.Column+1:]
						line := int(p.Row) + 1
						keep(Mutation{Class: OffByOne, Op: "lt-to-le", Edits: map[int]*string{line: &mutated}, Expect: [2]int{line, line}})
					}
				}
			}
		}
		for _, c := range namedChildren(n) {
			walk(c)
		}
	}
	walk(tree.RootNode())
	slices.SortStableFunc(out, func(a, b Mutation) int { return a.Expect[0] - b.Expect[0] })
	return out, nil
}

func namedChildren(n *sitter.Node) []*sitter.Node {
	out := make([]*sitter.Node, 0, n.NamedChildCount())
	for i := range n.NamedChildCount() {
		if c := n.NamedChild(i); c != nil && c.Kind() != "comment" {
			out = append(out, c)
		}
	}
	return out
}

func firstNamed(n *sitter.Node) *sitter.Node {
	if n.NamedChildCount() == 0 {
		return nil
	}
	return n.NamedChild(0)
}

// unparen strips parentheses (an if's condition is a
// parenthesized_expression, or a condition_clause in C++).
func unparen(n *sitter.Node) *sitter.Node {
	for n != nil && (n.Kind() == "parenthesized_expression" || n.Kind() == "condition_clause") && n.NamedChildCount() == 1 {
		n = n.NamedChild(0)
	}
	return n
}

// nullCheck reports whether cond is `x == NULL`, `x == nullptr`, `NULL ==
// x`, `x == 0` on a pointer-ish name, or `!x`, and returns x.
func nullCheck(cond *sitter.Node, src []byte) (string, bool) {
	c := unparen(cond)
	if c == nil {
		return "", false
	}
	isNull := func(n *sitter.Node) bool {
		t := n.Utf8Text(src)
		return t == "NULL" || t == "nullptr" || n.Kind() == "null"
	}
	switch c.Kind() {
	case "binary_expression":
		op := c.ChildByFieldName("operator")
		l, r := c.ChildByFieldName("left"), c.ChildByFieldName("right")
		if op == nil || op.Kind() != "==" || l == nil || r == nil {
			return "", false
		}
		if isNull(r) && l.Kind() == "identifier" {
			return l.Utf8Text(src), true
		}
		if isNull(l) && r.Kind() == "identifier" {
			return r.Utf8Text(src), true
		}
	case "unary_expression":
		op, arg := c.ChildByFieldName("operator"), c.ChildByFieldName("argument")
		if op != nil && op.Kind() == "!" && arg != nil && arg.Kind() == "identifier" {
			return arg.Utf8Text(src), true
		}
	}
	return "", false
}

// onlyReturns reports whether body is a return, alone or in braces.
func onlyReturns(body *sitter.Node) bool {
	if body.Kind() == "return_statement" {
		return true
	}
	if body.Kind() == "compound_statement" {
		st := namedChildren(body)
		return len(st) == 1 && st[0].Kind() == "return_statement"
	}
	return false
}

// exits reports whether body returns or jumps to error handling.
func exits(body *sitter.Node) bool {
	found := false
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found {
			return
		}
		if k := n.Kind(); k == "return_statement" || k == "goto_statement" {
			found = true
			return
		}
		for _, c := range namedChildren(n) {
			walk(c)
		}
	}
	walk(body)
	return found
}

// assignedFromCall returns the variable st assigns from a call: `T x =
// f(…);`, `T *x = f(…);` or `x = f(…);`, else "".
func assignedFromCall(st *sitter.Node, src []byte) string {
	switch st.Kind() {
	case "declaration":
		for _, c := range namedChildren(st) {
			if c.Kind() != "init_declarator" {
				continue
			}
			d, v := c.ChildByFieldName("declarator"), c.ChildByFieldName("value")
			for d != nil && (d.Kind() == "pointer_declarator" || d.Kind() == "reference_declarator") {
				if next := d.ChildByFieldName("declarator"); next != nil {
					d = next
				} else {
					d = firstNamed(d)
				}
			}
			if d != nil && d.Kind() == "identifier" && v != nil && v.Kind() == "call_expression" {
				return d.Utf8Text(src)
			}
		}
	case "expression_statement":
		a := firstNamed(st)
		if a != nil && a.Kind() == "assignment_expression" {
			l, r := a.ChildByFieldName("left"), a.ChildByFieldName("right")
			if l != nil && l.Kind() == "identifier" && r != nil && r.Kind() == "call_expression" {
				return l.Utf8Text(src)
			}
		}
	}
	return ""
}

// mentions reports whether n refers to the identifier name.
func mentions(n *sitter.Node, name string, src []byte) bool {
	if n.Kind() == "identifier" && n.Utf8Text(src) == name {
		return true
	}
	for _, c := range namedChildren(n) {
		if mentions(c, name, src) {
			return true
		}
	}
	return false
}

// callee names a call's function: `f` gives ("f", ""), `m.lock` and
// `m->lock` give ("lock", "m").
func callee(fn *sitter.Node, src []byte) (name, recv string) {
	if fn == nil {
		return "", ""
	}
	switch fn.Kind() {
	case "identifier":
		return fn.Utf8Text(src), ""
	case "field_expression":
		arg, field := fn.ChildByFieldName("argument"), fn.ChildByFieldName("field")
		if arg != nil && field != nil {
			return field.Utf8Text(src), arg.Utf8Text(src)
		}
	}
	return "", ""
}
