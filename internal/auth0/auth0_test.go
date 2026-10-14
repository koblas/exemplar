package auth0

import (
	"context"
	"testing"

	"github.com/koblas/exemplar/internal/svcauth"
)

func TestNew(t *testing.T) {
	s, err := New("Acme.us.auth0.com/", "https://exemplar.example/api")
	if err != nil || s.Issuer() != "https://acme.us.auth0.com/" || s.JWKSURL != "https://acme.us.auth0.com/.well-known/jwks.json" {
		t.Fatalf("New = %+v, %v", s, err)
	}
	for _, d := range []string{"https://acme.auth0.com", "acme", "acme.auth0.com/path"} {
		if _, err := New(d, "aud"); err == nil {
			t.Errorf("domain %q accepted", d)
		}
	}
	if _, err := New("acme.auth0.com", ""); err == nil {
		t.Error("no audience accepted")
	}
	var _ svcauth.Source = s
	if _, err := s.Verify(context.Background(), "not.a.jwt"); err == nil {
		t.Error("garbage verified")
	}
}
