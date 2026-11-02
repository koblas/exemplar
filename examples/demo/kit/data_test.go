package main

import (
	"os"
	"path"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/learn/ingest"
)

var testVars = Vars{Org: "demo-org", Repo: "ledger", Author: "dana", Owner: "riley", BaseSHA: "0123456789abcdef0123456789abcdef01234567"}

// TestSeedSurvivesIngest checks the seed history against what ingest and
// the learning plane need (docs/plans/demo.md §3), with ingest's own
// filter: every comment is kept, anchors on one line of a file its PR
// adds, and each convention recurs on at least two PRs from both
// reviewers on one file extension, so it clusters into a candidate.
func TestSeedSurvivesIngest(t *testing.T) {
	seeds, err := loadSeed()
	if err != nil {
		t.Fatal(err)
	}
	base, err := tree("_repo", testVars)
	if err != nil {
		t.Fatal(err)
	}
	added := map[string]bool{}
	type stats struct {
		prs, reviewers map[string]bool
		exts           map[string]bool
	}
	patterns := map[string]*stats{"logctx.From": {}, "cents": {}}
	for _, s := range seeds {
		if s.Branch == "" || s.Title == "" || len(s.Files) == 0 || len(s.Comments) == 0 {
			t.Fatalf("seed %q is incomplete", s.Branch)
		}
		for p := range s.Files {
			if _, ok := base[p]; ok || added[p] {
				t.Errorf("%s: %s is not a new file, so its lines may not all be in the diff", s.Branch, p)
			}
			added[p] = true
		}
		for _, c := range s.Comments {
			if why := ingest.Prefilter(c.Body); why != "" {
				t.Errorf("%s: comment dropped by ingest (%s): %q", s.Branch, why, c.Body)
			}
			if c.Reviewer != 0 && c.Reviewer != 1 {
				t.Errorf("%s: reviewer %d is not 0 or 1", s.Branch, c.Reviewer)
			}
			content, ok := s.Files[c.Path]
			if !ok {
				t.Errorf("%s: comment on %s, which the PR does not add", s.Branch, c.Path)
				continue
			}
			if _, err := lineOf(content, c.Match); err != nil {
				t.Errorf("%s: %s: %v", s.Branch, c.Path, err)
			}
			for key, st := range patterns {
				if !strings.Contains(c.Body, key) {
					continue
				}
				if st.prs == nil {
					st.prs, st.reviewers, st.exts = map[string]bool{}, map[string]bool{}, map[string]bool{}
				}
				st.prs[s.Branch], st.reviewers[string(rune('0'+c.Reviewer))], st.exts[path.Ext(c.Path)] = true, true, true
			}
		}
	}
	for key, st := range patterns {
		if len(st.prs) < 2 || len(st.reviewers) < 2 || len(st.exts) != 1 {
			t.Errorf("pattern %q: %d PRs, %d reviewers, extensions %v; want >= 2, 2 and one", key, len(st.prs), len(st.reviewers), st.exts)
		}
	}
}

// TestDemoPRs checks the PR set and the generated beat D PRs: every file
// renders, beat B has one PR each way, and a volume PR's TODOs post as
// separate comments (one per file, no more than the posting threshold).
func TestDemoPRs(t *testing.T) {
	prs, err := loadPRs()
	if err != nil {
		t.Fatal(err)
	}
	approve := map[bool]int{}
	for _, p := range prs {
		if p.Branch == "" || p.Title == "" || len(p.Files) == 0 {
			t.Fatalf("PR %q is incomplete", p.Branch)
		}
		if p.Beat == "B" {
			if p.Approve == nil {
				t.Fatalf("beat B PR %s says nothing about approval", p.Branch)
			}
			approve[*p.Approve]++
		}
	}
	if approve[true] != 1 || approve[false] != 1 {
		t.Errorf("beat B: %d auto-approve and %d routed PRs, want one each", approve[true], approve[false])
	}

	snap, err := config.LoadSnapshot(os.DirFS("../../exemplar-config"), "test")
	if err != nil {
		t.Fatal(err)
	}
	threshold, perFile := snap.Global.ClassThreshold(), snap.Global.CommentBudget.PerFile
	for _, p := range []DemoPR{volumePR(1), teachPR()} {
		total := 0
		for f, c := range p.Files {
			n := strings.Count(c, "TODO")
			if n > perFile {
				t.Errorf("%s: %d TODOs in %s, over comment_budget.per_file %d", p.Branch, n, f, perFile)
			}
			total += n
		}
		if total == 0 || total > threshold {
			t.Errorf("%s: %d TODOs, want 1 to posting.class_threshold %d", p.Branch, total, threshold)
		}
	}
	if !strings.HasPrefix(sortedKeys(teachPR().Files)[0], TeachPrefix+"/") {
		t.Errorf("teach PR is not under %s", TeachPrefix)
	}
}
