package gh

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/c0de/conversions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("the conversion is unauthenticated")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 42, "slug": "exemplar-acme", "html_url": "https://github.com/apps/exemplar-acme",
			"client_id": "Iv23abc", "client_secret": "cs", "webhook_secret": "ws", "pem": "-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----\n"}`))
	}))
	defer srv.Close()
	got, err := CompleteManifest(t.Context(), srv.URL, srv.Client(), "c0de")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.Slug != "exemplar-acme" || got.ClientID != "Iv23abc" || got.ClientSecret != "cs" ||
		got.WebhookSecret != "ws" || !strings.HasPrefix(got.PEM, "-----BEGIN") || got.HTMLURL == "" {
		t.Fatalf("CompleteManifest = %+v", got)
	}
	if _, err := CompleteManifest(t.Context(), srv.URL, srv.Client(), "used"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code = %v, want ErrNotFound", err)
	}
}

func TestOrgInstallation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 5, "account": {"login": "someone"}}, {"id": 9, "account": {"login": "Acme"}}]`))
	}))
	defer srv.Close()
	app := newTestApp(t, srv)
	if id, err := app.OrgInstallation(t.Context(), "acme"); err != nil || id != 9 {
		t.Fatalf("OrgInstallation = %d, %v", id, err)
	}
	if _, err := app.OrgInstallation(t.Context(), "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not installed = %v, want ErrNotFound", err)
	}
}
