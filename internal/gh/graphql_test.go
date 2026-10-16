package gh

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
)

func TestGraphQL(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr error
		anyErr  bool
	}{
		{name: "data", status: 200, body: `{"data":{"viewer":{"login":"octo"}}}`, want: "octo"},
		{name: "rate limited body", status: 200, body: `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, wantErr: ErrRateLimited},
		{name: "not found", status: 200, body: `{"data":null,"errors":[{"type":"NOT_FOUND","message":"no such PR"}]}`, wantErr: ErrNotFound},
		{name: "other error", status: 200, body: `{"errors":[{"type":"FORBIDDEN","message":"no access"}]}`, anyErr: true},
		{name: "http error", status: 502, body: `{"message":"bad gateway"}`, anyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			base := srv.URL + "/"
			c, err := github.NewClient(github.WithURLs(&base, &base))
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Viewer struct{ Login string } `json:"viewer"`
			}
			err = GraphQL(t.Context(), c, "query { viewer { login } }", nil, &out)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.anyErr:
				if err == nil || errors.Is(err, ErrRateLimited) {
					t.Fatalf("err = %v, want a non-rate-limit error", err)
				}
			default:
				if err != nil || out.Viewer.Login != tt.want {
					t.Fatalf("got %q, %v", out.Viewer.Login, err)
				}
			}
		})
	}
}

func TestContextFetch(t *testing.T) {
	var minted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewDecoder(r.Body).Decode(&minted)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_ctx", "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/repos/acme/shared":
			if r.Header.Get("Authorization") != "Bearer ghs_ctx" {
				t.Errorf("repo read with %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"name":"shared","private":true,"visibility":"internal"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	app := newTestApp(t, srv)
	tok, vis, err := app.ContextFetch(t.Context(), 9, "acme/shared")
	if err != nil || tok != "ghs_ctx" || vis != "internal" {
		t.Fatalf("ContextFetch = %q, %q, %v", tok, vis, err)
	}
	if perms, _ := minted["permissions"].(map[string]any); perms["contents"] != "read" || perms["issues"] != "read" || len(perms) != 2 {
		t.Errorf("minted permissions = %v", minted["permissions"])
	}
	if _, _, err := app.ContextFetch(t.Context(), 9, "acme/exemplar-config"); err == nil {
		t.Error("context token minted for the config repo")
	}
}
