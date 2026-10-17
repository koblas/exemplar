package gh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
)

func TestHookDeliveriesAndRedeliver(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	at := func(h int) string { return now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }
	var (
		mu         sync.Mutex
		redeliver  []string
		authorized = true
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		// The App's own endpoints authenticate with its JWT.
		authorized = authorized && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/hook/deliveries":
			if r.URL.Query().Get("cursor") == "" {
				w.Header().Set("Link", fmt.Sprintf(`<http://%s/app/hook/deliveries?cursor=p2>; rel="next"`, r.Host))
				fmt.Fprintf(w, `[{"id": 3, "guid": "g2", "status_code": 502, "delivered_at": %q, "event": "pull_request"},
					{"id": 2, "guid": "g1", "status_code": 200, "delivered_at": %q, "event": "pull_request"}]`, at(1), at(2))
				return
			}
			fmt.Fprintf(w, `[{"id": 1, "guid": "g0", "status_code": 0, "delivered_at": %q}, {"id": 0, "guid": "old", "delivered_at": %q}]`, at(3), at(30))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/app/hook/deliveries/"):
			redeliver = append(redeliver, r.URL.Path)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/repos/acme/app/installation":
			_, _ = w.Write([]byte(`{"id": 77}`))
		case r.URL.Path == "/repos/acme/exemplar-config/installation":
			_, _ = w.Write([]byte(`{"id": 78}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
		}
	}))
	defer srv.Close()
	app := newTestApp(t, srv)

	ds, err := app.HookDeliveries(t.Context(), now.Add(-6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	guids := make([]string, 0, len(ds))
	for _, d := range ds {
		guids = append(guids, d.GUID)
	}
	// Two pages, stopping at the first delivery older than the cutoff.
	if !slices.Equal(guids, []string{"g2", "g1", "g0"}) || ds[0].StatusCode != http.StatusBadGateway || ds[0].ID != 3 {
		t.Fatalf("deliveries = %+v", ds)
	}
	if err := app.Redeliver(t.Context(), 3); err != nil || !slices.Equal(redeliver, []string{"/app/hook/deliveries/3/attempts"}) {
		t.Fatalf("redeliver = %v, %v", redeliver, err)
	}
	if id, err := app.RepoInstallation(t.Context(), "acme/app"); err != nil || id != 77 {
		t.Errorf("installation = %d, %v", id, err)
	}
	if id, err := app.ConfigInstallation(t.Context()); err != nil || id != 78 {
		t.Errorf("config installation = %d, %v", id, err)
	}
	if !authorized {
		t.Error("an App endpoint was called without the App JWT")
	}
}

func TestSearchOpenPRs(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = decodeJSON(r, &body)
		queries = append(queries, fmt.Sprint(body.Variables["q"]))
		if body.Variables["after"] == nil {
			_, _ = w.Write([]byte(`{"data": {"search": {"nodes": [
				{"number": 7, "headRefOid": "abc", "isDraft": false, "repository": {"nameWithOwner": "acme/app"}, "headRepository": {"nameWithOwner": "acme/app"}},
				{}],
				"pageInfo": {"hasNextPage": true, "endCursor": "c1"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": {"search": {"nodes": [
			{"number": 8, "headRefOid": "def", "isDraft": true, "repository": {"nameWithOwner": "acme/web"}, "headRepository": null}],
			"pageInfo": {"hasNextPage": false}}}}`))
	}))
	defer srv.Close()
	c := clientFor(t, srv)
	since := time.Date(2026, 10, 13, 11, 0, 0, 0, time.UTC)
	prs, err := SearchOpenPRs(t.Context(), c, []string{"acme/app", "acme/web"}, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || prs[0] != (OpenPR{Repo: "acme/app", Number: 7, HeadSHA: "abc", HeadRepo: "acme/app"}) ||
		prs[1] != (OpenPR{Repo: "acme/web", Number: 8, HeadSHA: "def", Draft: true}) {
		t.Fatalf("prs = %+v", prs)
	}
	if queries[0] != "is:pr is:open updated:>=2026-10-13T11:00:00Z repo:acme/app repo:acme/web" {
		t.Errorf("query = %q", queries[0])
	}
}

func clientFor(t *testing.T, srv *httptest.Server) *github.Client {
	t.Helper()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decodeJSON(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
