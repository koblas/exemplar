// Package syntax counts parse errors in source files with tree-sitter, so
// code can check that a model's edit still parses (spec §6.11 fix
// validation). Errors counts both ERROR nodes and MISSING nodes, the
// zero-width tokens tree-sitter inserts to recover from an unclosed paren
// or a dropped semicolon.
package syntax

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tscsharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// ErrUnsupported is returned for a file whose language has no grammar here.
var ErrUnsupported = errors.New("syntax: no grammar for the file's language")

// ErrTimeout is returned when parsing takes longer than Timeout.
var ErrTimeout = errors.New("syntax: parse timed out")

// Timeout bounds one parse.
const Timeout = 5 * time.Second

// grammars maps lower-cased file extensions to tree-sitter grammars. C
// headers parse as C++, which is close to a superset of C: a header is
// as likely to be C++ as C, and a fix is judged by the errors it adds,
// not by a clean parse.
var grammars = map[string]func() unsafe.Pointer{
	".c": tsc.Language,
	".h": tscpp.Language, ".cc": tscpp.Language, ".cpp": tscpp.Language, ".cxx": tscpp.Language,
	".hh": tscpp.Language, ".hpp": tscpp.Language, ".hxx": tscpp.Language, ".ipp": tscpp.Language, ".inl": tscpp.Language,
	".cs":   tscsharp.Language,
	".java": tsjava.Language,
	".js":   tsjs.Language, ".jsx": tsjs.Language, ".mjs": tsjs.Language, ".cjs": tsjs.Language,
	".ts": tsts.LanguageTypescript, ".mts": tsts.LanguageTypescript, ".cts": tsts.LanguageTypescript,
	".tsx": tsts.LanguageTSX,
	".py":  tspython.Language, ".pyi": tspython.Language,
	".rb": tsruby.Language,
	".rs": tsrust.Language,
}

// Supported reports whether path's language has a grammar.
func Supported(path string) bool {
	_, ok := grammars[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Parse parses src as path's language. The caller closes the tree.
func Parse(ctx context.Context, path string, src []byte) (*sitter.Tree, error) {
	lang, ok := grammars[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return nil, ErrUnsupported
	}
	p := sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(sitter.NewLanguage(lang())); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// Not ParseCtx: in go-tree-sitter v0.25.0 its watcher writes through
	// the parser's cancellation flag, which tree-sitter 0.25 no longer
	// sets, so a context that ends (even after the parse) dereferences nil.
	// The progress callback is the supported way to cancel.
	tree := p.ParseWithOptions(func(i int, _ sitter.Point) []byte {
		if i < len(src) {
			return src[i:]
		}
		return nil
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
	if tree == nil {
		if ctx.Err() != nil {
			return nil, ErrTimeout
		}
		return nil, errors.New("syntax: parse failed")
	}
	return tree, nil
}

// Errors parses src as path's language and returns how many ERROR and
// MISSING nodes the tree has.
func Errors(ctx context.Context, path string, src []byte) (int, error) {
	tree, err := Parse(ctx, path, src)
	if err != nil {
		return 0, err
	}
	defer tree.Close()
	if !tree.RootNode().HasError() {
		return 0, nil
	}
	c := tree.Walk()
	defer c.Close()
	n := 0
	for {
		if node := c.Node(); node.IsError() || node.IsMissing() {
			n++
		}
		if c.GotoFirstChild() {
			continue
		}
		for !c.GotoNextSibling() {
			if !c.GotoParent() {
				return n, nil
			}
		}
	}
}
