package codeowners

import (
	"slices"
	"testing"
)

func TestOwners(t *testing.T) {
	co := Parse(`# owners
*           @acme/platform
*.go        @acme/go-reviewers   # Go
/svc/pay/   @acme/payments @carol
docs/**     docs@acme.example
/svc/pay/generated/
/config/*   @acme/sre
`)
	tests := map[string][]string{
		"README.md":                {"@acme/platform"},
		"lib/x.go":                 {"@acme/go-reviewers"},
		"svc/pay/charge.go":        {"@acme/payments", "@carol"},
		"other/svc/pay/charge.go":  {"@acme/go-reviewers"}, // anchored
		"docs/guide/intro.md":      {"docs@acme.example"},
		"svc/pay/generated/api.go": nil, // last match names no owner
		"config/app.yaml":          {"@acme/sre"},
		"config/prod/app.yaml":     {"@acme/platform"}, // "/*" is direct children only
	}
	for p, want := range tests {
		if got := co.Owners(p); !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Errorf("Owners(%q) = %v, want %v", p, got, want)
		}
	}
	if got := Parse("").Owners("a.go"); got != nil || !Parse("# only a comment").Empty() {
		t.Errorf("empty CODEOWNERS owns %v", got)
	}
}
