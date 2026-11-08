package webassets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	h := Handler()
	cases := []struct {
		path   string
		status int
	}{
		{Bulma, http.StatusOK},
		{App, http.StatusOK},
		{Prefix + "README.md", http.StatusNotFound},
		{Prefix + "nope.css", http.StatusNotFound},
		{Prefix + "x/app.css", http.StatusNotFound},
		{"/elsewhere/app.css", http.StatusNotFound},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("GET %s = %d, want %d", tc.path, w.Code, tc.status)
			continue
		}
		if tc.status != http.StatusOK {
			continue
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", tc.path, ct)
		}
		// The ETag lets a browser revalidate instead of downloading again.
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
		r.Header.Set("If-None-Match", w.Header().Get("ETag"))
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r)
		if w2.Code != http.StatusNotModified {
			t.Errorf("GET %s with its ETag = %d, want 304", tc.path, w2.Code)
		}
	}
}

// The vendored file is the Bulma release its name says.
func TestBulmaVersion(t *testing.T) {
	a := assets[strings.TrimPrefix(Bulma, Prefix)]
	if !strings.Contains(string(a.body[:200]), "bulma.io v1.0.4 ") {
		t.Errorf("%s doesn't carry the v1.0.4 banner", Bulma)
	}
}
