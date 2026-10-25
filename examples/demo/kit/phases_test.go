package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
)

// fakeGitHub is the slice of the GitHub API the kit uses, for one repo.
type fakeGitHub struct {
	t       *testing.T
	mu      sync.Mutex
	nextID  int
	refs    map[string]string // branch -> sha
	prs     []*fakePR
	writes  int
	botRule string // when set, each volume PR gets TodosPerVolumePR bot comments
	// refused are the logins whose replies the worker does not
	// acknowledge (no +1), as when their command is not allowed.
	refused map[string]bool
	authors map[int]string // comment id -> login, for the +1
}

type fakePR struct {
	number   int
	branch   string
	head     string
	merged   bool
	reviews  []fakeReview
	comments []map[string]any
}

type fakeReview struct {
	login    string
	comments []map[string]any
}

// str is v as a string, "" when it is not one.
func str(v any) string { s, _ := v.(string); return s }

func (f *fakeGitHub) id() int { f.nextID++; return f.nextID }

func (f *fakeGitHub) pr(n int) *fakePR { return f.prs[n-1] }

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	login := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	p := strings.TrimPrefix(r.URL.Path, "/repos/demo-org/ledger")
	if r.Method != http.MethodGet {
		f.writes++
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			f.t.Errorf("encode reply: %v", err)
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		reply(map[string]any{"login": login})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/ref/heads/"):
		sha, ok := f.refs[strings.TrimPrefix(p, "/git/ref/heads/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"message": "Not Found"})
			return
		}
		reply(map[string]any{"object": map[string]any{"sha": sha}})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/commits/"):
		reply(map[string]any{"sha": strings.TrimPrefix(p, "/git/commits/"), "tree": map[string]any{"sha": "tree"}})
	case r.Method == http.MethodPost && p == "/git/trees":
		reply(map[string]any{"sha": fmt.Sprintf("tree%d", f.id())})
	case r.Method == http.MethodPost && p == "/git/commits":
		reply(map[string]any{"sha": fmt.Sprintf("commit%d", f.id())})
	case r.Method == http.MethodPost && p == "/git/refs":
		f.refs[strings.TrimPrefix(str(body["ref"]), "refs/heads/")] = str(body["sha"])
		reply(map[string]any{})
	case r.Method == http.MethodPatch && strings.HasPrefix(p, "/git/refs/heads/"):
		f.refs[strings.TrimPrefix(p, "/git/refs/heads/")] = str(body["sha"])
		reply(map[string]any{})
	case r.Method == http.MethodGet && p == "/pulls":
		branch := strings.TrimPrefix(r.URL.Query().Get("head"), "demo-org:")
		out := []map[string]any{}
		for _, pr := range f.prs {
			if pr.branch == branch {
				out = append(out, f.prJSON(pr))
			}
		}
		reply(out)
	case r.Method == http.MethodPost && p == "/pulls":
		pr := &fakePR{number: len(f.prs) + 1, branch: str(body["head"]), head: f.refs[str(body["head"])]}
		f.prs = append(f.prs, pr)
		if f.botRule != "" && strings.HasPrefix(pr.branch, "demo/job-") {
			for range TodosPerVolumePR {
				pr.comments = append(pr.comments, map[string]any{"id": f.id(), "user": map[string]any{"type": "Bot"}, "body": "Remove this TODO.\n\n<sub>Rule `" + f.botRule + "`</sub>"})
			}
		}
		reply(f.prJSON(pr))
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/pulls/comments/") && strings.HasSuffix(p, "/reactions"):
		id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p, "/pulls/comments/"), "/reactions"))
		out := []map[string]any{}
		if who, ok := f.authors[id]; ok && !f.refused[who] {
			out = append(out, map[string]any{"content": "+1", "user": map[string]any{"type": "Bot"}})
		}
		reply(out)
	case strings.HasPrefix(p, "/pulls/"):
		parts := strings.Split(strings.TrimPrefix(p, "/pulls/"), "/")
		n, _ := strconv.Atoi(parts[0])
		pr := f.pr(n)
		switch {
		case r.Method == http.MethodGet && parts[1] == "reviews":
			out := []map[string]any{}
			for _, rv := range pr.reviews {
				out = append(out, map[string]any{"user": map[string]any{"login": rv.login}, "state": "COMMENTED"})
			}
			reply(out)
		case r.Method == http.MethodPost && parts[1] == "reviews":
			if body["commit_id"] != pr.head {
				f.t.Errorf("review of #%d on %v, want head %s", n, body["commit_id"], pr.head)
			}
			rv := fakeReview{login: login}
			if cs, ok := body["comments"].([]any); ok {
				for _, c := range cs {
					if m, ok := c.(map[string]any); ok {
						rv.comments = append(rv.comments, m)
					}
				}
			}
			pr.reviews = append(pr.reviews, rv)
			reply(map[string]any{"id": f.id()})
		case r.Method == http.MethodGet && parts[1] == "comments":
			reply(pr.comments)
		case r.Method == http.MethodPost && parts[1] == "comments":
			id := f.id()
			pr.comments = append(pr.comments, map[string]any{"id": id, "in_reply_to_id": body["in_reply_to"], "user": map[string]any{"login": login}, "body": body["body"]})
			f.authors[id] = login
			reply(map[string]any{"id": id})
		case r.Method == http.MethodPut && parts[1] == "merge":
			pr.merged = true
			reply(map[string]any{"merged": true})
		default:
			f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeGitHub) prJSON(pr *fakePR) map[string]any {
	out := map[string]any{"number": pr.number, "merged": pr.merged, "head": map[string]any{"sha": pr.head, "ref": pr.branch}}
	if pr.merged {
		out["merged_at"] = time.Now().Format(time.RFC3339)
	}
	return out
}

