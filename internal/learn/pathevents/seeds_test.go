package pathevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/learn/ingest"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/store/storetest"
)

func (f *fakeGitHub) prPatches(_ context.Context, _, _ string, n int) ([]gh.FilePatch, error) {
	p, ok := f.patches[n]
	if !ok {
		return nil, gh.ErrNotFound
	}
	return p, nil
}

// fakeSecret is assembled at run time so no literal credential is in source.
func fakeSecret() string {
	sum := sha256.Sum256([]byte("exemplar seed test token"))
	return "ghp_" + hex.EncodeToString(sum[:])[:36]
}

func thread(login, typename, path string, start, line any) map[string]any {
	return map[string]any{"comments": map[string]any{"nodes": []any{map[string]any{
		"path": path, "line": line, "startLine": start, "originalLine": line, "originalStartLine": start,
		"author": map[string]any{"__typename": typename, "login": login},
	}}}}
}

const cachePatch = "@@ -1,2 +1,4 @@\n package svc\n+import \"sync\"\n+var mu sync.Mutex\n \n" +
	"@@ -18,2 +20,3 @@ func Get(k string) string {\n \tv := load(k)\n+\tcache.Set(k, v) // token " + "%s" + "\n \treturn v\n"

// TestRevertSeeds runs at now, in a synctest bubble slept to it.
func TestRevertSeeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(now))
		testRevertSeeds(t)
	})
}

func testRevertSeeds(t *testing.T) {
	t.Helper()
	st := storetest.New(t)
	ctx := t.Context()
	revertPR := mergedPR(9, `Revert "Add price cache"`, "Reverts acme/app#7\n\n"+
		"The cache served stale prices after each deploy; see https://docs.internal.example/runbooks/cache.", "m9", "svc/cache.go", "svc/other.go")
	revertPR.URL = "https://github.com/acme/app/pull/9"
	revertPR.Author.Login = "oncall"
	direct := commit("d1", "Revert hotfix\n\nThis reverts commit 5555555.", nil)
	direct.URL = "https://github.com/acme/app/commit/d1"
	f := &fakeGitHub{
		t:       t,
		pages:   map[string]historyPage{"": history("", commit("m9", "Merge pull request #9", revertPR), direct)},
		commits: map[string][]string{"d1": {"svc/pay.go"}},
		reverted: map[int]map[string]any{
			7: {
				"merged": true, "headRefOid": "ddddddd4444444", "author": map[string]any{"login": "alice"},
				"reviewThreads": map[string]any{"nodes": []any{
					thread("bob", "User", "svc/cache.go", nil, 3),        // covers hunk 1 (lines 1-4)
					thread("dependabot", "Bot", "svc/cache.go", nil, 21), // bots do not count
					thread("alice", "User", "svc/cache.go", 20, 22),      // nor does the author
					thread("carol", "User", "svc/other.go", nil, nil),    // file-level: covers other.go
					thread("dave", "User", "svc/unrelated.go", nil, 1),   // another file
				}},
			},
			6: {
				"merged": true, "headRefOid": "eeeeeee5555555", "author": map[string]any{"login": "erin"},
				"reviewThreads": map[string]any{"nodes": []any{}},
			},
		},
		patches: map[int][]gh.FilePatch{
			7: {
				{Path: "svc/cache.go", Patch: strings.Replace(cachePatch, "%s", fakeSecret(), 1)},
				{Path: "svc/other.go", Patch: "@@ -1 +1 @@\n-a\n+b\n"},
				{Path: "api/x.pb.go", Patch: "@@ -1 +1 @@\n-a\n+b\n"}, // excluded
				{Path: "logo.png"}, // binary
			},
			6: {{Path: "svc/pay.go", Patch: "@@ -10,1 +10,2 @@\n x\n+y\n"}},
		},
		commitPRs: map[string]int{"5555555": 6},
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	pseudo, _ := ingest.NewPseudonymizer([]byte("0123456789abcdef0123456789abcdef"))
	cfg := Config{Seeds: &Seeds{
		Pseudonyms: pseudo, Scanner: sc, DocHosts: []string{"docs.internal.example"},
	}}
	run := func() RevertStats {
		t.Helper()
		j, err := New(cfg, f.connect, st, eligible{"acme/app": true})
		if err != nil {
			t.Fatal(err)
		}
		stats, err := j.Reverts(ctx, "acme/app")
		if err != nil {
			t.Fatal(err)
		}
		return stats
	}
	if stats := run(); stats.Reverts != 2 || stats.Seeds != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	type seedRow struct {
		id               int64
		pr               int
		path, body, hunk string
		reviewer, commit string
		start, end       int
		docs             []string
	}
	rows, err := st.Pool.Query(ctx, `SELECT id, pr_number, path, body, diff_hunk, reviewer_pid, commit_sha, start_line, end_line, doc_urls
		FROM mined_comments WHERE source = 'revert_seed' AND extract_status = 'pending' ORDER BY pr_number DESC`)
	if err != nil {
		t.Fatal(err)
	}
	var got []seedRow
	for rows.Next() {
		var r seedRow
		if err := rows.Scan(&r.id, &r.pr, &r.path, &r.body, &r.hunk, &r.reviewer, &r.commit, &r.start, &r.end, &r.docs); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("seeds = %+v", got)
	}
	cache, pay := got[0], got[1]
	if cache.pr != 7 || cache.path != "svc/cache.go" || cache.id >= 0 {
		t.Errorf("cache seed = %+v", cache)
	}
	// Only the unreviewed second hunk is seeded, redacted, at its new-side lines.
	if strings.Contains(cache.hunk, "@@ -1,2") || !strings.Contains(cache.hunk, "@@ -18,2 +20,3 @@") ||
		strings.Contains(cache.hunk, fakeSecret()) || !strings.Contains(cache.hunk, redact.Placeholder) {
		t.Errorf("cache seed hunk = %q", cache.hunk)
	}
	if cache.start != 20 || cache.end != 22 || cache.commit != "ddddddd4444444" {
		t.Errorf("cache seed location = %d-%d @%s", cache.start, cache.end, cache.commit)
	}
	if !strings.Contains(cache.body, "stale prices") || strings.Contains(cache.body, "Reverts acme/app#7") {
		t.Errorf("cache seed body = %q", cache.body)
	}
	if cache.reviewer != pseudo.PID("oncall") || !slices.Equal(cache.docs, []string{"https://docs.internal.example/runbooks/cache"}) {
		t.Errorf("cache seed reviewer %q docs %v", cache.reviewer, cache.docs)
	}
	// The direct-push revert finds its PR through the reverted SHA; it has no reason.
	if pay.pr != 6 || pay.path != "svc/pay.go" || !strings.Contains(pay.body, "(no reason given)") || pay.commit != "eeeeeee5555555" {
		t.Errorf("pay seed = %+v", pay)
	}

	// Re-running is idempotent.
	run()
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM mined_comments WHERE source = 'revert_seed'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("seeds after re-run = %d, %v", n, err)
	}
}

