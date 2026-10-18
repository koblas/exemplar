package main

import (
	"errors"
	"testing"

	"github.com/koblas/exemplar/internal/envcfg"
)

// The core manifests' placeholders stop startup before anything connects.
func TestRunRejectsPlaceholders(t *testing.T) {
	tests := []struct{ name, key, value string }{
		{"API URL", "EXEMPLAR_API_URL", "https://exemplar.example.invalid"},
		{"client id", "EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID", "Iv1.example"},
		{"allowed orgs", "EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS", "example-org"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EXEMPLAR_DATABASE_URL", "postgres://unused.invalid/db")
			t.Setenv("EXEMPLAR_API_URL", "https://exemplar.acme.com")
			t.Setenv(tc.key, tc.value)
			if err := run(nil); !errors.Is(err, envcfg.ErrPlaceholder) {
				t.Fatalf("run = %v, want ErrPlaceholder", err)
			}
		})
	}
}
