package exclude

import "testing"

func TestReason(t *testing.T) {
	r := Load(
		"# generated code\n*.pb.go linguist-generated\nthird_party/** linguist-vendored=true\ndocs/** -diff\n",
		"# not worth reviewing\nfixtures/\n/legacy/*.go\n!keep.go\n",
	)
	tests := map[string]string{
		"go.sum":                "lockfile",
		"web/package-lock.json": "lockfile",
		"static/app.min.js":     "minified",
		"api/v1/user.pb.go":     "linguist-generated",
		"third_party/x/y.go":    "linguist-vendored",
		"testdata/fixtures/a":   ".exemplarignore",
		"legacy/old.go":         ".exemplarignore",
		"sub/legacy/old.go":     "", // anchored pattern
		"docs/readme.md":        "", // attributes other than linguist-* do not exclude
		"svc/main.go":           "",
	}
	for p, want := range tests {
		if got := r.Reason(p); got != want {
			t.Errorf("Reason(%q) = %q, want %q", p, got, want)
		}
	}
	if Load("", "").Reason("svc/main.go") != "" {
		t.Error("empty rules exclude a normal file")
	}
}