func TestSeedsNeedDataHandling(t *testing.T) {
	st := storetest.New(t)
	f := &fakeGitHub{t: t}
	if _, err := New(Config{Seeds: &Seeds{}}, f.connect, st, eligible{}); err == nil {
		t.Fatal("accepted seeds without a pseudonymizer and scanner")
	}
}

func TestUncovered(t *testing.T) {
	hunks, err := parsePatch("a.go", "@@ -1,2 +1,3 @@\n a\n+b\n c\n@@ -10 +11,2 @@\n x\n+y\n")
	if err != nil || len(hunks) != 2 {
		t.Fatalf("parsePatch = %v, %v", hunks, err)
	}
	tests := []struct {
		reviewed [][2]int
		want     int
	}{
		{nil, 2},
		{[][2]int{{2, 2}}, 1},
		{[][2]int{{3, 11}}, 0},
		{[][2]int{{5, 9}}, 2},
	}
	for _, tt := range tests {
		if got := uncovered(hunks, tt.reviewed); len(got) != tt.want {
			t.Errorf("uncovered(%v) = %d hunks, want %d", tt.reviewed, len(got), tt.want)
		}
	}
	// Stability is covered by TestRevertSeeds' idempotent re-run.
	if a, b := seedID("acme/app", "#9", "a.go"), seedID("acme/app", "#9", "b.go"); a >= 0 || b >= 0 || a == b {
		t.Errorf("seed ids %d, %d must be negative and distinct", a, b)
	}
}
