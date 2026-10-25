package ghuser

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

// handlerTransport serves requests with h in the calling goroutine, so
// no connection goroutine keeps a synctest bubble's clock from moving.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

// TestChecker runs on 2026-10-14, in a synctest bubble slept to it.
func TestChecker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)))
		testChecker(t)
	})
}

func testChecker(t *testing.T) {
	t.Helper()
	calls := map[string]int{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
	c := New("http://github.test/", &http.Client{Transport: handlerTransport{h}})
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
	time.Sleep(TTL + time.Second)
	_, _ = c.CanRead(ctx, "u", "acme/app")
	if calls["Bearer u /repos/acme/app"] != 2 {
		t.Errorf("expired entry not refreshed: %v", calls)
	}
}
