package index

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/search/mirror"
)

// refsRepo is a small multi-language repo with known importers.
var refsRepo = map[string]string{
	"go.mod":         "module acme/app\n\ngo 1.27\n",
	"svc/fetch.go":   "package svc\n\nfunc Fetch() error { return nil }\n",
	"svc/retry.go":   "package svc\n\nfunc Retry() error {\n\treturn Fetch()\n}\n",
	"svc/sub/x.go":   "package sub\n\n// Fetch is mentioned in a subpackage that does not import svc.\nvar Fetch = 1\n",
	"api/handler.go": "package api\n\nimport (\n\t\"acme/app/svc\"\n)\n\nfunc Handle() error {\n\treturn svc.Fetch()\n}\n",
	"api/alias.go":   "package api\n\nimport s \"acme/app/svc\"\n\nvar _ = s.Fetch\n",
	"other/fetch.go": "package other\n\n// Fetch here is unrelated: other does not import svc.\nfunc Fetch() {}\n",
	"cmd/main.go":    "package main\n\nimport \"acme/app/api\"\n\nfunc main() { _ = api.Handle() }\n",

	"web/avatar.ts":    "export function load(url: string) { return url }\n",
	"web/app.ts":       "import { load } from './avatar'\nload('a')\n",
	"web/lib/index.ts": "export const helper = 1\n",
	"admin/page.tsx":   "import { load } from \"../web/avatar\";\nimport { helper } from '@acme/web/lib'\nload('b')\n",
	"admin/other.ts":   "import { load } from './avatar'\nload('c')\n", // a different ./avatar

	"src/net/avatar.py": "def fetch(url):\n    return url\n",
	"src/app.py":        "from src.net.avatar import fetch\nfetch('a')\n",
	"src/b.py":          "from net import avatar\navatar.fetch('b')\n",
	"src/c.py":          "import json\nfetch = 1\n",

	"java/com/acme/Cart.java":  "package com.acme;\n\npublic class Cart {\n  public int total() { return 0; }\n}\n",
	"java/com/shop/Till.java":  "package com.shop;\n\nimport com.acme.Cart;\n\nclass Till { int t(Cart c) { return c.total(); } }\n",
	"java/com/shop/Other.java": "package com.shop;\n\nimport com.acme.*;\n",

	"include/net/avatar.h": "int avatar_fetch(const char *url);\n",
	"lib/avatar.cc":        "#include \"net/avatar.h\"\nint avatar_fetch(const char *url) { return 0; }\n",
	"tools/use.cc":         "#include <net/avatar.h>\nint main() { return avatar_fetch(\"x\"); }\n",
	"tools/no.cc":          "#include <other/avatar.h>\n",

	"docs/notes.txt": "Fetch is documented here.\n",
}

