package gh

import (
	"encoding/base64"
	"encoding/json"
	"errors"
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
	// commits are the commits each ref or PR has beyond its base, as the
	// compare and PR-commits endpoints list them; merged records the
	// expected head each merge named.
	commits map[string][]map[string]any
	prUser  string
	merged  map[string]string
}

// botCommit and humanCommit are list entries for the fake's commits.
func botCommit() map[string]any {
	return map[string]any{"author": map[string]any{"login": "exemplar[bot]", "type": "Bot"}, "commit": map[string]any{"verification": map[string]any{"verified": true}}}
}

// actionsCommit is a signed commit from another app: GitHub Actions,
// which a committer's workflow can make push to any branch.
func actionsCommit() map[string]any {
	return map[string]any{"author": map[string]any{"login": "github-actions[bot]", "type": "Bot"}, "commit": map[string]any{"verification": map[string]any{"verified": true}}}
}

func humanCommit() map[string]any {
	return map[string]any{"author": map[string]any{"login": "mallory", "type": "User"}, "commit": map[string]any{"verification": map[string]any{"verified": true}}}
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
	case r.Method == http.MethodGet && p == repo:
		fmt.Fprint(w, `{"default_branch":"main"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(p, repo+"/git/ref/"):
		ref := strings.TrimPrefix(p, repo+"/git/ref/")
		fmt.Fprintf(w, `{"ref":"refs/%s","object":{"sha":%q}}`, ref, f.refs[ref])
	case r.Method == http.MethodPost && p == repo+"/git/refs":
		rawRef, ok := bodyString(w, body, "ref")
		if !ok {
			return
		}
		ref := strings.TrimPrefix(rawRef, "refs/")
		if _, exists := f.refs[ref]; exists {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Reference already exists"}`)
			return
		}
		sha, ok := bodyString(w, body, "sha")
		if !ok {
			return
		}
		f.refs[ref] = sha
		fmt.Fprint(w, `{}`)
	case strings.HasPrefix(p, repo+"/contents/"):
		path := strings.TrimPrefix(p, repo+"/contents/")
		branch := r.URL.Query().Get("ref")
		if r.Method == http.MethodPut {
			b, ok := bodyString(w, body, "branch")
			if !ok {
				return
			}
			branch = b
			// GitHub signs an app's API commit, which the config writer
			// relies on (ownCommits), only when the request names no
			// author, committer or signature of its own.
			for _, k := range []string{"author", "committer", "signature"} {
				if _, set := body[k]; set {
					http.Error(w, "PutFile sent "+k+": GitHub would not sign the commit", http.StatusBadRequest)
					return
				}
			}
			key := branch + ":" + path
			if prev, ok := f.sha[key]; ok && body["sha"] != prev {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"message":"sha mismatch"}`)
				return
			}
			content, ok := bodyString(w, body, "content")
			if !ok {
				return
			}
			raw, _ := base64.StdEncoding.DecodeString(content)
			f.files[key] = string(raw)
			f.sha[key] = fmt.Sprintf("blob%d", len(f.sha)+1)
			fmt.Fprint(w, `{"content":{}}`)
			return
		}
		if s, ok := f.sha[branch+":"+path]; ok {
			fmt.Fprintf(w, `{"type":"file","sha":%q}`, s)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == http.MethodGet && p == repo+"/pulls":
		head := r.URL.Query().Get("head")
		var out []map[string]any
		for _, pr := range f.prs {
			if h, ok := pr["head"].(string); ok && "acme:"+h == head {
				out = append(out, map[string]any{"html_url": pr["html_url"], "head": map[string]any{"ref": pr["head"]}})
			}
		}
		writeJSON(w, out)
	case r.Method == http.MethodGet && strings.HasPrefix(p, repo+"/compare/"):
		_, head, _ := strings.Cut(strings.TrimPrefix(p, repo+"/compare/"), "...")
		cs := f.commits[head]
		writeJSON(w, map[string]any{"status": "ahead", "total_commits": len(cs), "commits": cs})
	case r.Method == http.MethodGet && p == repo+"/pulls/9":
		fmt.Fprintf(w, `{"number":9,"head":{"sha":"tip9"},"base":{"sha":"base1"},"user":{"login":%q,"type":%q}}`, f.prUser, map[bool]string{true: "Bot", false: "User"}[strings.HasSuffix(f.prUser, "[bot]")])
	case r.Method == http.MethodGet && p == repo+"/pulls/9/commits":
		writeJSON(w, f.commits["pr9"])
	case r.Method == http.MethodPut && p == repo+"/pulls/9/merge":
		f.merged["9"] = fmt.Sprint(body["sha"])
		fmt.Fprint(w, `{"merged":true}`)
	case r.Method == http.MethodPost && p == repo+"/issues/5/comments":
		f.comments = append(f.comments, fmt.Sprint(body["body"]))
		fmt.Fprint(w, `{"id": 1}`)
	case r.Method == http.MethodGet && p == repo+"/pulls/5":
		fmt.Fprint(w, `{"number":5,"title":"Candidate rule: x","base":{"sha":"base1"},"head":{"sha":"head1"},"user":{"login":"exemplar[bot]"}}`)
	case r.Method == http.MethodPost && p == repo+"/pulls":
		body["html_url"] = fmt.Sprintf("https://github.com/acme/exemplar-config/pull/%d", len(f.prs)+1)
		f.prs = append(f.prs, body) // the request as sent
		writeJSON(w, map[string]any{"html_url": body["html_url"], "draft": body["draft"], "base": map[string]any{"ref": body["base"]}})
	default:
		w.WriteHeader(http.StatusNotFound)
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
	r, err := NewConfigRepo(client, "acme/exemplar-config", "exemplar[bot]")
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
	if _, err := NewConfigRepo(client, "no-slash", "exemplar[bot]"); err == nil {
		t.Fatal("bad repo name accepted")
	}
	if _, err := NewConfigRepo(client, "acme/exemplar-config", ""); err == nil {
		t.Fatal("config repo without the app's login accepted")
	}
}

// A branch or PR Exemplar writes can carry commits someone else pushed;
// those never merge under the App's identity.
func TestConfigRepoRefusesForeignCommits(t *testing.T) {
	fake := &fakeConfigGitHub{
		refs: map[string]string{"heads/exemplar/demote-x": "tipX"}, files: map[string]string{}, sha: map[string]string{},
		commits: map[string][]map[string]any{}, merged: map[string]string{},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/"
	client, _ := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_cfg"))
	r, _ := NewConfigRepo(client, "acme/exemplar-config", "exemplar[bot]")
	ctx := t.Context()

	// A pre-pushed branch with a person's commit is refused.
	fake.commits["tipX"] = []map[string]any{humanCommit()}
	if err := r.EnsureBranch(ctx, "exemplar/demote-x", "base1"); err == nil {
		t.Error("reused a branch with a person's commit")
	}
	// A retried run's own commits are fine.
	fake.commits["tipX"] = []map[string]any{botCommit()}
	if err := r.EnsureBranch(ctx, "exemplar/demote-x", "base1"); err != nil {
		t.Errorf("retried branch: %v", err)
	}
	fake.commits["tipX"] = []map[string]any{actionsCommit()}
	if err := r.EnsureBranch(ctx, "exemplar/demote-x", "base1"); err == nil {
		t.Error("reused a branch with another app's commit")
	}
	unsigned := botCommit()
	unsigned["commit"] = map[string]any{"verification": map[string]any{"verified": false}}
	fake.commits["tipX"] = []map[string]any{unsigned}
	if err := r.EnsureBranch(ctx, "exemplar/demote-x", "base1"); err == nil {
		t.Error("reused a branch with an unsigned commit claiming the bot")
	}

	// MergePR merges only the App's own PR of the App's own commits, pinned
	// to the head it checked.
	fake.prUser = "exemplar[bot]"
	fake.commits["pr9"] = []map[string]any{botCommit(), humanCommit()}
	if err := r.MergePR(ctx, 9, "msg"); err == nil || fake.merged["9"] != "" {
		t.Errorf("merged a PR with a person's commit: %v", err)
	}
	fake.commits["pr9"] = []map[string]any{botCommit(), actionsCommit()}
	if err := r.MergePR(ctx, 9, "msg"); err == nil || fake.merged["9"] != "" {
		t.Errorf("merged a PR with another app's commit: %v", err)
	}
	fake.prUser = "github-actions[bot]"
	fake.commits["pr9"] = []map[string]any{botCommit()}
	if err := r.MergePR(ctx, 9, "msg"); err == nil || fake.merged["9"] != "" {
		t.Errorf("merged another app's PR: %v", err)
	}
	fake.prUser = "mallory"
	if err := r.MergePR(ctx, 9, "msg"); err == nil || fake.merged["9"] != "" {
		t.Errorf("merged a person's PR: %v", err)
	}
	fake.prUser = "exemplar[bot]"
	if err := r.MergePR(ctx, 9, "msg"); err != nil || fake.merged["9"] != "tip9" {
		t.Errorf("MergePR = %v, merged %q", err, fake.merged["9"])
	}
}

func TestConfigRepoCommentAndPR(t *testing.T) {
	fake := &fakeConfigGitHub{refs: map[string]string{}, files: map[string]string{}, sha: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	base := srv.URL + "/"
	client, _ := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_cfg"))
	r, _ := NewConfigRepo(client, "acme/exemplar-config", "exemplar[bot]")
	pr, err := r.PullRequest(t.Context(), 5)
	if err != nil || pr.BaseSHA != "base1" || pr.HeadSHA != "head1" || pr.Repo != "acme/exemplar-config" {
		t.Fatalf("PullRequest = %+v, %v", pr, err)
	}
	if err := r.Comment(t.Context(), 5, "reply"); err != nil || len(fake.comments) != 1 || fake.comments[0] != "reply" {
		t.Fatalf("Comment = %v, %v", fake.comments, err)
	}
}

// bodyString returns the string field key of a fake's request body. When it
// is missing or not a string it answers 400, so the client call fails.
func bodyString(w http.ResponseWriter, body map[string]any, key string) (string, bool) {
	s, ok := body[key].(string)
	if !ok {
		http.Error(w, fmt.Sprintf("request field %q is not a string", key), http.StatusBadRequest)
	}
	return s, ok
}

// writeJSON encodes v as a fake's response, answering 500 if it cannot.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func TestConfigRepoOpenPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/exemplar-config/pulls":
			if r.URL.Query().Get("state") != "open" {
				http.Error(w, "want open PRs", http.StatusBadRequest)
				return
			}
			writeJSON(w, []map[string]any{
				{
					"number": 3, "html_url": "u3", "user": map[string]any{"login": "dana", "type": "User"},
					"head": map[string]any{"ref": "seed", "sha": "h3", "repo": map[string]any{"full_name": "acme/exemplar-config"}},
				},
				{
					"number": 4, "html_url": "u4", "user": map[string]any{"login": "exemplar[bot]", "type": "Bot"},
					"head": map[string]any{"ref": "fork-branch", "sha": "h4", "repo": map[string]any{"full_name": "mallory/exemplar-config"}},
				},
			})
		case "/repos/acme/exemplar-config/pulls/3/files":
			writeJSON(w, []map[string]any{{"filename": "rules/a.yaml", "status": "added"}, {"filename": "rules/b.yaml", "status": "modified"}})
		case "/repos/acme/exemplar-config/pulls/4/files":
			writeJSON(w, []map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	client, _ := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_cfg"))
	r, _ := NewConfigRepo(client, "acme/exemplar-config", "exemplar[bot]")
	prs, err := r.OpenPRs(t.Context())
	if err != nil || len(prs) != 2 {
		t.Fatalf("OpenPRs = %+v, %v", prs, err)
	}
	if p := prs[0]; p.Fork || p.Bot || p.HeadRef != "seed" || p.HeadSHA != "h3" || p.Author != "dana" || len(p.Added) != 1 || p.Added[0] != "rules/a.yaml" {
		t.Errorf("same-repo PR = %+v", p)
	}
	if p := prs[1]; !p.Fork || !p.Bot || len(p.Added) != 0 {
		t.Errorf("fork PR = %+v", p)
	}
}

func TestConfigRepoLatestHumanCommentAndReopen(t *testing.T) {
	var edited, commented string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/exemplar-config/issues/7/comments":
			writeJSON(w, []map[string]any{
				{"body": "first", "user": map[string]any{"login": "erin", "type": "User"}},
				{"body": "Covered by errcheck.", "user": map[string]any{"login": "dana", "type": "User"}},
				{"body": "  ", "user": map[string]any{"login": "pat", "type": "User"}},
				{"body": "bot note", "user": map[string]any{"login": "exemplar[bot]", "type": "Bot"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/exemplar-config/issues/8/comments":
			writeJSON(w, []map[string]any{})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/exemplar-config/pulls/7":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			edited, _ = body["state"].(string)
			writeJSON(w, map[string]any{"number": 7})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/exemplar-config/issues/7/comments":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			commented, _ = body["body"].(string)
			writeJSON(w, map[string]any{"id": 1})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/exemplar-config/pulls/9":
			http.Error(w, `{"message":"branch deleted"}`, http.StatusUnprocessableEntity)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	client, _ := github.NewClient(github.WithURLs(&base, &base), github.WithAuthToken("ghs_cfg"))
	r, _ := NewConfigRepo(client, "acme/exemplar-config", "exemplar[bot]")
	login, body, found, err := r.LatestHumanComment(t.Context(), 7)
	if err != nil || !found || login != "dana" || body != "Covered by errcheck." {
		t.Errorf("LatestHumanComment = %q %q %v %v", login, body, found, err)
	}
	if _, _, found, err := r.LatestHumanComment(t.Context(), 8); err != nil || found {
		t.Errorf("no comments = %v %v", found, err)
	}
	if err := r.ReopenPR(t.Context(), 7, "why?"); err != nil || edited != "open" || commented != "why?" {
		t.Errorf("ReopenPR = %v, state %q, comment %q", err, edited, commented)
	}
	if err := r.ReopenPR(t.Context(), 9, "why?"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reopen of a deleted branch = %v, want ErrNotFound", err)
	}
}
