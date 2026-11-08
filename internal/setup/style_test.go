package setup

import (
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/webassets"
)

var stylesheetRe = regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`)

// The CSP allows only same-origin stylesheets, so an inline style would be
// dropped without a word: the templates carry none.
func TestNoInlineStyles(t *testing.T) {
	err := fs.WalkDir(pageFiles, "pages", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(pageFiles, p)
		if err != nil {
			return err
		}
		if s := string(b); strings.Contains(s, "style=") || strings.Contains(s, "<style") {
			t.Errorf("%s has an inline style", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The sign-in page loads Bulma and the overrides before anyone has signed
// in, as CSS (nosniff drops any other type), and the CSP allows them.
func TestStylesheetsBeforeSignIn(t *testing.T) {
	srv, _ := newSetup(t)
	c := anonymous(t)
	p := get(t, c, srv.URL+"/setup")
	if p.status != http.StatusUnauthorized {
		t.Fatalf("GET /setup = %d", p.status)
	}
	if csp := p.header.Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self'") {
		t.Errorf("CSP %q doesn't allow the stylesheets", csp)
	}
	got := make([]string, 0, 2)
	for _, m := range stylesheetRe.FindAllStringSubmatch(p.body, -1) {
		got = append(got, m[1])
	}
	if want := []string{webassets.Bulma, webassets.App}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("stylesheets = %v, want %v", got, want)
	}
	for _, href := range got {
		css := get(t, c, srv.URL+href)
		if css.status != http.StatusOK || css.header.Get("Content-Type") != "text/css; charset=utf-8" || css.body == "" {
			t.Errorf("GET %s = %d %q, %d bytes", href, css.status, css.header.Get("Content-Type"), len(css.body))
		}
		if cc := css.header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q", href, cc)
		}
	}
}
