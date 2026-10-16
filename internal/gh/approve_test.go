package gh

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v88/github"
)

func TestApproveAndDismiss(t *testing.T) {
	var (
		mu        sync.Mutex
		created   []map[string]any
		dismissed []string
	)
	reviews := `[
		{"id": 1, "state": "APPROVED", "user": {"type": "Bot"}, "commit_id": "old", "body": "ok\n\n<!-- exemplar:approve id=3 head=old -->"},
		{"id": 2, "state": "APPROVED", "user": {"type": "User"}, "commit_id": "old", "body": "lgtm <!-- exemplar:approve id=3 head=old -->"},
		{"id": 4, "state": "APPROVED", "user": {"type": "Bot"}, "commit_id": "old", "body": "another bot's approval"},
		{"id": 5, "state": "COMMENTED", "user": {"type": "Bot"}, "commit_id": "old", "body": "<!-- exemplar:approve id=3 head=old -->"},
		{"id": 6, "state": "APPROVED", "user": {"type": "Bot"}, "commit_id": "new", "body": "<!-- exemplar:approve id=8 head=new -->"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/7/reviews":
			_, _ = w.Write([]byte(reviews))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/dismissals"):
			dismissed = append(dismissed, r.URL.Path)
			_, _ = w.Write([]byte(`{"id": 1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/pulls/7/reviews":
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			created = append(created, m)
			_, _ = w.Write([]byte(`{"id": 99}`))
		case r.URL.Path == "/repos/acme/app/pulls/7":
			_, _ = w.Write([]byte(`{"title": "t", "user": {"login": "renovate[bot]", "type": "Bot"}, "state": "open",
				"base": {"sha": "b", "ref": "main"}, "head": {"sha": "h", "repo": {"full_name": "acme/app"}}}`))
		case r.URL.Path == "/repos/acme/app/pulls/7/commits":
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[{"commit": {"message": "two"}}]`))
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, r.URL.Path))
			_, _ = w.Write([]byte(`[{"commit": {"message": "one\n\nCo-authored-by: Claude <noreply@anthropic.com>"}}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	c, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	p := NewPoster(c)
	pr, _ := ParsePR("acme/app", 7, "new")

	// Only the bot review with Exemplar's marker is dismissed.
	n, err := p.DismissApprovals(t.Context(), pr, "New commits")
	if err != nil || n != 1 || !slices.Equal(dismissed, []string{"/repos/acme/app/pulls/7/reviews/1/dismissals"}) {
		t.Fatalf("dismiss = %d %v, %v", n, dismissed, err)
	}

	id, posted, err := p.Approve(t.Context(), pr, 12, "All gates passed.")
	if err != nil || !posted || id != 99 || len(created) != 1 || created[0]["event"] != "APPROVE" || created[0]["commit_id"] != "new" ||
		!strings.Contains(fmt.Sprint(created[0]["body"]), ApproveMarker(12, "new")) {
		t.Fatalf("approve = %d %v %v %v", id, posted, created, err)
	}

	got, err := GetPullRequest(t.Context(), c, "acme/app", 7)
	if err != nil || got.AuthorType != "Bot" || got.HeadRepo != "acme/app" || got.BaseRef != "main" {
		t.Errorf("PR = %+v, %v", got, err)
	}
	msgs, err := PRCommitMessages(t.Context(), c, "acme/app", 7)
	if err != nil || len(msgs) != 2 || !strings.Contains(msgs[0], "Co-authored-by") || msgs[1] != "two" {
		t.Errorf("commits = %q, %v", msgs, err)
	}
}
