package gh

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloseThread(t *testing.T) {
	var replies, resolves int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/graphql" && strings.Contains(string(body), "resolveReviewThread"):
			resolves++
			_, _ = w.Write([]byte(`{"data": {"resolveReviewThread": {"thread": {"isResolved": true}}}}`))
		case r.URL.Path == "/graphql":
			_, _ = w.Write([]byte(`{"data": {"repository": {"pullRequest": {"reviewThreads": {"nodes": [
				{"id": "T1", "isResolved": false, "comments": {"nodes": [{"databaseId": 11}]}},
				{"id": "T2", "isResolved": true, "comments": {"nodes": [{"databaseId": 22}]}}],
				"pageInfo": {"hasNextPage": false}}}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/pulls/7/comments":
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["in_reply_to"] != float64(11) || m["body"] != "Addressed in abc1234." {
				t.Errorf("reply = %v", m)
			}
			replies++
			_, _ = w.Write([]byte(`{"id": 99}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := NewPoster(clientFor(t, srv))
	pr, _ := ParsePR("acme/app", 7, "abc1234")
	id, err := p.CloseThread(t.Context(), pr, 11, "", "Addressed in abc1234.")
	if err != nil || id != "T1" || replies != 1 || resolves != 1 {
		t.Fatalf("close = %q, %v (replies %d, resolves %d)", id, err, replies, resolves)
	}
	// Someone already resolved it: no reply, no second resolve.
	if id, err := p.CloseThread(t.Context(), pr, 22, "", "Addressed in abc1234."); err != nil || id != "T2" || replies != 1 || resolves != 1 {
		t.Errorf("resolved thread = %q, %v (replies %d)", id, err, replies)
	}
	if _, err := p.CloseThread(t.Context(), pr, 33, "", "x"); !errors.Is(err, ErrNoThread) {
		t.Errorf("missing thread err = %v", err)
	}
}

func TestIsAncestor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/app/compare/a...head":
			_, _ = w.Write([]byte(`{"status": "ahead"}`))
		case "/repos/acme/app/compare/head...head":
			_, _ = w.Write([]byte(`{"status": "identical"}`))
		case "/repos/acme/app/compare/b...head":
			_, _ = w.Write([]byte(`{"status": "diverged"}`))
		case "/repos/acme/app/compare/err...head":
			w.WriteHeader(http.StatusInternalServerError)
		default: // the old head is gone after a force-push
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
		}
	}))
	defer srv.Close()
	c := clientFor(t, srv)
	for old, want := range map[string]bool{"a": true, "head": true, "b": false, "gone": false} {
		if got, err := IsAncestor(t.Context(), c, "acme/app", old, "head"); err != nil || got != want {
			t.Errorf("%s: %v, %v", old, got, err)
		}
	}
	if _, err := IsAncestor(t.Context(), c, "acme/app", "err", "head"); err == nil {
		t.Error("a server error read as an answer")
	}
}

func TestMinimizeCommentAndReply(t *testing.T) {
	var minimized []string
	var replies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/comments/11":
			_, _ = w.Write([]byte(`{"id": 11, "node_id": "PRRC_11"}`))
		case r.URL.Path == "/graphql" && strings.Contains(string(body), "minimizeComment") && strings.Contains(string(body), "OUTDATED"):
			var m struct {
				Variables struct {
					ID string `json:"id"`
				} `json:"variables"`
			}
			_ = json.Unmarshal(body, &m)
			minimized = append(minimized, m.Variables.ID)
			_, _ = w.Write([]byte(`{"data": {"minimizeComment": {"minimizedComment": {"isMinimized": true}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/pulls/7/comments":
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			replies = append(replies, m)
			_, _ = w.Write([]byte(`{"id": 99}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := NewPoster(clientFor(t, srv))
	pr, _ := ParsePR("acme/app", 7, "abc1234")
	if err := p.MinimizeComment(t.Context(), pr, 11); err != nil || len(minimized) != 1 || minimized[0] != "PRRC_11" {
		t.Fatalf("minimize = %v, %v", minimized, err)
	}
	if err := p.Reply(t.Context(), pr, 11, "golangci-lint reports this too."); err != nil || len(replies) != 1 ||
		replies[0]["in_reply_to"] != float64(11) || replies[0]["body"] != "golangci-lint reports this too." {
		t.Fatalf("reply = %v, %v", replies, err)
	}
}

func TestPRComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/acme/app/pulls/7/comments" {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
		  {"id": 1, "user": {"login": "dana", "type": "User"}, "author_association": "MEMBER", "path": "a.go", "side": "RIGHT", "start_line": 3, "line": 5, "body": "range"},
		  {"id": 2, "user": {"login": "dana", "type": "User"}, "path": "a.go", "side": "RIGHT", "line": 8, "body": "single"},
		  {"id": 3, "user": {"login": "dana", "type": "User"}, "path": "a.go", "side": "RIGHT", "line": null, "original_line": 9, "body": "outdated"}]`))
	}))
	defer srv.Close()
	cs, err := PRComments(t.Context(), clientFor(t, srv), "acme/app", 7)
	if err != nil || len(cs) != 2 {
		t.Fatalf("PRComments = %+v, %v", cs, err)
	}
	if c := cs[0]; c.Start != 3 || c.End != 5 || c.Association != "MEMBER" || c.AuthorType != "User" || c.Author != "dana" {
		t.Errorf("range comment = %+v", c)
	}
	if c := cs[1]; c.Start != 8 || c.End != 8 {
		t.Errorf("single-line comment = %+v", c)
	}
}
