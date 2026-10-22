package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

const goodFix = "\treq, err := http.NewRequestWithContext(ctx, http.MethodGet, \"https://inventory.internal/items\", nil)"

func TestCheckFix(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	eng, _ := newEngine(e, Script{})
	policy := map[string]string{"propagate-context": schema.FixSuggest, "other-rule": schema.FixDescribe}
	tests := []struct {
		name   string
		rule   string
		lane   string
		anchor scope.Anchor
		fix    string
		want   string
	}{
		{name: "valid", fix: goodFix},
		{name: "valid with a final newline", fix: goodFix + "\n"},
		{
			name: "valid two-line range", anchor: scope.Anchor{Path: "main.go", Side: scope.SideRight, StartLine: 13, EndLine: 14},
			fix: "\tdefer func() { _ = resp.Body.Close() }()\n\t_ = ctx",
		},
		{
			name: "breaks the block", anchor: scope.Anchor{Path: "main.go", Side: scope.SideRight, StartLine: 9, EndLine: 10},
			fix: goodFix + "\n\tresp, err := http.DefaultClient.Do(req)", want: FixParse,
		},
		{name: "bug classes may fix", rule: "bug.nil-deref", lane: LaneBug, fix: goodFix},
		{name: "rule describes only", rule: "other-rule", fix: goodFix, want: FixNotAllowed},
		{name: "removed lines", anchor: scope.Anchor{Path: "main.go", Side: scope.SideLeft, StartLine: 9, EndLine: 9}, fix: goodFix, want: FixLeftSide},
		{name: "over 30 lines", fix: strings.Repeat("\t_ = 1\n", 31), want: FixTooLong},
		{name: "code fence", fix: "\t// ```\n" + goodFix, want: FixFence},
		{name: "redacted placeholder", fix: "\ttoken := \"" + redact.Placeholder + "\"", want: FixRedacted},
		{name: "a secret", fix: "\ttoken := \"" + fakeToken() + "\"", want: FixSecret},
		{name: "unchanged", fix: "\tresp, err := http.Get(\"https://inventory.internal/items\")", want: FixUnchanged},
		{name: "repeats the line above", fix: "func handler(ctx context.Context, w http.ResponseWriter, r *http.Request) {\n" + goodFix, want: FixOutside},
		{name: "repeats the line below", fix: goodFix + "\n\tif err != nil {", want: FixOutside},
		{name: "parse error", fix: "\tresp, err := http.Get(", want: FixParse},
		{name: "not gofmt-formatted", fix: "    resp, err := http.Get(\"https://inventory.internal/items\")  ", want: FixFormat},
		{name: "past the end of the file", anchor: scope.Anchor{Path: "main.go", Side: scope.SideRight, StartLine: 99, EndLine: 99}, fix: goodFix, want: FixBadRange},
		{name: "missing file", anchor: scope.Anchor{Path: "nope.go", Side: scope.SideRight, StartLine: 1, EndLine: 1}, fix: goodFix, want: FixNoFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := Finding{RuleID: cmpOrStr(tt.rule, "propagate-context"), Lane: cmpOrStr(tt.lane, LaneExemplar), Anchor: tt.anchor, Fix: tt.fix}
			if f.Anchor.Path == "" {
				f.Anchor = anchorAt("main.go", httpGetLine)
			}
			got := eng.checkFix(t.Context(), e.prep, &f, policy)
			if got != tt.want || f.FixValid == nil || *f.FixValid != (tt.want == "") {
				t.Errorf("checkFix = %q (valid %v), want %q", got, f.FixValid, tt.want)
			}
		})
	}
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestCheckSyntaxOtherLanguages(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	eng, _ := newEngine(e, Script{})
	tests := []struct {
		path, before, after, want string
	}{
		{"svc/a.py", "def f():\n    return 1\n", "def f():\n    return 2\n", ""},
		{"svc/a.py", "def f():\n    return 1\n", "def f():\n    return g(2\n", FixParse},
		// An error already in the file is not the fix's.
		{"svc/a.py", "x = = 1\ndef f():\n    return 1\n", "x = = 1\ndef f():\n    return 2\n", ""},
		{"src/a.c", "int main(void) { return 0; }\n", "int main(void) { return 1; }\n", ""},
		{"src/a.c", "int main(void) { return 0; }\n", "int main(void) { return 1 }\n", FixParse},
		{"web/a.ts", "const x: number = 1;\n", "const x: number = (1;\n", FixParse},
		{"web/a.ts", "const x: number = 1;\n", "const x: number = 2;\n", ""},
		{"lib/a.zig", "const x = 1;\n", "const x = 2;\n", FixNoParser},
	}
	for _, tt := range tests {
		if got := eng.checkSyntax(t.Context(), tt.path, []byte(tt.before), []byte(tt.after)); got != tt.want {
			t.Errorf("%s %q -> %q = %q, want %q", tt.path, tt.before, tt.after, got, tt.want)
		}
	}
	// A Go file that did not parse before: the fix is not blamed.
	if got := eng.checkSyntax(t.Context(), "a.go", []byte("package a\nfunc f( {\n"), []byte("package a\nfunc g( {\n")); got != "" {
		t.Errorf("already broken = %q", got)
	}
}

