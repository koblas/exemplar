package pathevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

func commit(oid, msg string, pr *associatedPR) historyCommit {
	c := historyCommit{Oid: oid, Message: msg, CommittedDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if pr != nil {
		c.AssociatedPullRequests.Nodes = []associatedPR{*pr}
	}
	return c
}

func mergedPR(n int, title, body, mergeOid string, files ...string) *associatedPR {
	p := &associatedPR{Number: n, Title: title, Body: body, Merged: true}
	if mergeOid != "" {
		p.MergeCommit = &struct {
			Oid string `json:"oid"`
		}{mergeOid}
	}
	for _, f := range files {
		p.Files.Nodes = append(p.Files.Nodes, struct {
			Path string `json:"path"`
		}{f})
	}
	return p
}

func TestClassify(t *testing.T) {
	trailer := "Revert retry change\n\nThis reverts commit 3f2c1a9b8e7d6c5b4a39281706f5e4d3c2b1a090."
	tests := []struct {
		name string
		c    historyCommit
		ref  string
	}{
		{"direct push with trailer", commit("aaa", trailer, nil), "aaa"},
		{"direct push GitHub title", commit("aaa", `Revert "Add cache"`, nil), "aaa"},
		{"revert of a revert re-lands", commit("aaa", `Revert "Revert "Add cache""`, nil), ""},
		{"ordinary commit", commit("aaa", "Add cache", nil), ""},
		{"GitHub revert PR title", commit("aaa", "Merge pull request #9", mergedPR(9, `Revert "Add cache"`, "", "aaa")), "#9"},
		{"GitHub revert PR body", commit("aaa", "Undo cache (#9)", mergedPR(9, "Undo cache", "Reverts acme/app#7\n\nIt broke prod.", "aaa")), "#9"},
		{"squash merge carrying the trailer", commit("aaa", "Back out cache (#9)\n\n"+trailer, mergedPR(9, "Back out cache", "", "aaa")), "#9"},
		{"trailer on a commit inside a normal PR", commit("bbb", trailer, mergedPR(9, "Add cache", "", "aaa")), ""},
		{"unmerged PR falls back to the commit", commit("aaa", trailer, &associatedPR{Number: 9, Title: "x"}), "aaa"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, _, ok := classify(tt.c)
			if ok != (tt.ref != "") || ref != tt.ref {
				t.Errorf("classify = %q, %v; want %q", ref, ok, tt.ref)
			}
		})
	}
}