func newFake(t *testing.T) (*fakeGitHub, *repoClient) {
	t.Helper()
	f := &fakeGitHub{t: t, refs: map[string]string{"main": "base"}, refused: map[string]bool{}, authors: map[int]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	api := srv.URL + "/"
	who := func(login string) account {
		c, err := github.NewClient(github.WithAuthToken(login), github.WithURLs(&api, &api))
		if err != nil {
			t.Fatal(err)
		}
		return account{login: login, c: c}
	}
	r := &repoClient{org: "demo-org", repo: "ledger", author: who("dana"), reviewers: [2]account{who("riley"), who("sam")}, log: slog.New(slog.DiscardHandler)}
	return f, r
}

// TestSeedPhase: every seed PR is opened by the author, reviewed by each
// reviewer with their comments on the lines the data names, and merged;
// a second run writes nothing.
func TestSeedPhase(t *testing.T) {
	t.Parallel()
	f, r := newFake(t)
	seeds, err := loadSeed()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.seed(t.Context(), seeds); err != nil {
		t.Fatal(err)
	}
	if len(f.prs) != len(seeds) {
		t.Fatalf("%d PRs opened, want %d", len(f.prs), len(seeds))
	}
	for i, s := range seeds {
		pr := f.prs[i]
		if !pr.merged || pr.branch != s.Branch {
			t.Errorf("PR %d: branch %s merged %v", pr.number, pr.branch, pr.merged)
		}
		got := 0
		for _, rv := range pr.reviews {
			if rv.login != r.reviewers[0].login && rv.login != r.reviewers[1].login {
				t.Errorf("%s reviewed by %s, not a reviewer", s.Branch, rv.login)
			}
			for _, c := range rv.comments {
				got++
				if str(c["side"]) != "RIGHT" {
					t.Errorf("%s: comment side %v", s.Branch, c["side"])
				}
			}
		}
		if got != len(s.Comments) {
			t.Errorf("%s: %d comments posted, want %d", s.Branch, got, len(s.Comments))
		}
		for _, c := range s.Comments {
			want, _ := lineOf(s.Files[c.Path], c.Match)
			found := false
			for _, rv := range pr.reviews {
				for _, posted := range rv.comments {
					if rv.login == r.reviewers[c.Reviewer].login && posted["path"] == c.Path && posted["line"] == float64(want) {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("%s: reviewer %d's comment on %s:%d not posted", s.Branch, c.Reviewer, c.Path, want)
			}
		}
	}

	writes := f.writes
	if err := r.seed(t.Context(), seeds); err != nil {
		t.Fatal(err)
	}
	if f.writes != writes {
		t.Errorf("re-run made %d writes, want none", f.writes-writes)
	}
}

// TestFeedbackPhase: volume PRs are opened until Target findings are
// judged, each PR is reviewed by both reviewers before their replies, the
// replies alternate between them, and a re-run replies to nothing again.
func TestFeedbackPhase(t *testing.T) {
	t.Parallel()
	f, r := newFake(t)
	f.botRule = DemoRule
	fb := Feedback{Target: 2 * TodosPerVolumePR, MaxPRs: 5}
	n, err := r.feedback(t.Context(), fb)
	if err != nil || n != fb.Target {
		t.Fatalf("judged %d, %v; want %d", n, err, fb.Target)
	}
	if len(f.prs) != 2 {
		t.Fatalf("%d volume PRs, want 2", len(f.prs))
	}
	by := map[string]int{}
	for _, pr := range f.prs {
		reviewed := map[string]bool{}
		for _, rv := range pr.reviews {
			reviewed[rv.login] = true
		}
		if !reviewed["riley"] || !reviewed["sam"] {
			t.Errorf("#%d not reviewed by both reviewers: %v", pr.number, reviewed)
		}
		for _, c := range pr.comments {
			if c["in_reply_to_id"] == nil {
				continue
			}
			if c["body"] != WrongReason {
				t.Errorf("reply %v", c["body"])
			}
			user, _ := c["user"].(map[string]any)
			by[str(user["login"])]++
		}
	}
	if by["riley"] != TodosPerVolumePR || by["sam"] != TodosPerVolumePR {
		t.Errorf("replies by reviewer = %v, want %d each", by, TodosPerVolumePR)
	}

	writes := f.writes
	if n, err := r.feedback(t.Context(), fb); err != nil || n != fb.Target || f.writes != writes {
		t.Errorf("re-run judged %d (%v) with %d writes; want %d and none", n, err, f.writes-writes, fb.Target)
	}
}

// TestFeedbackCountsAcknowledged: a reply the worker does not acknowledge
// is not counted, so more PRs are opened to reach the target.
func TestFeedbackCountsAcknowledged(t *testing.T) {
	t.Parallel()
	f, r := newFake(t)
	f.botRule = DemoRule
	f.refused["sam"] = true
	fb := Feedback{Target: 2 * TodosPerVolumePR, MaxPRs: 10}
	n, err := r.feedback(t.Context(), fb)
	if err != nil || n < fb.Target {
		t.Fatalf("judged %d, %v", n, err)
	}
	// Half the replies (sam's) are refused, so about twice the PRs.
	if len(f.prs) < 3 {
		t.Errorf("%d volume PRs; refused replies were counted", len(f.prs))
	}
}

// TestFeedbackStopsWhenHeldBack: volume PRs with no demo-rule finding stop
// the phase after emptyRun of them, instead of waiting out -max-prs.
func TestFeedbackStopsWhenHeldBack(t *testing.T) {
	t.Parallel()
	f, r := newFake(t)
	_, err := r.feedback(t.Context(), Feedback{Target: 30, MaxPRs: 20})
	if !errors.Is(err, errShort) || len(f.prs) != emptyRun {
		t.Fatalf("err %v after %d PRs; want errShort after %d", err, len(f.prs), emptyRun)
	}
}
