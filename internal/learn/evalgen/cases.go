// Package evalgen generates eval cases for candidates:
// positives from the source comments, negatives from CODEOWNER-approved PRs
// with no comment from the cluster, and injected violations, where a model
// makes the minimal edit that introduces the violation into a negative
// case and code validates it (the patch applies, the file still parses,
// the diff is at most 15 lines).
//
// Cases reference code as repo@sha plus a path and line range; no code is
// copied into exemplar-config except an injected case's small patch.
package evalgen

import (
	"fmt"
	"regexp"
	"strings"
)

// Kinds of generated case.
const (
	KindPositive = "positive"
	KindNegative = "negative"
	KindInjected = "injected"
)

// Expect is a finding the rule must produce overlapping Lines (inclusive).
type Expect struct {
	Path  string `json:"path"`
	Lines [2]int `json:"lines"`
}

// Case is one generated eval case.
type Case struct {
	ID     string
	Kind   string
	Name   string
	Ref    string // repo@sha
	Patch  string // injected only
	Expect []Expect
	// Paths are the files to run the rule on; negatives list the PR's
	// in-scope files (positives use their Expect paths).
	Paths     []string
	SpotCheck bool
}

// PositiveSource is a candidate source comment with its location.
type PositiveSource struct {
	CommentID  int64
	Repo       string
	PR         int
	Path       string
	CommitSHA  string
	StartLine  int
	EndLine    int
	CommentURL string
}

// MaxCasePaths caps the files a case runs on (schema: paths maxItems 20).
const MaxCasePaths = 10

var shaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Positives returns one case per source comment that records where it pointed.
// Comments without a commit or line range (older ingest data) give no case.
func Positives(sources []PositiveSource) []Case {
	var out []Case
	seen := map[string]bool{}
	for _, s := range sources {
		if !shaRE.MatchString(s.CommitSHA) || s.StartLine < 1 {
			continue
		}
		end := s.EndLine
		if end < s.StartLine {
			end = s.StartLine
		}
		key := fmt.Sprintf("%s@%s:%s#%d-%d", s.Repo, s.CommitSHA, s.Path, s.StartLine, end)
		if seen[key] {
			continue
		}
		seen[key] = true
		id, name := fmt.Sprintf("p-%d", s.CommentID), fmt.Sprintf("review comment on %s#%d", s.Repo, s.PR)
		if s.CommentID < 0 { // revert seed: the reverted change is the positive
			id, name = fmt.Sprintf("p-r%d", -s.CommentID), fmt.Sprintf("reverted change in %s#%d", s.Repo, s.PR)
		}
		out = append(out, Case{
			ID:     id,
			Kind:   KindPositive,
			Name:   name,
			Ref:    s.Repo + "@" + s.CommitSHA,
			Expect: []Expect{{Path: s.Path, Lines: [2]int{s.StartLine, end}}},
		})
	}
	return out
}

// NegativePR is a merged, CODEOWNER-approved PR with no cluster comment.
type NegativePR struct {
	Repo    string
	PR      int
	HeadSHA string
	BaseSHA string
	Files   []string
}

// Matcher reports whether a path is in the rule's scope.
type Matcher interface{ Match(path string) bool }

// Negatives returns cases for PRs that touched at least one in-scope file, up to limit.
// It also returns, per case, the first in-scope file (for injection).
func Negatives(prs []NegativePR, scope Matcher, limit int) ([]Case, map[string]InjectTarget) {
	var out []Case
	targets := map[string]InjectTarget{}
	for _, p := range prs {
		if len(out) >= limit {
			break
		}
		if !shaRE.MatchString(p.HeadSHA) {
			continue
		}
		var inScope []string
		for _, f := range p.Files {
			if scope.Match(f) && len(inScope) < MaxCasePaths {
				inScope = append(inScope, f)
			}
		}
		if len(inScope) == 0 {
			continue
		}
		id := "n-" + slug(p.Repo) + "-" + fmt.Sprint(p.PR)
		out = append(out, Case{
			ID:     id,
			Kind:   KindNegative,
			Name:   fmt.Sprintf("approved PR %s#%d, no comment from this cluster", p.Repo, p.PR),
			Ref:    p.Repo + "@" + p.HeadSHA,
			Expect: []Expect{},
			Paths:  inScope,
		})
		targets[id] = InjectTarget{Repo: p.Repo, PR: p.PR, SHA: p.HeadSHA, Path: inScope[0]}
	}
	return out, targets
}

// InjectTarget is a file at a commit where a violation may be injected.
type InjectTarget struct {
	Repo string
	PR   int
	SHA  string
	Path string
}

// MarkSpotChecks flags ceil(10%) of the synthetic cases for owner review
// the first ones in order so the choice is stable.
func MarkSpotChecks(cases []Case) {
	var synthetic []int
	for i, c := range cases {
		if c.Kind == KindInjected {
			synthetic = append(synthetic, i)
		}
	}
	n := (len(synthetic) + 9) / 10
	for _, i := range synthetic[:n] {
		cases[i].SpotCheck = true
	}
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
