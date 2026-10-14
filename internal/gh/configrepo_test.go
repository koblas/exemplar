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

// fakeConfigGitHub imitates the REST endpoints ConfigRepo uses.
type fakeConfigGitHub struct {
	mu       sync.Mutex
	refs     map[string]string // "heads/x" -> sha
	files    map[string]string // branch:path -> content
	sha      map[string]string // branch:path -> blob sha
	prs      []map[string]any
	comments []string
	requests []string
}

func (f *fakeConfigGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{}
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		_ = json.Unmarshal(data, &body)
	}
	const repo = "/repos/acme/exemplar-config"
	switch p := r.URL.Path; {
	case r.Method == "GET" && p == repo:
		fmt.Fprint(w, `{"default_branch":"main"}`)
	case r.Method == "GET" && strings.HasPrefix(p, repo+"/git/ref/"):
		ref := strings.TrimPrefix(p, repo+"/git/ref/")
		fmt.Fprintf(w, `{"ref":"refs/%s","object":{"sha":%q}}`, ref, f.refs[ref])
	case r.Method == "POST" && p == repo+"/git/refs":
		ref := strings.TrimPrefix(body["ref"].(string), "refs/")
		if _, exists := f.refs[ref]; exists {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"message":"Reference already exists"}`)
			return
		}
		f.refs[ref] = body["sha"].(string)
		fmt.Fprint(w, `{}`)
	case strings.HasPrefix(p, repo+"/contents/"):
		path := strings.TrimPrefix(p, repo+"/contents/")
		branch := r.URL.Query().Get("ref")
		if r.Method == "PUT" {
			branch = body["branch"].(string)
			key := branch + ":" + path
			if prev, ok := f.sha[key]; ok && body["sha"] != prev {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"message":"sha mismatch"}`)
				return
			}
			raw, _ := base64.StdEncoding.DecodeString(body["content"].(string))
			f.files[key] = string(raw)
			f.sha[key] = fmt.Sprintf("blob%d", len(f.sha)+1)
			fmt.Fprint(w, `{"content":{}}`)
			return
		}
		if s, ok := f.sha[branch+":"+path]; ok {
			fmt.Fprintf(w, `{"type":"file","sha":%q}`, s)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == "GET" && p == repo+"/pulls":
		head := r.URL.Query().Get("head")
		var out []map[string]any
		for _, pr := range f.prs {
			if "acme:"+pr["head"].(string) == head {
				out = append(out, map[string]any{"html_url": pr["html_url"], "head": map[string]any{"ref": pr["head"]}})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == "POST" && p == repo+"/issues/5/comments":
		f.comments = append(f.comments, fmt.Sprint(body["body"]))
		fmt.Fprint(w, `{"id": 1}`)
	case r.Method == "GET" && p == repo+"/pulls/5":
		fmt.Fprint(w, `{"number":5,"title":"Candidate rule: x","base":{"sha":"base1"},"head":{"sha":"head1"},"user":{"login":"exemplar[bot]"}}`)
	case r.Method == "POST" && p == repo+"/pulls":
		body["html_url"] = fmt.Sprintf("https://github.com/acme/exemplar-config/pull/%d", len(f.prs)+1)
		f.prs = append(f.prs, body) // the request as sent
		_ = json.NewEncoder(w).Encode(map[string]any{"html_url": body["html_url"], "draft": body["draft"], "base": map[string]any{"ref": body["base"]}})
	default:
		w.WriteHeader(404)
	}
}

func TestConfigRepoFlow(t *testing.T) {
	fake := &fakeConfigGitHub{refs: map[string]string{"heads/main": "base1"}, files: map[string]string{}, sha: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_cfg"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewConfigRepo(client, "acme/exemplar-config")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	name, sha, err := r.DefaultBranch(ctx)
	if err != nil || name != "main" || sha != "base1" {
		t.Fatalf("DefaultBranch = %s %s %v", name, sha, err)
	}
	if _, found, err := r.FindPR(ctx, "exemplar/candidate-1"); err != nil || found {
		t.Fatalf("FindPR before open = %v %v", found, err)
	}
	for range 2 { // a retried run finds the branch already there
		if err := r.EnsureBranch(ctx, "exemplar/candidate-1", sha); err != nil {
			t.Fatalf("EnsureBranch: %v", err)
		}
	}
	for _, content := range []string{"id: a\n", "id: b\n"} { // create, then update with the blob sha
		if err := r.PutFile(ctx, "exemplar/candidate-1", "rules/x/a.yaml", []byte(content), "msg"); err != nil {
			t.Fatalf("PutFile: %v", err)
		}
	}
	if got := fake.files["exemplar/candidate-1:rules/x/a.yaml"]; got != "id: b\n" {
		t.Fatalf("file = %q", got)
	}
	url, err := r.OpenDraftPR(ctx, "exemplar/candidate-1", "main", "Candidate rule: x", "body")
	if err != nil || !strings.HasSuffix(url, "/pull/1") {
		t.Fatalf("OpenDraftPR = %s %v", url, err)
	}
	if fake.prs[0]["draft"] != true || fake.prs[0]["base"] != "main" {
		t.Fatalf("PR = %v", fake.prs[0])
	}
	if got, found, _ := r.FindPR(ctx, "exemplar/candidate-1"); !found || got != url {
		t.Fatalf("FindPR after open = %s %v", got, found)
	}
	if _, err := NewConfigRepo(client, "no-slash"); err == nil {
		t.Fatal("bad repo name accepted")
	}
}

func TestConfigRepoCommentAndPR(t *testing.T) {
	fake := &fakeConfigGitHub{refs: map[string]string{}, files: map[string]string{}, sha: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/"
	client, _ := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_cfg"))
	r, _ := NewConfigRepo(client, "acme/exemplar-config")
	pr, err := r.PullRequest(t.Context(), 5)
	if err != nil || pr.BaseSHA != "base1" || pr.HeadSHA != "head1" || pr.Repo != "acme/exemplar-config" {
		t.Fatalf("PullRequest = %+v, %v", pr, err)
	}
	if err := r.Comment(t.Context(), 5, "reply"); err != nil || len(fake.comments) != 1 || fake.comments[0] != "reply" {
		t.Fatalf("Comment = %v, %v", fake.comments, err)
	}
}
