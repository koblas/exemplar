package syntax

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestErrors(t *testing.T) {
	tests := []struct {
		path, good, bad string
	}{
		// The bad cases include missing tokens (unclosed paren, dropped
		// semicolon), which tree-sitter recovers from with MISSING nodes.
		{"a.c", "int main(void) { return 0; }\n", "int main(void { return 0; }\n"},
		{"a.c", "int main(void) { return 0; }\n", "int main(void) { return 0 }\n"},
		{"a.h", "class A { public: int f() const; };\n", "class A { public: int f() const };\n"},
		{"a.cpp", "#include <vector>\nint f(std::vector<int>& v) { return v.size(); }\n", "int f(std::vector<int>& v) { return v.size(; }\n"},
		{"A.cs", "class A { int F() { return 1; } }\n", "class A { int F() { return 1 } }\n"},
		{"A.java", "class A { int f() { return 1; } }\n", "class A { int f() { return 1; }\n"},
		{"a.js", "const x = f(1);\n", "const x = f(1;\n"},
		{"a.jsx", "const x = <div>{y}</div>;\n", "const x = <div>{y</div>;\n"},
		{"a.ts", "const x: number = f(1);\n", "const x: number = f(1;\n"},
		{"a.tsx", "const x = <A b={1} />;\n", "const x = <A b={1 />;\n"},
		{"a.py", "def f():\n    return 1\n", "def f(:\n    return 1\n"},
		{"a.py", "def f():\n    return g(1)\n", "def f():\n    return g(1\n"},
		{"a.rb", "def f\n  1\nend\n", "def f\n  g(1\nend\n"},
		{"a.rs", "fn f() -> i32 { 1 }\n", "fn f() -> i32 { g(1 }\n"},
	}
	for _, tt := range tests {
		good, err := Errors(t.Context(), tt.path, []byte(tt.good))
		if err != nil || good != 0 {
			t.Errorf("%s good = %d, %v", tt.path, good, err)
		}
		bad, err := Errors(t.Context(), tt.path, []byte(tt.bad))
		if err != nil || bad == 0 {
			t.Errorf("%s bad %q = %d, %v", tt.path, tt.bad, bad, err)
		}
	}
	if _, err := Errors(t.Context(), "a.zig", []byte("x")); !errors.Is(err, ErrUnsupported) || Supported("a.zig") || !Supported("A.CPP") {
		t.Errorf("unsupported = %v", err)
	}
}

// A context that ends stops the parse, and never crashes (go-tree-sitter's
// ParseCtx dereferences a nil cancellation flag; Errors does not use it).
func TestErrorsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	big := []byte(strings.Repeat("def f():\n    return g(1)\n", 20000))
	if _, err := Errors(ctx, "a.py", big); !errors.Is(err, ErrTimeout) {
		t.Errorf("cancelled = %v", err)
	}
	// Many parses whose contexts end right after: the old crash.
	for range 50 {
		ctx, cancel := context.WithCancel(t.Context())
		if _, err := Errors(ctx, "a.py", []byte("x = 1\n")); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
}
