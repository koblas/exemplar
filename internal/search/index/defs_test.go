package index

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/search/mirror"
)

func TestDefiner(t *testing.T) {
	tests := []struct {
		lang, line string
		want       bool
	}{
		{"Go", "func Fetch(ctx context.Context) error {", true},
		{"Go", "func (c *Client) Fetch(ctx context.Context) error {", true},
		{"Go", "func Fetch[T any](x T) {", true},
		{"Go", "type Fetch struct {", true},
		{"Go", "const Fetch = 3", true},
		{"Go", "	return Fetch(ctx)", false},
		{"Go", "	x := Fetch(ctx)", false},
		{"Go", "// Fetch does things", false},
		{"Python", "def Fetch(url):", true},
		{"Python", "    async def Fetch(self, url):", true},
		{"Python", "class Fetch(Base):", true},
		{"Python", "Fetch = make_fetcher()", true},
		{"Python", "    result = Fetch(url)", false},
		{"TypeScript", "export async function Fetch(url: string): Promise<void> {", true},
		{"TypeScript", "export const Fetch = (url: string) => {", true},
		{"TypeScript", "  async Fetch(url: string): Promise<void> {", true},
		{"TypeScript", "interface Fetch {", true},
		{"TypeScript", "  await Fetch(url);", false},
		{"TypeScript", "  return Fetch(url) {", false},
		{"Java", "public class Fetch {", true},
		{"Java", "  public static List<String> Fetch(String url) {", true},
		{"Java", "    return Fetch(url);", false},
		{"Java", "    Foo x = new Fetch(url);", false},
		{"Kotlin", "fun <T> Client.Fetch(url: String): T {", true},
		{"Kotlin", "object Fetch {", true},
		{"C", "int Fetch(const char *url)", true},
		{"C", "#define Fetch(x) do_fetch(x)", true},
		{"C", "struct Fetch {", true},
		{"C", "} Fetch;", true},
		{"C", "    Fetch(url);", false},
		{"C++", "template <typename T> class Fetch {", true},
		{"C++", "std::string Client::Fetch(const std::string& url) {", true},
		{"C++", "namespace Fetch {", true},
	}
	d := newDefiner("Fetch")
	for _, tt := range tests {
		if got := d.match(tt.lang, tt.line); got != tt.want {
			t.Errorf("%s %q = %v, want %v", tt.lang, tt.line, got, tt.want)
		}
	}
	// Names are quoted: a regexp metacharacter in a name is literal.
	if newDefiner("$").match("TypeScript", "const a = 1") {
		t.Error("name used as a regexp")
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string]string{
		"Fetch": "Fetch|", "net.Fetch": "Fetch|net", "a.b.C": "C|a.b", "std::string": "string|std", "Cart#total": "total|Cart",
	} {
		base, qual, err := splitName(in)
		if err != nil || base+"|"+strings.Join(qual, ".") != want {
			t.Errorf("splitName(%q) = %q %v, %v", in, base, qual, err)
		}
	}
	for _, bad := range []string{"", "a b", "a..b", "1abc", "a(b)", "a.*", strings.Repeat("a", 201)} {
		if _, _, err := splitName(bad); !errors.Is(err, mirror.ErrBadRequest) {
			t.Errorf("splitName(%q) accepted", bad)
		}
	}
}

func TestRank(t *testing.T) {
	defs := func() []Definition {
		return []Definition{
			{Path: "lib/net/fetch.go", Line: 3},
			{Path: "svc/pay/fetch.go", Line: 9},
			{Path: "svc/cart/client.go", Line: 4},
			{Path: "x.go", Line: 1},
		}
	}
	order := func(ds []Definition) string {
		out := make([]string, 0, len(ds))
		for _, d := range ds {
			out = append(out, d.Path)
		}
		return strings.Join(out, ",")
	}
	d := defs()
	rank(d, nil, "svc/pay/charge.go")
	if got := order(d); got != "svc/pay/fetch.go,svc/cart/client.go,x.go,lib/net/fetch.go" {
		t.Errorf("proximity = %s", got)
	}
	d = defs()
	rank(d, []string{"net"}, "svc/pay/charge.go")
	if got := order(d); !strings.HasPrefix(got, "lib/net/fetch.go,") {
		t.Errorf("qualified = %s", got)
	}
	d = defs()
	d[2].Parent = "Cart"
	rank(d, []string{"Cart"}, "")
	if got := order(d); !strings.HasPrefix(got, "svc/cart/client.go,") {
		t.Errorf("parent-qualified = %s", got)
	}
}

func defFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	f.u.write("defs", map[string]string{
		"svc/pay/fetch.go":     "package pay\n\nfunc Fetch() {}\n\nfunc use() { Fetch() }\n",
		"lib/net/fetch.go":     "package net\n\nfunc Fetch() {}\n",
		"gen/fetch.pb.go":      "package gen\n\nfunc Fetch() {}\n", // linguist-generated: excluded
		"web/fetch.ts":         "export function Fetch(): void {}\n",
		"svc/pay/fetch_doc.md": "func Fetch() in prose\n",
	})
	if _, err := f.m.Fetch(t.Context(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	return f
}

func paths2(r DefResult) string {
	out := make([]string, 0, len(r.Definitions))
	for _, d := range r.Definitions {
		out = append(out, fmt.Sprintf("%s:%d", d.Path, d.Line))
	}
	return strings.Join(out, ",")
}

func checkDefs(t *testing.T, f *fixture, wantDegraded bool) {
	t.Helper()
	ctx := t.Context()
	res, err := f.ix.FindDefinition(ctx, "acme/app", DefQuery{Name: "Fetch", NearPath: "svc/pay/charge.go"})
	if err != nil {
		t.Fatal(err)
	}
	// Go only (near_path is Go), the call site and the generated file left
	// out, nearest first (svc/fetch.go comes from the base fixture).
	if got := paths2(res); got != "svc/pay/fetch.go:3,svc/fetch.go:3,lib/net/fetch.go:3" || res.Degraded != wantDegraded {
		t.Errorf("Fetch near svc/pay = %s (degraded %v, symbols %v)", got, res.Degraded, res.Symbols)
	}
	res, _ = f.ix.FindDefinition(ctx, "acme/app", DefQuery{Name: "net.Fetch", NearPath: "svc/pay/charge.go"})
	if got := paths2(res); !strings.HasPrefix(got, "lib/net/fetch.go:3,") {
		t.Errorf("net.Fetch = %s", got)
	}
	res, _ = f.ix.FindDefinition(ctx, "acme/app", DefQuery{Name: "Fetch", NearPath: "web/app.ts"})
	if got := paths2(res); got != "web/fetch.ts:1" {
		t.Errorf("Fetch near a .ts file = %s", got)
	}
	if res, _ := f.ix.FindDefinition(ctx, "acme/app", DefQuery{Name: "Nope"}); len(res.Definitions) != 0 {
		t.Errorf("unknown name = %s", paths2(res))
	}
	if _, err := f.ix.FindDefinition(ctx, "acme/app", DefQuery{Name: "Fetch", NearPath: "../x.go"}); !errors.Is(err, mirror.ErrBadRequest) {
		t.Errorf("bad near_path err = %v", err)
	}
}

func TestFindDefinitionDegraded(t *testing.T) {
	f := defFixture(t)
	checkDefs(t, f, true) // no generation: git grep + regexes
}

func TestFindDefinitionIndexed(t *testing.T) {
	f := defFixture(t)
	if _, err := f.ix.Build(t.Context(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	// With universal-ctags installed this exercises symbols; without it,
	// the index answers with regexes and says so.
	checkDefs(t, f, f.cfg.CTags == "")
	res, _ := f.ix.FindDefinition(t.Context(), "acme/app", DefQuery{Name: "Fetch"})
	if res.Symbols != (f.cfg.CTags != "") {
		t.Errorf("symbols = %v with ctags %q", res.Symbols, f.cfg.CTags)
	}
	if f.cfg.CTags == "" {
		if os.Getenv("EXEMPLAR_REQUIRE_ENGINES") == "1" {
			t.Fatal("universal-ctags is required (EXEMPLAR_REQUIRE_ENGINES=1)")
		}
		t.Log("universal-ctags not on PATH: symbol queries untested")
	}
}
