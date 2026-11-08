package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/webassets"
)

// The CSP allows only same-origin stylesheets, so an inline style would be
// dropped without a word: the templates carry none.
func TestNoInlineStyles(t *testing.T) {
	for _, tmpl := range pages.Templates() {
		if tmpl.Tree == nil {
			continue
		}
		if src := tmpl.Tree.Root.String(); strings.Contains(src, "style=") || strings.Contains(src, "<style") {
			t.Errorf("template %q has an inline style", tmpl.Name())
		}
	}
}

// Every page links the embedded stylesheets exemplar-api serves at
// webassets.Prefix, and marks the open tab for assistive technology.
func TestPageStylesheets(t *testing.T) {
	var b strings.Builder
	if err := pages.ExecuteTemplate(&b, "notfound", map[string]any{"Title": "Not found", "Nav": "cost", "Login": "alice"}); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, 2)
	for _, m := range regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`).FindAllStringSubmatch(b.String(), -1) {
		got = append(got, m[1])
	}
	if want := []string{webassets.Bulma, webassets.App}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("stylesheets = %v, want %v", got, want)
	}
	if !strings.Contains(b.String(), `<li class="is-active"><a href="/ui/cost" aria-current="page">Cost</a></li>`) || strings.Count(b.String(), `aria-current="page"`) != 1 {
		t.Errorf("the Cost tab isn't the one marked current:\n%s", b.String())
	}
}
