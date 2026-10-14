package gh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v88/github"
)

// fakeTargetGitHub imitates the REST and GraphQL calls RepoWriter makes on
// one target repo.
type fakeTargetGitHub struct {
	mu       sync.Mutex
	refs     map[string]string // "heads/x" -> sha
	files    map[string]string // ref:path -> content
	prs      int
	graphql  []string
	requests []string
}

func (f *fakeTargetGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{}
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		_ = json.Unmarshal(data, &body)
	}
	const repo = "/repos/acme/app"
	switch p := r.URL.Path; {
	case r.Method == "GET" && p == repo:
		fmt.Fprint(w, `{"default_branch":"trunk"}`)
	case r.Method == "GET" && strings.HasPrefix(p, repo+"/git/ref/"):
		ref := strings.TrimPrefix(p, repo+"/git/ref/")
		fmt.Fprintf(w, `{"object":{"sha":%q}}`, f.refs[ref])
	case r.Method == "POST" && p == repo+"/git/refs":
		ref := strings.TrimPrefix(body["ref"].(string), "refs/")
		if _, ok := f.refs[ref]; ok {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"message":"Reference already exists"}`)
			return
		}
		f.refs[ref] = body["sha"].(string)
		fmt.Fprint(w, `{}`)
	case r.Method == "PATCH" && strings.HasPrefix(p, repo+"/git/refs/"):
		if body["force"] != true {
			w.WriteHeader(422)
			return
		}
		f.refs[strings.TrimPrefix(p, repo+"/git/refs/")] = body["sha"].(string)
		fmt.Fprint(w, `{}`)
	case strings.HasPrefix(p, repo+"/contents/"):
		path := strings.TrimPrefix(p, repo+"/contents/")
		if r.Method == "PUT" {
			key := body["branch"].(string) + ":" + path
			if _, ok := f.files[key]; ok != (body["sha"] != nil) {
				w.WriteHeader(409)
				return
			}
			raw, _ := base64.StdEncoding.DecodeString(body["content"].(string))
			f.files[key] = string(raw)
			fmt.Fprint(w, `{"content":{}}`)
			return
		}
		c, ok := f.files[r.URL.Query().Get("ref")+":"+path]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprintf(w, `{"type":"file","sha":"blob","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(c)))
	case r.Method == "GET" && p == repo+"/pulls":
		if r.URL.Query().Get("head") != "acme:exemplar/exports" || r.URL.Query().Get("state") != "open" || f.prs == 0 {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[{"number":3,"html_url":"https://github.com/acme/app/pull/3","node_id":"PR_3","state":"open"}]`)
	case r.Method == "POST" && p == repo+"/pulls":
		if body["head"] != "exemplar/exports" || body["base"] != "trunk" {
			w.WriteHeader(422)
			return
		}
		f.prs++
		fmt.Fprint(w, `{"number":3,"html_url":"https://github.com/acme/app/pull/3","node_id":"PR_3","state":"open"}`)
	case r.Method == "POST" && p == "/graphql":
		f.graphql = append(f.graphql, fmt.Sprint(body["query"], " ", body["variables"]))
		fmt.Fprint(w, `{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-10-14T00:00:00Z"}}}}}`)
	default:
		w.WriteHeader(404)
	}
}

func TestRepoWriterFlow(t *testing.T) {
	fake := &fakeTargetGitHub{refs: map[string]string{"heads/trunk": "head1"}, files: map[string]string{"trunk:AGENTS.md": "# App\n"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_export"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepoWriter(client, "app"); err == nil {
		t.Fatal("a repo without an owner was accepted")
	}
	w, err := NewRepoWriter(client, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	name, sha, err := w.DefaultBranch(ctx)
	if err != nil || name != "trunk" || sha != "head1" {
		t.Fatalf("DefaultBranch = %s %s %v", name, sha, err)
	}
	got, ok, err := w.GetFile(ctx, "trunk", "AGENTS.md")
	if err != nil || !ok || string(got) != "# App\n" {
		t.Fatalf("GetFile = %q %v %v", got, ok, err)
	}
	if _, ok, err := w.GetFile(ctx, "trunk", "CLAUDE.md"); err != nil || ok {
		t.Fatalf("missing file = %v %v", ok, err)
	}

	// Created, then force-moved.
	for _, s := range []string{"head1", "head2"} {
		if err := w.ResetBranch(ctx, "exemplar/exports", s); err != nil || fake.refs["heads/exemplar/exports"] != s {
			t.Fatalf("ResetBranch(%s) = %v, refs %v", s, err, fake.refs)
		}
	}
	// Created, then updated with the blob sha.
	for _, c := range []string{"one\n", "two\n"} {
		if err := w.PutFile(ctx, "exemplar/exports", "AGENTS.md", []byte(c), "m"); err != nil {
			t.Fatal(err)
		}
		fake.files["exemplar/exports:AGENTS.md"] = c // GetContents reads by ref
	}

	if _, _, found, err := w.OpenPRFor(ctx, "exemplar/exports"); err != nil || found {
		t.Fatalf("OpenPRFor before = %v %v", found, err)
	}
	pr, node, err := w.OpenPR(ctx, "exemplar/exports", "trunk", "t", "b")
	if err != nil || pr.URL != "https://github.com/acme/app/pull/3" || node != "PR_3" {
		t.Fatalf("OpenPR = %+v %s %v", pr, node, err)
	}
	if pr, node, found, err := w.OpenPRFor(ctx, "exemplar/exports"); err != nil || !found || pr.Number != 3 || node != "PR_3" {
		t.Fatalf("OpenPRFor after = %+v %s %v %v", pr, node, found, err)
	}
	if err := w.EnableAutoMerge(ctx, node); err != nil || len(fake.graphql) != 1 ||
		!strings.Contains(fake.graphql[0], "enablePullRequestAutoMerge") || !strings.Contains(fake.graphql[0], "SQUASH") || !strings.Contains(fake.graphql[0], "PR_3") {
		t.Fatalf("EnableAutoMerge = %v, %v", err, fake.graphql)
	}
}
