package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/setup"
)

// noStore is a Store for pages that read none.
type noStore struct{ Store }

func TestSetupPage(t *testing.T) {
	signedIn := true
	h, err := New(Config{
		Store:         noStore{},
		Session:       func(*http.Request) (string, Access, bool) { return "alice", nil, signedIn },
		SignOut:       func(http.ResponseWriter) {},
		Act:           func(context.Context, string, string, string, string) (int64, error) { return 0, nil },
		FalsePositive: func(context.Context, int64, string, string) error { return nil },
		Try:           func(context.Context, string, string, int, string, string) (int64, error) { return 0, nil },
		FormKey:       []byte(strings.Repeat("k", 32)),
		Checks: func(context.Context) []setup.Check {
			return []setup.Check{
				{Name: "GitHub App installed on acme", Status: setup.StatusOK, Detail: "Installation 7."},
				{Name: "GitHub delivers webhooks", Status: setup.StatusFail, Detail: "got HTTP 502.", Fix: "Check that the tunnel routes /webhook."},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		c := srv.Client()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := get("/ui/setup")
	if code != http.StatusOK {
		t.Fatalf("GET /ui/setup = %d", code)
	}
	for _, want := range []string{"GitHub App installed on acme", "Installation 7.", "fail", "Check that the tunnel routes /webhook.", `href="/ui/setup" class="on"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
	code, body = get("/ui/setup.json")
	var got struct {
		Checks []setup.Check `json:"checks"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || len(got.Checks) != 2 || got.Checks[1].Status != setup.StatusFail {
		t.Fatalf("GET /ui/setup.json = %d %s", code, body)
	}

	signedIn = false
	if code, _ := get("/ui/setup"); code != http.StatusFound {
		t.Errorf("signed out /ui/setup = %d, want a sign-in redirect", code)
	}
	if code, _ := get("/ui/setup.json"); code != http.StatusUnauthorized {
		t.Errorf("signed out /ui/setup.json = %d", code)
	}
}
