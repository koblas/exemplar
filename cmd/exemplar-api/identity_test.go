package main

import (
	"errors"
	"testing"

	"github.com/koblas/exemplar/internal/api"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

func TestIdentityStore(t *testing.T) {
	ids := identityStore{storetest.New(t)}
	ctx := t.Context()
	if _, ok, err := ids.GitHubLink(ctx, "https://idp", "u1"); err != nil || ok {
		t.Fatalf("before linking = %v, %v", ok, err)
	}
	if err := ids.LinkGitHub(ctx, "https://idp", "u1", "dana@acme.example", 7, "dana"); err != nil {
		t.Fatal(err)
	}
	if login, ok, err := ids.GitHubLink(ctx, "https://idp", "u1"); err != nil || !ok || login != "dana" {
		t.Fatalf("linked = %q %v %v", login, ok, err)
	}
	// Another identity cannot take the account.
	if err := ids.LinkGitHub(ctx, "https://idp", "u2", "eve@acme.example", 7, "dana"); !errors.Is(err, api.ErrLinkedElsewhere) {
		t.Errorf("same account for u2 = %v", err)
	}
	// The same identity may switch accounts, and relink its own.
	if err := ids.LinkGitHub(ctx, "https://idp", "u1", "dana@acme.example", 8, "dana-work"); err != nil {
		t.Fatal(err)
	}
	if login, _, _ := ids.GitHubLink(ctx, "https://idp", "u1"); login != "dana-work" {
		t.Errorf("after switching = %q", login)
	}
	// The old account is free again.
	if err := ids.LinkGitHub(ctx, "https://idp", "u2", "eve@acme.example", 7, "dana"); err != nil {
		t.Errorf("freed account = %v", err)
	}
}
