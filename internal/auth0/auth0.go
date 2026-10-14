// Package auth0 is the Auth0 source of service tokens (internal/svcauth):
// access tokens for exemplar-api's API, issued by https://<domain>/. What a
// caller may evaluate comes from the token's permissions (Auth0 RBAC's
// `permissions` claim, or the `scope` a machine-to-machine grant carries):
// `evaluate:<owner>/<repo>`, `evaluate:<owner>/*` or `evaluate:*`. The
// caller is "svc:<sub>".
package auth0

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/svcauth"
)

// Source verifies one tenant's tokens for one API.
type Source struct{ *svcauth.Verifier }

var domainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// New returns a Source for an Auth0 domain (tenant.us.auth0.com or a
// custom domain) and API audience.
func New(domain, audience string) (*Source, error) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "/"))
	if !domainRE.MatchString(domain) {
		return nil, fmt.Errorf("auth0: domain %q must be a host name, without https://", domain)
	}
	if audience == "" {
		return nil, errors.New("auth0: an audience is required")
	}
	return &Source{svcauth.NewVerifier("https://"+domain+"/", audience, "https://"+domain+"/.well-known/jwks.json")}, nil
}

// Issuer implements svcauth.Source.
func (s *Source) Issuer() string { return s.Verifier.Issuer }

// Verify implements svcauth.Source.
func (s *Source) Verify(ctx context.Context, token string) (svcauth.Claims, error) {
	var c struct {
		Sub         string   `json:"sub"`
		Scope       string   `json:"scope"`
		Permissions []string `json:"permissions"`
	}
	if err := s.Verifier.Verify(ctx, token, &c); err != nil {
		return svcauth.Claims{}, err
	}
	perms := append(slices.Clone(c.Permissions), strings.Fields(c.Scope)...)
	return svcauth.Claims{Subject: "svc:" + c.Sub, Permissions: perms}, nil
}
