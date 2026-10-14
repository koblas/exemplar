package ghuser

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChecker(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Header.Get("Authorization")+" "+r.URL.Path]++
		switch r.Header.Get("Authorization") + " " + r.URL.Path {
		case "Bearer u /user":
			_, _ = w.Write([]byte(`{"login": "alice", "type": "User"}`))
		case "Bearer b /user":
			_, _ = w.Write([]byte(`{"login": "ci[bot]", "type": "Bot"}`))
		case "Bearer u /repos/acme/app":
			_, _ = w.Write([]byte(`{"permissions": {"pull": true}}`))
		case "Bearer u /repos/acme/down":
			w.WriteHeader(http.StatusBadGateway)
		case "Bearer x /user":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/", srv.Client())
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	ctx := t.Context()

	if l, err := c.Login(ctx, "u"); err != nil || l != "alice" {
		t.Fatalf("login = %q, %v", l, err)
	}
	if _, err := c.Login(ctx, "b"); !errors.Is(err, ErrRefused) {
		t.Errorf("bot = %v", err)
	}
	if _, err := c.Login(ctx, "x"); !errors.Is(err, ErrRefused) {
		t.Errorf("bad token = %v", err)
	}
	for repo, want := range map[string]bool{"acme/app": true, "acme/secret": false, "acme/../x?y": false, "nope": false} {
		if ok, err := c.CanRead(ctx, "u", repo); err != nil || ok != want {
			t.Errorf("%s = %v, %v", repo, ok, err)
		}
	}
	if _, err := c.CanRead(ctx, "u", "acme/down"); err == nil {
		t.Error("a GitHub failure read as no access")
	}
	// Cached for TTL, then asked again.
	_, _ = c.Login(ctx, "u")
	_, _ = c.CanRead(ctx, "u", "acme/app")
	_, _ = c.CanRead(ctx, "u", "acme/secret")
	if calls["Bearer u /user"] != 1 || calls["Bearer u /repos/acme/app"] != 1 || calls["Bearer u /repos/acme/secret"] != 1 {
		t.Errorf("calls = %v", calls)
	}
	now = now.Add(TTL + time.Second)
	_, _ = c.CanRead(ctx, "u", "acme/app")
	if calls["Bearer u /repos/acme/app"] != 2 {
		t.Errorf("expired entry not refreshed: %v", calls)
	}
}
