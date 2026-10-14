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

func TestBranchRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		notFound := func(msg string) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"message":%q}`, msg)
		}
		switch r.URL.Path {
		case "/repos/acme/app/branches/main/protection":
			_, _ = w.Write([]byte(`{
				"required_pull_request_reviews": {"required_approving_review_count": 1, "require_code_owner_reviews": false},
				"required_status_checks": {"strict": true, "contexts": ["ci"], "checks": [{"context": "ci"}, {"context": "lint"}]}
			}`))
		case "/repos/acme/app/rules/branches/main":
			if r.URL.Query().Get("page") != "2" {
				w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, r.URL.Path))
				_, _ = w.Write([]byte(`[{"type": "pull_request", "ruleset_id": 7, "ruleset_source_type": "Repository", "ruleset_source": "acme/app",
					"parameters": {"required_approving_review_count": 2, "require_code_owner_review": true, "require_last_push_approval": true,
					"dismiss_stale_reviews_on_push": true, "required_review_thread_resolution": false}}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"type": "required_status_checks", "ruleset_id": 9, "ruleset_source_type": "Organization", "ruleset_source": "acme",
				"parameters": {"strict_required_status_checks_policy": false, "required_status_checks": [{"context": "exemplar/merge-policy"}]}}]`))
		case "/repos/acme/open/branches/main/protection":
			notFound("Branch not protected")
		case "/repos/acme/open/rules/branches/main":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/acme/noadmin/branches/main/protection":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			notFound("Not Found")
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}

	// Classic protection and two rulesets: the strictest of each wins.
	br, err := BranchRules(t.Context(), c, "acme/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if br.RequiredApprovals != 2 || !br.CodeOwnerReview || !br.LastPushApproval ||
		!slices.Equal(br.RequiredChecks, []string{"ci", "exemplar/merge-policy", "lint"}) ||
		!slices.Equal(br.Sources, []string{"branch_protection", "ruleset:7", "ruleset:9"}) {
		t.Errorf("rules = %+v", br)
	}

	// An unprotected branch with no rulesets has no rules; that's not an error.
	if br, err := BranchRules(t.Context(), c, "acme/open", "main"); err != nil || br.RequiredApprovals != 0 || len(br.Sources) != 0 {
		t.Errorf("open = %+v, %v", br, err)
	}
	// A token without Administration read, or a missing repo, is an error:
	// the gate fails closed rather than reading "no rules".
	if _, err := BranchRules(t.Context(), c, "acme/noadmin", "main"); err == nil {
		t.Error("403 read as no rules")
	}
	if _, err := BranchRules(t.Context(), c, "acme/gone", "main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing repo err = %v", err)
	}
	if _, err := BranchRules(t.Context(), c, "nope", "main"); err == nil {
		t.Error("bad repo accepted")
	}
}
