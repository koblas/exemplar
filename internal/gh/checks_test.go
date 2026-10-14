package gh

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
)

func TestAnnotationsAndMergedPRs(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	ts := func(d int) string { return now.AddDate(0, 0, -d).Format(time.RFC3339) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/app/commits/abc1234/check-runs":
			fmt.Fprint(w, `{"total_count":2,"check_runs":[{"id":1,"name":"golangci-lint"},{"id":2,"name":"unit-tests"}]}`)
		case "/repos/acme/app/check-runs/1/annotations":
			fmt.Fprint(w, `[{"path":"svc/a.go","start_line":6,"end_line":6,"annotation_level":"warning","title":"noctx","message":"should use NewRequestWithContext"}]`)
		case "/repos/acme/app/check-runs/2/annotations":
			t.Error("read annotations of a check that is not a configured linter")
		case "/repos/acme/app/pulls":
			fmt.Fprintf(w, `[
			  {"number":3,"updated_at":%q,"merged_at":%q,"base":{"sha":"b3"},"head":{"sha":"h3"},"user":{"login":"a"}},
			  {"number":2,"updated_at":%q,"merged_at":null,"base":{"sha":"b2"},"head":{"sha":"h2"}},
			  {"number":1,"updated_at":%q,"merged_at":%q,"base":{"sha":"b1"},"head":{"sha":"h1"}}
			]`, ts(1), ts(1), ts(5), ts(40), ts(40))
		case "/repos/acme/app/pulls/3/files":
			fmt.Fprint(w, `[{"filename":"svc/a.go"},{"filename":"README.md"}]`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	anns, err := Annotations(t.Context(), c, "acme/app", "abc1234", []string{"GolangCI-Lint"})
	if err != nil || len(anns) != 1 || anns[0].Title != "noctx" || anns[0].Check != "golangci-lint" || anns[0].Start != 6 {
		t.Fatalf("Annotations = %+v, %v", anns, err)
	}
	prs, files, err := MergedPRs(t.Context(), c, "acme/app", now.AddDate(0, 0, -30), 10)
	if err != nil || len(prs) != 1 || prs[0].Number != 3 || prs[0].HeadSHA != "h3" || strings.Join(files[0], ",") != "svc/a.go,README.md" {
		t.Fatalf("MergedPRs = %+v %v, %v", prs, files, err)
	}
}

func TestReviewsAndTeamMember(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/exemplar-config/pulls/4/reviews":
			fmt.Fprint(w, `[{"id":1,"user":{"login":"dana","type":"User"},"state":"APPROVED","commit_id":"h1"}]`)
		case "/orgs/platform/teams/devex/memberships/dana":
			fmt.Fprint(w, `{"state":"active","role":"member"}`)
		case "/orgs/platform/teams/devex/memberships/pat":
			fmt.Fprint(w, `{"state":"pending","role":"member"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, _ := github.NewClient(github.WithURLs(&base, &base))
	rs, err := Reviews(t.Context(), c, "acme/exemplar-config", 4)
	if err != nil || len(rs) != 1 || rs[0].User != "dana" || rs[0].State != "APPROVED" || rs[0].CommitID != "h1" {
		t.Fatalf("Reviews = %+v, %v", rs, err)
	}
	for login, want := range map[string]bool{"dana": true, "pat": false, "eve": false} {
		if got, err := TeamMember(t.Context(), c, "@platform/devex", login); err != nil || got != want {
			t.Errorf("TeamMember(%s) = %v, %v", login, got, err)
		}
	}
	if _, err := TeamMember(t.Context(), c, "devex", "dana"); err == nil {
		t.Error("accepted a team without an org")
	}
}

func TestLinterAnnotations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/app/commits/abc1234/check-runs":
			fmt.Fprint(w, `{"total_count":3,"check_runs":[
			  {"id":1,"name":"golangci-lint","status":"completed"},
			  {"id":2,"name":"eslint","status":"in_progress"},
			  {"id":3,"name":"unit-tests","status":"completed"}]}`)
		case "/repos/acme/app/check-runs/1/annotations":
			fmt.Fprint(w, `[{"path":"svc/a.go","start_line":6,"end_line":7,"title":"noctx","message":"use a context (noctx)"}]`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	// eslint is still running and clang-tidy has not started: both pend,
	// and the running one's partial annotations are not read.
	anns, pending, err := LinterAnnotations(t.Context(), c, "acme/app", "abc1234", []string{"golangci-lint", "ESLint", "clang-tidy"})
	if err != nil || len(anns) != 1 || anns[0].End != 7 || strings.Join(pending, ",") != "ESLint,clang-tidy" {
		t.Fatalf("LinterAnnotations = %+v, pending %v, %v", anns, pending, err)
	}
}
