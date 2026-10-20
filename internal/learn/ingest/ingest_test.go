package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// fakeToken is assembled at run time so no literal credential is in source.
func fakeToken() string {
	sum := sha256.Sum256([]byte("exemplar ingest test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

type eligible map[string]bool

func (e eligible) CheckEligible(repo string) error {
	if !e[repo] {
		return errors.New("denylisted")
	}
	return nil
}

// fakeGraphQL serves canned pages keyed by the "after" cursor.
type fakeGraphQL struct {
	t       *testing.T
	pages   map[string]map[string]any // "" = first page
	afters  []string
	teams   int
	limited bool
}

func (f *fakeGraphQL) query(_ context.Context, q string, vars map[string]any, out any) error {
	var data any
	switch q {
	case filesQuery:
		data = map[string]any{"repository": map[string]any{
			"gitattributes":    map[string]any{"text": "*.pb.go linguist-generated\n"},
			"exemplarignore":   nil,
			"codeownersGithub": map[string]any{"text": "*.go @acme/go\n/svc/ @svc-lead\n"},
		}}
	case teamQuery:
		f.teams++
		data = map[string]any{"organization": map[string]any{"team": map[string]any{"members": map[string]any{
			"nodes": []any{map[string]any{"login": "Gopher"}}, "pageInfo": map[string]any{"hasNextPage": false},
		}}}}
	case prQuery:
		after := ""
		if a, ok := vars["after"].(*string); ok && a != nil {
			after = *a
		}
		f.afters = append(f.afters, after)
		if f.limited {
			return gh.ErrRateLimited
		}
		page, ok := f.pages[after]
		if !ok {
			f.t.Fatalf("unexpected cursor %q", after)
		}
		data = page
	default:
		f.t.Fatalf("unexpected query %q", q[:40])
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func thread(id int, login, typename, path, body string, resolved bool) map[string]any {
	return map[string]any{"isResolved": resolved, "comments": map[string]any{"nodes": []any{map[string]any{
		"fullDatabaseId": itoa(id), "url": "https://github.com/acme/app/pull/1#discussion_r" + itoa(id),
		"body": body, "path": path, "diffHunk": "@@ -1,3 +1,4 @@\n+x := 1", "createdAt": "2026-09-01T10:00:00Z",
		"author": map[string]any{"__typename": typename, "login": login}, "authorAssociation": "MEMBER",
		"originalCommit": map[string]any{"oid": "aaaaaaa1111111"}, "originalLine": 12, "originalStartLine": 10,
	}}}}
}

// as sets a thread root's author association.
func as(association string, th map[string]any) map[string]any {
	comments, _ := th["comments"].(map[string]any)
	nodes, _ := comments["nodes"].([]any)
	root, _ := nodes[0].(map[string]any)
	root["authorAssociation"] = association
	return th
}

func itoa(i int) string { return strconv.Itoa(i) }

func pr(number int, author string, merged bool, files []string, approvers []string, threads ...map[string]any) map[string]any {
	fs := make([]any, 0, len(files))
	rs := make([]any, 0, len(approvers))
	ts := make([]any, 0, len(threads))
	for _, f := range files {
		fs = append(fs, map[string]any{"path": f})
	}
	for _, a := range approvers {
		rs = append(rs, map[string]any{"author": map[string]any{"__typename": "User", "login": a}})
	}
	for _, t := range threads {
		ts = append(ts, t)
	}
	p := map[string]any{
		"number": number, "author": map[string]any{"__typename": "User", "login": author},
		"headRefOid": "bbbbbbb2222222", "baseRefOid": "ccccccc3333333",
		"files":         map[string]any{"nodes": fs, "pageInfo": map[string]any{"hasNextPage": false}},
		"reviews":       map[string]any{"nodes": rs},
		"reviewThreads": map[string]any{"nodes": ts, "pageInfo": map[string]any{"hasNextPage": false}},
	}
	if merged {
		p["mergedAt"] = "2026-09-02T10:00:00Z"
	}
	return p
}

func page(next string, prs ...any) map[string]any {
	pi := map[string]any{"hasNextPage": next != "", "endCursor": "end-" + itoa(len(prs))}
	if next != "" {
		pi["endCursor"] = next
	}
	return map[string]any{
		"rateLimit":  map[string]any{"cost": 1, "remaining": 4000, "resetAt": "2026-09-02T11:00:00Z"},
		"repository": map[string]any{"pullRequests": map[string]any{"pageInfo": pi, "nodes": prs}},
	}
}

const keep = "This fetch runs once per item in the loop; batch the IDs and fetch once."

func newJob(t *testing.T, st *store.Store, f *fakeGraphQL, cfg Config) *Job {
	t.Helper()
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := NewPseudonymizer([]byte("0123456789abcdef0123456789abcdef"))
	j, err := New(cfg, f.query, st, eligible{"acme/app": true}, sc, p)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestRun(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	f := &fakeGraphQL{t: t, pages: map[string]map[string]any{
		"": page("c1",
			pr(1, "alice", true, []string{"svc/a.go", "lib/b.go", "api/x.pb.go"}, []string{"gopher"},
				thread(101, "bob", "User", "svc/a.go", keep, true),
				thread(102, "alice", "User", "svc/a.go", keep, false),           // self-comment
				thread(103, "dependabot", "Bot", "svc/a.go", keep, false),       // bot
				thread(104, "exemplar-review", "User", "svc/a.go", keep, false), // our own app
				thread(105, "bob", "User", "svc/a.go", "LGTM 👍", false),         // ack
				thread(106, "bob", "User", "api/x.pb.go", keep, false),          // excluded path
				thread(107, "carol", "User", "svc/b.go", "Never hardcode tokens: "+fakeToken()+" should come from the secret store.", false),
				as("NONE", thread(108, "mallory", "User", "svc/a.go", keep, false)),                      // not the repo's
				as("CONTRIBUTOR", thread(109, "mallory", "User", "svc/a.go", keep, false)),               // an outside contributor
				as("COLLABORATOR", thread(110, "carol", "User", "lib/b.go", keep+" Again here.", false)), // kept
			),
		),
		"c1": page("",
			pr(2, "dave", false, []string{"svc/c.go"}, nil,
				thread(201, "bob", "User", "svc/c.go", keep+" See https://docs.internal.example/http/loops", false)),
			pr(3, "erin", true, []string{"README.md"}, []string{"erin", "frank"}),
		),
	}}
	cfg := Config{BotLogins: []string{"exemplar-review"}, DocHosts: []string{"docs.internal.example"}, MaxPages: 1}

	// First run: one page, then stop (bounded run); the cursor is saved.
	stats, err := newJob(t, st, f, cfg).Run(ctx, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Done || stats.Pages != 1 || stats.Kept != 3 {
		t.Fatalf("first run stats = %+v", stats)
	}
	if !strings.Contains(prQuery, "authorAssociation") {
		t.Error("the PR query does not ask for authorAssociation")
	}
	for reason, n := range map[string]int{DropSelf: 1, DropBot: 2, DropAck: 1, DropExcluded: 1, DropOutsider: 2} {
		if stats.Dropped[reason] != n {
			t.Errorf("dropped[%s] = %d, want %d (%v)", reason, stats.Dropped[reason], n, stats.Dropped)
		}
	}

	// Second run resumes from c1 and finishes.
	cfg.MaxPages = 0
	stats, err = newJob(t, st, f, cfg).Run(ctx, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Done || stats.Kept != 1 || !slices.Equal(f.afters, []string{"", "c1"}) {
		t.Fatalf("second run stats = %+v, cursors %v", stats, f.afters)
	}

	var n int
	var body, reviewer string
	var resolved bool
	var start, end int
	var commit string
	if err := st.Pool.QueryRow(ctx, `SELECT body, reviewer_pid, thread_resolved, start_line, end_line, commit_sha, ext
		FROM mined_comments WHERE id = 101`).Scan(&body, &reviewer, &resolved, &start, &end, &commit, new(string)); err != nil {
		t.Fatal(err)
	}
	if body != keep || !resolved || start != 10 || end != 12 || commit != "aaaaaaa1111111" {
		t.Errorf("comment 101 = %q resolved=%v lines=%d-%d commit=%s", body, resolved, start, end, commit)
	}
	if reviewer == "bob" || len(reviewer) != 64 {
		t.Errorf("reviewer not pseudonymized: %q", reviewer)
	}
	var redacted string
	if err := st.Pool.QueryRow(ctx, `SELECT body FROM mined_comments WHERE id = 107`).Scan(&redacted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, fakeToken()) || !strings.Contains(redacted, redact.Placeholder) {
		t.Errorf("secret not redacted: %q", redacted)
	}
	var docs []string
	if err := st.Pool.QueryRow(ctx, `SELECT doc_urls FROM mined_comments WHERE id = 201`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(docs, []string{"https://docs.internal.example/http/loops"}) {
		t.Errorf("doc_urls = %v", docs)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM mined_comments`).Scan(&n); err != nil || n != 4 {
		t.Errorf("mined_comments = %d, %v; want 4", n, err)
	}

	// Merged PRs only; generated files left out; codeowner approval via a team.
	type minedPR struct {
		approved bool
		files    []string
	}
	got := map[int]minedPR{}
	rows, _ := st.Pool.Query(ctx, `SELECT pr_number, approved_by_codeowner, files FROM mined_prs`)
	for rows.Next() {
		var num int
		var m minedPR
		if err := rows.Scan(&num, &m.approved, &m.files); err != nil {
			t.Fatal(err)
		}
		got[num] = m
	}
	if len(got) != 2 || !got[1].approved || !slices.Equal(got[1].files, []string{"svc/a.go", "lib/b.go"}) || got[3].approved {
		t.Errorf("mined_prs = %+v", got)
	}
	if f.teams != 1 {
		t.Errorf("team lookups = %d, want 1 (cached)", f.teams)
	}
	var owner string
	if err := st.Pool.QueryRow(ctx, `SELECT owner FROM path_owners WHERE repo = 'acme/app' AND path = 'svc/a.go'`).Scan(&owner); err != nil || owner != "@svc-lead" {
		t.Errorf("path owner = %q, %v", owner, err)
	}

	// Re-running from scratch is idempotent.
	if _, err := st.Pool.Exec(ctx, `DELETE FROM ingest_cursors`); err != nil {
		t.Fatal(err)
	}
	if _, err := newJob(t, st, f, cfg).Run(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM mined_comments`).Scan(&n); err != nil || n != 4 {
		t.Errorf("after re-run mined_comments = %d, %v; want 4", n, err)
	}
}

func TestRunStopsWhenRateLimited(t *testing.T) {
	st := storetest.New(t)
	f := &fakeGraphQL{t: t, limited: true}
	stats, err := newJob(t, st, f, Config{}).Run(t.Context(), "acme/app")
	if err != nil || stats.ResumeAt == nil || stats.Done {
		t.Fatalf("stats = %+v, err = %v; want a resume time", stats, err)
	}
}

func TestRunRefusesIneligibleRepo(t *testing.T) {
	st := storetest.New(t)
	f := &fakeGraphQL{t: t}
	if _, err := newJob(t, st, f, Config{}).Run(t.Context(), "acme/denied"); err == nil {
		t.Fatal("ingested an ineligible repo")
	}
	if len(f.afters) != 0 {
		t.Error("queried GitHub for an ineligible repo")
	}
}

// Comments mined before ingest checked the commenter's association are
// rechecked by a full re-pass: the ones it keeps are trusted, the rest of
// the repo's human comments are not, and those are never extracted.
func TestRecheckAuthors(t *testing.T) {
	st := storetest.New(t)
	ctx := t.Context()
	f := &fakeGraphQL{t: t, pages: map[string]map[string]any{
		"": page("c1",
			pr(1, "alice", true, []string{"svc/a.go"}, nil,
				thread(101, "bob", "User", "svc/a.go", keep, false),
				as("NONE", thread(108, "mallory", "User", "svc/a.go", keep, false)),
			),
		),
		"c1": page(""),
	}}
	// Rows from before the check: 101 and 108 as mined then, 999 from a
	// PR the pass no longer returns, 900 a revert seed (no commenter).
	for _, r := range []struct {
		id     int
		source string
	}{{101, "human"}, {108, "human"}, {999, "human"}, {900, "revert_seed"}} {
		if _, err := st.Pool.Exec(ctx, `INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at, comment_url, body, source)
			VALUES ($1, 'acme/app', 1, 'svc/a.go', '.go', 'r', 'a', now(), 'u', $2, $3)`, r.id, keep, r.source); err != nil {
			t.Fatal(err)
		}
	}
	job := newJob(t, st, f, Config{})
	if err := job.StartAuthorRecheck(ctx, "acme/app"); err != nil {
		t.Fatal(err)
	}
	stats, err := job.Run(ctx, "acme/app")
	if err != nil || !stats.Done || stats.Untrusted != 2 {
		t.Fatalf("recheck run = %+v, %v", stats, err)
	}
	trust := map[int]*bool{}
	rows, _ := st.Pool.Query(ctx, `SELECT id, author_trusted FROM mined_comments`)
	for rows.Next() {
		var id int
		var tr *bool
		if err := rows.Scan(&id, &tr); err != nil {
			t.Fatal(err)
		}
		trust[id] = tr
	}
	is := func(p *bool, v bool) bool { return p != nil && *p == v }
	if !is(trust[101], true) || !is(trust[108], false) || !is(trust[999], false) || trust[900] != nil {
		t.Errorf("trust = 101:%v 108:%v 999:%v 900:%v", trust[101], trust[108], trust[999], trust[900])
	}
	pending, err := st.PendingComments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.ID == 108 || p.ID == 999 {
			t.Errorf("untrusted comment %d is pending extraction", p.ID)
		}
	}
	// The recheck is done: a later pass marks nothing.
	if pendingRecheck, err := st.AuthorRecheckPending(ctx, "acme/app"); err != nil || pendingRecheck {
		t.Errorf("recheck still pending: %v, %v", pendingRecheck, err)
	}
}