func TestReviewPostsValidFix(t *testing.T) {
	t.Parallel()
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	for i := range e.prep.Effective.Rules {
		if e.prep.Effective.Rules[i].ID == "propagate-context" {
			e.prep.Effective.Rules[i].FixPolicy = schema.FixSuggest
		}
	}
	fix := strings.ReplaceAll(goodFix, `"`, `\"`)
	fix = strings.ReplaceAll(fix, "\t", `\t`)
	eng, tr := newEngine(e, Script{
		Generator: contextViolation(`, "suggested_fix": "` + fix + `"`),
		Quote:     func(path string, line int) string { return headLineNoT(e.prep, path, line) },
	})
	res, err := eng.Review(t.Context(), e.prep, Options{Caller: "eval"})
	if err != nil {
		t.Fatal(err)
	}
	fs := findByRule(res.Findings, "propagate-context")
	if len(fs) != 1 || fs[0].State != StatePosted || fs[0].FixValid == nil || !*fs[0].FixValid || SuggestionText(fs[0]) != goodFix {
		t.Fatalf("finding = %+v", fs)
	}
	// The agent is told fixes are allowed.
	allowed := false
	for _, c := range tr.Calls {
		allowed = allowed || strings.Contains(string(c.Body), "Fixes: allowed")
	}
	if !allowed {
		t.Error("the rule task does not allow fixes")
	}
}

func TestCheckFormatClang(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("clang-format"); err != nil {
		if os.Getenv("EXEMPLAR_REQUIRE_ENGINES") == "1" {
			t.Fatal("clang-format is required (EXEMPLAR_REQUIRE_ENGINES=1)")
		}
		t.Skip("clang-format not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".clang-format"), []byte("BasedOnStyle: LLVM\nIndentWidth: 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := setup(t, "Fetch inventory", "Adds a fetch.")
	eng, _ := newEngine(e, Script{})
	clean := "int main(void) {\n    int x = f();\n    return x;\n}\n"
	tests := []struct{ before, after, want string }{
		{clean, "int main(void) {\n    int x = g();\n    return x;\n}\n", ""},
		{clean, "int main(void){int x=g();return x;}\n", FixFormat},
		// The repo's style (IndentWidth 4) is the one held to.
		{clean, "int main(void) {\n  int x = g();\n  return x;\n}\n", FixFormat},
		// An unformatted original holds the fix to nothing.
		{"int main(void){return 0;}\n", "int main(void){return 1;}\n", ""},
	}
	for _, tt := range tests {
		if got := eng.checkFormat(t.Context(), dir, "src/a.c", []byte(tt.before), []byte(tt.after)); got != tt.want {
			t.Errorf("%q -> %q = %q, want %q", tt.before, tt.after, got, tt.want)
		}
	}
	// No formatter for the language: no check.
	if got := eng.checkFormat(t.Context(), dir, "a.rb", []byte("x\n"), []byte("y  \n")); got != "" {
		t.Errorf("ruby = %q", got)
	}
}