func refsFixture(t *testing.T, indexed bool) *fixture {
	t.Helper()
	f := setup(t)
	f.u.write("refs", refsRepo)
	sha, err := f.m.Fetch(t.Context(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if indexed {
		if _, err := f.ix.Build(t.Context(), "acme/app"); err != nil {
			t.Fatal(err)
		}
		f.ix.NoteMirror("acme/app", sha)
	}
	return f
}

func TestDependents(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		t.Run(fmt.Sprintf("indexed=%v", indexed), func(t *testing.T) {
			f := refsFixture(t, indexed)
			tests := []struct {
				file  string
				depth int
				roots []string
				files []string
			}{
				{"svc/fetch.go", 1, nil, []string{"api/alias.go", "api/handler.go"}},
				{"svc/fetch.go", 2, nil, []string{"api/alias.go", "api/handler.go", "cmd/main.go"}},
				{"web/avatar.ts", 1, nil, []string{"admin/page.tsx", "web/app.ts"}},
				{"web/lib/index.ts", 1, nil, []string{"admin/page.tsx"}},
				{"src/net/avatar.py", 1, nil, []string{"src/app.py", "src/b.py"}},
				{"java/com/acme/Cart.java", 1, nil, []string{"java/com/shop/Other.java", "java/com/shop/Till.java"}},
				{"include/net/avatar.h", 1, []string{"include"}, []string{"lib/avatar.cc", "tools/use.cc"}},
			}
			for _, tt := range tests {
				res, err := f.ix.Dependents(t.Context(), "acme/app", tt.file, tt.depth, tt.roots)
				if err != nil {
					t.Fatalf("%s: %v", tt.file, err)
				}
				if !slices.Equal(res.Files, tt.files) || res.Degraded == indexed || res.Proximity {
					t.Errorf("%s depth %d: %+v", tt.file, tt.depth, res)
				}
			}
			res, _ := f.ix.Dependents(t.Context(), "acme/app", "docs/notes.txt", 1, nil)
			if !res.Proximity || len(res.Files) != 0 {
				t.Errorf("no import rules: %+v", res)
			}
		})
	}
}

func refSites(r RefResult) []string {
	out := make([]string, 0, len(r.References))
	for _, x := range r.References {
		out = append(out, fmt.Sprintf("%s:%d", x.Path, x.Line))
	}
	return out
}

func TestFindReferences(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		t.Run(fmt.Sprintf("indexed=%v", indexed), func(t *testing.T) {
			f := refsFixture(t, indexed)
			res, err := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "svc.Fetch", DefinedIn: "svc/fetch.go"})
			if err != nil {
				t.Fatal(err)
			}
			// The defining package first, then importers; the unrelated
			// other/ and svc/sub/ mentions and docs are out of scope.
			want := []string{"svc/fetch.go:3", "svc/retry.go:4", "api/alias.go:5", "api/handler.go:8"}
			if !slices.Equal(refSites(res), want) || res.Degraded == indexed || res.Packages != 1 {
				t.Fatalf("references = %v (%+v)", refSites(res), res)
			}
			r := res.References[1]
			if r.ContextStart != 2 || !strings.Contains(r.Context, "func Retry() error {") || !strings.Contains(r.Context, "}") {
				t.Errorf("context = %+v", r)
			}

			// No import rules: the defining directory's subtree.
			if res, _ := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "Fetch", DefinedIn: "docs/notes.txt"}); !res.Proximity || !slices.Equal(refSites(res), []string{"docs/notes.txt:1"}) {
				t.Errorf("proximity = %v %+v", refSites(res), res)
			}
			// A rare name needs no defined_in.
			if res, err := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "avatar_fetch"}); err != nil || len(res.References) != 3 {
				t.Errorf("repo-wide = %v, %v", refSites(res), err)
			}
			if _, err := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "a b"}); !errors.Is(err, mirror.ErrBadRequest) {
				t.Errorf("bad name err = %v", err)
			}
			if _, err := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "x", DefinedIn: "../etc/passwd"}); !errors.Is(err, mirror.ErrBadRequest) {
				t.Errorf("bad path err = %v", err)
			}
		})
	}
}

func TestFindReferencesNeedsDefinedIn(t *testing.T) {
	f := setup(t)
	files := map[string]string{}
	for i := range 12 {
		files[fmt.Sprintf("many/f%02d.go", i)] = "package many\n\n" + strings.Repeat("var _ = common\n", 45)
	}
	f.u.write("many", files)
	if _, err := f.m.Fetch(t.Context(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "common"}); !errors.Is(err, mirror.ErrBadRequest) ||
		!strings.Contains(err.Error(), "pass defined_in") {
		t.Fatalf("540 hits without defined_in: %v", err)
	}
	// Degraded mode caps at 25.
	res, err := f.ix.FindReferences(t.Context(), "acme/app", RefQuery{Name: "common", DefinedIn: "many/f00.go"})
	if err != nil || len(res.References) != MaxReferencesDegraded || !res.Truncated || !res.Degraded {
		t.Fatalf("degraded cap = %d %+v, %v", len(res.References), res.Truncated, err)
	}
}
