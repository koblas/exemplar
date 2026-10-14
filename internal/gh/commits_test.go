package gh

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/go-github/v88/github"
)

func TestCommitFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/acme/app/commits/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.URL.Query().Get("page") == "2":
			_, _ = w.Write([]byte(`{"sha":"abc","files":[{"filename":"c.go"}]}`))
		default:
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, r.URL.Path))
			_, _ = w.Write([]byte(`{"sha":"abc","files":[{"filename":"a.go"},{"filename":"b.go"}]}`))
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	files, err := CommitFiles(t.Context(), c, "acme", "app", "abc")
	if err != nil || !slices.Equal(files, []string{"a.go", "b.go", "c.go"}) {
		t.Errorf("CommitFiles = %v, %v", files, err)
	}
	if _, err := CommitFiles(t.Context(), c, "acme", "app", "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing commit err = %v, want ErrNotFound", err)
	}
}

func TestPRFilePatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/acme/app/pulls/7/files" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"filename":"a.go","patch":"@@ -1 +1 @@\n-x\n+y"},{"filename":"logo.png"}]`))
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	files, err := PRFilePatches(t.Context(), c, "acme", "app", 7)
	if err != nil || len(files) != 2 || files[0].Patch == "" || files[1].Patch != "" {
		t.Errorf("PRFilePatches = %+v, %v", files, err)
	}
	if _, err := PRFilePatches(t.Context(), c, "acme", "app", 8); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing PR err = %v, want ErrNotFound", err)
	}
}

func TestCanReadAndGetPullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/app/collaborators/alice/permission":
			_, _ = w.Write([]byte(`{"permission":"read","user":{"login":"alice"}}`))
		case "/repos/acme/app/collaborators/mallory/permission":
			_, _ = w.Write([]byte(`{"permission":"none","user":{"login":"mallory"}}`))
		case "/repos/acme/app/collaborators/broken/permission":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		case "/repos/acme/app/pulls/7":
			_, _ = w.Write([]byte(`{"number":7,"title":"Add cache","body":"b","state":"open","draft":false,"user":{"login":"bob"},"base":{"sha":"aaaaaaa"},"head":{"sha":"bbbbbbb"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	for login, want := range map[string]bool{"alice": true, "mallory": false, "ghost": false, "": false} {
		if got, err := CanRead(t.Context(), c, "acme/app", login); err != nil || got != want {
			t.Errorf("CanRead(%q) = %v, %v; want %v", login, got, err, want)
		}
	}
	if _, err := CanRead(t.Context(), c, "acme/app", "broken"); err == nil {
		t.Error("a server error must be an error, not a yes or no")
	}
	pr, err := GetPullRequest(t.Context(), c, "acme/app", 7)
	if err != nil || pr.Title != "Add cache" || pr.Author != "bob" || pr.BaseSHA != "aaaaaaa" || pr.HeadSHA != "bbbbbbb" {
		t.Errorf("GetPullRequest = %+v, %v", pr, err)
	}
	if _, err := GetPullRequest(t.Context(), c, "acme/app", 8); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing PR err = %v", err)
	}
}