func TestReadIncidents(t *testing.T) {
	good := `{"id":"INC-1","at":"2026-09-01T12:00:00Z","title":"checkout down","links":[{"repo":"acme/app","pr":7},{"repo":"acme/app","commit":"3f2c1a9"}]}

{"id":"INC-2","at":"2026-09-02T12:00:00Z","links":[]}
`
	incs, err := ReadIncidents(strings.NewReader(good))
	if err != nil || len(incs) != 2 || len(incs[0].Links) != 2 {
		t.Fatalf("ReadIncidents = %+v, %v", incs, err)
	}
	for _, bad := range []string{
		`{"id":"INC 1","at":"2026-09-01T12:00:00Z"}`, // id charset
		`{"id":"INC-1"}`, // no time
		`{"id":"INC-1","at":"2026-09-01T12:00:00Z","links":[{"repo":"app","pr":1}]}`, // repo
		`{"id":"INC-1","at":"2026-09-01T12:00:00Z","links":[{"repo":"acme/app"}]}`,   // neither
		`{"id":"INC-1","at":"2026-09-01T12:00:00Z","links":[{"repo":"acme/app","pr":1,"commit":"abcdef1"}]}`,
		`{"id":"INC-1","at":"2026-09-01T12:00:00Z","links":[{"repo":"acme/app","commit":"HEAD"}]}`,
		`not json`,
	} {
		if _, err := ReadIncidents(strings.NewReader(good + bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

type eligible map[string]bool

func (e eligible) CheckEligible(repo string) error {
	if !e[repo] {
		return errors.New("denylisted")
	}
	return nil
}

// fakeGitHub serves history pages and PR/commit file lists.
type fakeGitHub struct {
	t        *testing.T
	pages    map[string]historyPage // keyed by "after" ("" = first)
	prFiles  map[int][]string
	commits  map[string][]string
	sinces   []string
	prCalls  int
	limited  bool
	connects []string
	// seeds
	reverted  map[int]map[string]any // revertedPRQuery answers
	patches   map[int][]gh.FilePatch
	commitPRs map[string]int
}

func (f *fakeGitHub) connect(_ context.Context, repo string) (API, error) {
	f.connects = append(f.connects, repo)
	return API{Query: f.query, CommitFiles: f.commitFiles, PRFiles: f.prPatches}, nil
}

func (f *fakeGitHub) query(_ context.Context, q string, vars map[string]any, out any) error {
	var data any
	switch q {
	case historyQuery:
		if f.limited {
			return gh.ErrRateLimited
		}
		f.sinces = append(f.sinces, vars["since"].(string))
		after := ""
		if a, ok := vars["after"].(*string); ok && a != nil {
			after = *a
		}
		data = f.pages[after]
	case prFilesQuery:
		f.prCalls++
		files, ok := f.prFiles[vars["number"].(int)]
		if !ok {
			return fmt.Errorf("%w: no PR", gh.ErrNotFound)
		}
		var nodes []any
		for _, p := range files {
			nodes = append(nodes, map[string]any{"path": p})
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"files": map[string]any{"nodes": nodes}}}}
	case exclusionsQuery:
		data = map[string]any{"repository": map[string]any{
			"gitattributes": map[string]any{"text": "*.pb.go linguist-generated\n"}, "exemplarignore": nil,
		}}
	case revertedPRQuery:
		pr, ok := f.reverted[vars["number"].(int)]
		if !ok {
			return fmt.Errorf("%w: no PR", gh.ErrNotFound)
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": pr}}
	case commitPRQuery:
		var nodes []any
		if n, ok := f.commitPRs[vars["oid"].(string)]; ok {
			nodes = append(nodes, map[string]any{"number": n, "merged": true})
		}
		data = map[string]any{"repository": map[string]any{"object": map[string]any{"associatedPullRequests": map[string]any{"nodes": nodes}}}}
	default:
		f.t.Fatalf("unexpected query")
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}

func (f *fakeGitHub) commitFiles(_ context.Context, _, _, sha string) ([]string, error) {
	files, ok := f.commits[sha]
	if !ok {
		return nil, gh.ErrNotFound
	}
	return files, nil
}

func history(next string, commits ...historyCommit) historyPage {
	var p historyPage
	p.Repository.DefaultBranchRef = &struct {
		Target struct {
			History struct {
				PageInfo struct {
					HasNextPage bool    `json:"hasNextPage"`
					EndCursor   *string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []historyCommit `json:"nodes"`
			} `json:"history"`
		} `json:"target"`
	}{}
	h := &p.Repository.DefaultBranchRef.Target.History
	h.Nodes = commits
	if next != "" {
		h.PageInfo.HasNextPage, h.PageInfo.EndCursor = true, &next
	}
	return p
}

var now = time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)

func newJob(t *testing.T, st *store.Store, f *fakeGitHub) *Job {
	t.Helper()
	j, err := New(Config{Now: func() time.Time { return now }}, f.connect, st, eligible{"acme/app": true})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

type event struct{ Path, Kind, Ref string }

func events(t *testing.T, st *store.Store) []event {
	t.Helper()
	rows, err := st.Pool.Query(t.Context(), `SELECT path, kind, ref FROM path_events ORDER BY kind, ref, path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.Path, &e.Kind, &e.Ref); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestReverts(t *testing.T) {
	st := storetest.New(t)
	revertPR := mergedPR(9, `Revert "Add cache"`, "Reverts acme/app#7", "m9", "svc/cache.go", "svc/cache_test.go")
	f := &fakeGitHub{t: t,
		pages: map[string]historyPage{
			"": history("p2",
				commit("m9", "Merge pull request #9 from acme/revert-7", revertPR),
				commit("r9", `Revert "Add cache"`+"\n\nThis reverts commit 1111111.", revertPR), // inside the revert PR: same ref
				commit("c8", "Add metrics", mergedPR(8, "Add metrics", "", "c8", "svc/metrics.go")),
			),
			"p2": history("",
				commit("d1", "Revert hotfix\n\nThis reverts commit 2222222.", nil),
			),
		},
		commits:   map[string][]string{"d1": {"svc/pay.go"}},
		commitPRs: map[string]int{"2222222": 5},
	}
	stats, err := newJob(t, st, f).Reverts(t.Context(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Commits != 4 || stats.Reverts != 2 || stats.Events != 3 {
		t.Errorf("stats = %+v", stats)
	}
	want := []event{{"svc/cache.go", "revert", "#9"}, {"svc/cache_test.go", "revert", "#9"}, {"svc/pay.go", "revert", "d1"}}
	if got := events(t, st); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}

	// The next run starts from the saved point minus the overlap and is idempotent.
	if _, err := newJob(t, st, f).Reverts(t.Context(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	wantSince := []string{"2025-10-13T00:00:00Z", "2025-10-13T00:00:00Z", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z"}
	if !slices.Equal(f.sinces, wantSince) {
		t.Errorf("since = %v, want %v", f.sinces, wantSince)
	}
	if got := events(t, st); len(got) != 3 {
		t.Errorf("re-run changed events: %v", got)
	}
	// The reverted PRs, for counter-metrics: #7 named by the revert PR,
	// #5 through the reverted commit of a direct push.
	if got := prLinks(t, st); !slices.Equal(got, []string{"acme/app#5 revert d1", "acme/app#7 revert #9"}) {
		t.Errorf("links = %v", got)
	}
}

func prLinks(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Pool.Query(t.Context(), `SELECT repo || '#' || pr_number || ' ' || kind || ' ' || ref FROM pr_links ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestRevertsRateLimitedKeepsCursor(t *testing.T) {
	st := storetest.New(t)
	f := &fakeGitHub{t: t, limited: true}
	stats, err := newJob(t, st, f).Reverts(t.Context(), "acme/app")
	if err != nil || !stats.RateLimit {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
	var n int
	if err := st.Pool.QueryRow(t.Context(), `SELECT count(*) FROM path_event_cursors`).Scan(&n); err != nil || n != 0 {
		t.Errorf("cursor saved after a rate-limited run: %d, %v", n, err)
	}
}

func TestRevertsRefusesIneligibleRepo(t *testing.T) {
	st := storetest.New(t)
	f := &fakeGitHub{t: t}
	if _, err := newJob(t, st, f).Reverts(t.Context(), "acme/denied"); err == nil {
		t.Fatal("scanned an ineligible repo")
	}
	if len(f.connects) != 0 {
		t.Error("minted a token for an ineligible repo")
	}
}

func TestIncidents(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	// PR 7 was ingested; PR 8 was not and comes from GitHub.
	if _, err := st.Pool.Exec(ctx, `INSERT INTO mined_prs (repo, pr_number, head_sha, base_sha, merged_at, approved_by_codeowner, files)
		VALUES ('acme/app', 7, 'aaaaaaa', 'bbbbbbb', now(), true, ARRAY['svc/a.go'])`); err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{t: t, prFiles: map[int][]string{8: {"svc/b.go"}}, commits: map[string][]string{"3f2c1a9": {"svc/c.go"}}}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	incs := []Incident{
		{ID: "INC-1", At: at, Links: []Link{
			{Repo: "acme/app", PR: 7}, {Repo: "acme/app", PR: 8}, {Repo: "acme/app", Commit: "3f2c1a9"},
			{Repo: "acme/app", PR: 99},            // unknown
			{Repo: "acme/denied", PR: 1},          // ineligible
			{Repo: "acme/app", Commit: "deadbee"}, // unknown commit
		}},
	}
	stats, err := newJob(t, st, f).Incidents(ctx, incs)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Links != 6 || stats.Ineligible != 1 || stats.Missing != 2 || stats.Events != 3 {
		t.Errorf("stats = %+v", stats)
	}
	if f.prCalls != 2 || slices.Contains(f.connects, "acme/denied") || len(f.connects) != 1 {
		t.Errorf("pr calls = %d, connects = %v", f.prCalls, f.connects)
	}
	want := []event{{"svc/a.go", "incident", "INC-1"}, {"svc/b.go", "incident", "INC-1"}, {"svc/c.go", "incident", "INC-1"}}
	if got := events(t, st); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	// Linked PRs that exist; unknown and ineligible ones are not.
	if got := prLinks(t, st); !slices.Equal(got, []string{"acme/app#7 incident INC-1", "acme/app#8 incident INC-1"}) {
		t.Errorf("links = %v", got)
	}
}
