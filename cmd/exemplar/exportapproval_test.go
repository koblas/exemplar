package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportApprovalCmd(t *testing.T) {
	dir := copyConfig(t)
	base := git(t, dir, "rev-parse", "HEAD")
	args := func(head string) []string {
		return []string{"export-approval", "--config", dir, "--changed-since", base, "--repo", "acme/exemplar-config",
			"--pr", "4", "--head-sha", head, "--author", "owen"}
	}

	// A rule change that leaves export: alone passes with no tokens at all.
	q := filepath.Join(dir, "rules/reliability/propagate-context.yaml")
	pc, _ := os.ReadFile(q)
	_ = os.WriteFile(q, append(pc, []byte("model_tier: high\n")...), 0o644)
	git(t, dir, "commit", "-q", "-am", "tweak")
	out, _, err := runCLI(t, args(git(t, dir, "rev-parse", "HEAD"))...)
	if err != nil || !strings.Contains(out, "No rule's `export:` block changed") {
		t.Fatalf("no export change: %q, %v", out, err)
	}

	// Changing the summary needs devex.
	edited := strings.Replace(string(pc), "every I/O call on a request path.", "every outbound call.", 1)
	_ = os.WriteFile(q, []byte(edited), 0o644)
	git(t, dir, "commit", "-q", "-am", "export summary")
	head := git(t, dir, "rev-parse", "HEAD")
	approver := "owen" // the author only
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/exemplar-config/pulls/4/reviews":
			fmt.Fprintf(w, `[{"id":1,"user":{"login":%q,"type":"User"},"state":"APPROVED","commit_id":%q}]`, approver, head)
		case "/orgs/platform/teams/devex/memberships/dana":
			fmt.Fprint(w, `{"state":"active"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	defer srv.Close()
	t.Setenv("GITHUB_TOKEN", "ghs_reviews")
	t.Setenv("EXEMPLAR_TEAMS_TOKEN", "ghs_teams")
	withAPI := append(args(head), "--github-api-url", srv.URL+"/")
	out, _, err = runCLI(t, withAPI...)
	if err == nil || !strings.Contains(out, "❌") || !strings.Contains(out, "every outbound call.") {
		t.Fatalf("author-only approval: %q, %v", out, err)
	}
	approver = "dana"
	out, _, err = runCLI(t, withAPI...)
	if err != nil || !strings.Contains(out, "✅ approved by dana") {
		t.Fatalf("devex approval: %q, %v", out, err)
	}
}
