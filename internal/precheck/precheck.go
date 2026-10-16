// Package precheck runs the config-PR checks that sit beside eval:
// the pre-launch volume estimate and linter overlap.
//
// Volume estimate: on a config PR that adds or activates a rule, run it
// alone over a sample of PRs merged in the last 30 days in the repos it
// applies to, and report the share of PRs it would comment on and the
// comments per PR after the comment budget. PRs should come from the
// replay harness's stored tuples; until production reviews are stored,
// PRs come from GitHub, sampled to bound model cost.
//
// Linter overlap: for each positive history case, the annotations the
// repo's configured linters (repos/<org>/<repo>.yaml linters) reported on
// that commit are compared with the expected lines; any overlap fails the
// check, naming the linter and its rule. This reads what the repo's CI
// already reported (the same source linter dedupe uses) rather
// than re-running linters outside the repo's build. Synthetic cases have
// no CI runs and are skipped.
package precheck

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/evalrun"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/playground"
	"github.com/koblas/exemplar/internal/schema"
)

// AnnotationSource reads linter annotations on a commit (gh.Annotations).
type AnnotationSource interface {
	Annotations(ctx context.Context, repo, sha string, checks []string) ([]gh.Annotation, error)
}

// Overlap is a linter annotation on a positive case's expected lines.
type Overlap struct {
	Case   string `json:"case"`
	Repo   string `json:"repo"`
	Linter string `json:"linter"`
	Rule   string `json:"linter_rule"`
	Path   string `json:"path"`
	Start  int    `json:"start_line"`
	End    int    `json:"end_line"`
}

// OverlapReport is one rule's linter-overlap check.
type OverlapReport struct {
	Rule     string    `json:"rule"`
	Checked  int       `json:"checked"`
	Skipped  []string  `json:"skipped,omitempty"` // case: reason
	Overlaps []Overlap `json:"overlaps"`
	Pass     bool      `json:"pass"`
}

// LinterOverlap checks rule's positive cases against linter annotations.
// eligible is the base branch's eligibility check.
func LinterOverlap(ctx context.Context, src AnnotationSource, snap *config.Snapshot, eligible func(string) bool, rule string, cases []evalrun.Case) (OverlapReport, error) {
	rep := OverlapReport{Rule: rule, Overlaps: []Overlap{}}
	skip := func(c evalrun.Case, why string) { rep.Skipped = append(rep.Skipped, c.Name+": "+why) }
	for _, c := range cases {
		switch {
		case len(c.Expect) == 0:
			continue // negatives expect nothing, so nothing can overlap
		case c.Synthetic:
			skip(c, "synthetic (no CI ran on it)")
			continue
		case !eligible(c.Repo):
			skip(c, "repo is not eligible")
			continue
		}
		linters := snap.Repos[c.Repo].Linters
		if len(linters) == 0 {
			skip(c, "repo configures no linters")
			continue
		}
		anns, err := src.Annotations(ctx, c.Repo, c.SHA, linters)
		if errors.Is(err, gh.ErrNotFound) {
			skip(c, "commit or check runs not found")
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("precheck: annotations for %s@%s: %w", c.Repo, c.SHA, err)
		}
		rep.Checked++
		for _, a := range anns {
			for _, e := range c.Expect {
				if len(e.Lines) == 2 && a.Path == e.Path && a.Start <= e.Lines[1] && a.End >= e.Lines[0] {
					rep.Overlaps = append(rep.Overlaps, Overlap{
						Case: c.Name, Repo: c.Repo, Linter: a.Check, Rule: a.Title,
						Path: a.Path, Start: a.Start, End: a.End,
					})
					break
				}
			}
		}
	}
	rep.Pass = len(rep.Overlaps) == 0
	return rep, nil
}

// PRSource lists recently merged PRs (gh.MergedPRs).
type PRSource interface {
	MergedPRs(ctx context.Context, repo string, since time.Time, limit int) ([]gh.PullRequest, [][]string, error)
}

// PRRunner runs rules alone on a PR (playground.Runner).
type PRRunner interface {
	RunPR(ctx context.Context, snap *config.Snapshot, ruleIDs []string, pr gh.PullRequest) (playground.Trial, error)
}

// VolumeConfig bounds the estimate.
type VolumeConfig struct {
	Days    int // window (default 30)
	PerRepo int // PRs listed per repo (default 10)
	MaxRuns int // rule runs across all repos (default 40)
	Now     func() time.Time
}

// VolumeReport is one rule's volume estimate.
type VolumeReport struct {
	Rule    string   `json:"rule"`
	Repos   []string `json:"repos"`
	Sampled int      `json:"sampled_prs"`
	InScope int      `json:"in_scope_prs"` // the rule's file_patterns match a changed file
	Ran     int      `json:"ran"`
	// Affected PRs would get at least one comment from the rule.
	Affected      int     `json:"affected_prs"`
	Comments      int     `json:"comments"`
	ShareAffected float64 `json:"share_affected"`
	CommentsPerPR float64 `json:"comments_per_pr"`
	// MaxPerPR is the most comments one PR would get.
	MaxPerPR int      `json:"max_comments_on_one_pr"`
	CostUSD  float64  `json:"cost_usd"`
	Notes    []string `json:"notes,omitempty"`
}

// Volume estimates rule's comment volume. snap is the config PR's head;
// eligible is the base branch's check.
func Volume(ctx context.Context, src PRSource, trial PRRunner, snap *config.Snapshot, eligible func(string) bool, rule config.Rule, cfg VolumeConfig) (VolumeReport, error) {
	if cfg.Days <= 0 {
		cfg.Days = 30
	}
	if cfg.PerRepo <= 0 {
		cfg.PerRepo = 10
	}
	if cfg.MaxRuns <= 0 {
		cfg.MaxRuns = 40
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	rep := VolumeReport{Rule: rule.ID, Repos: []string{}}
	for repo, rc := range snap.Repos {
		if eligible(repo) && rc.Enabled && !slices.Contains(rc.DisabledGlobalRules, rule.ID) {
			rep.Repos = append(rep.Repos, repo)
		}
	}
	slices.Sort(rep.Repos)
	since := cfg.Now().AddDate(0, 0, -cfg.Days)
	budget := snap.Global.CommentBudget
	exempt := strings.EqualFold(rule.Category, "Security") || rule.Severity == schema.SeverityBlocking
	for _, repo := range rep.Repos {
		prs, files, err := src.MergedPRs(ctx, repo, since, cfg.PerRepo)
		if err != nil {
			return rep, fmt.Errorf("precheck: PRs of %s: %w", repo, err)
		}
		for i, pr := range prs {
			rep.Sampled++
			if len(rule.Patterns.Filter(files[i])) == 0 {
				continue
			}
			rep.InScope++
			if rep.Ran == cfg.MaxRuns {
				continue
			}
			tr, err := trial.RunPR(ctx, snap, []string{rule.ID}, pr)
			if err != nil {
				return rep, err
			}
			if tr.Result == nil {
				continue // ineligible, incomplete, or excluded files only
			}
			rep.Ran++
			n := 0
			perFile := map[string]int{}
			for _, run := range tr.Result.Runs {
				if run.RuleID == rule.ID {
					rep.CostUSD += run.CostUSD
				}
			}
			for _, f := range tr.Result.Findings {
				if f.RuleID != rule.ID || f.State == engine.StateDropped {
					continue
				}
				// The comment budget caps what is posted, except for
				// Security and blocking findings.
				if !exempt && (perFile[f.Anchor.Path] >= budget.PerFile || n >= budget.PerPR) {
					continue
				}
				perFile[f.Anchor.Path]++
				n++
			}
			rep.Comments += n
			rep.MaxPerPR = max(rep.MaxPerPR, n)
			if n > 0 {
				rep.Affected++
			}
		}
	}
	if rep.InScope > rep.Ran {
		rep.Notes = append(rep.Notes, fmt.Sprintf("ran on %d of %d in-scope PRs (cap %d); rates use the PRs it ran on", rep.Ran, rep.InScope, cfg.MaxRuns))
	}
	// Rates over the PRs the rule could have commented on: every sampled PR
	// out of scope, plus the in-scope ones it ran on.
	if denom := rep.Sampled - rep.InScope + rep.Ran; denom > 0 {
		rep.ShareAffected = round3(float64(rep.Affected) / float64(denom))
		rep.CommentsPerPR = round3(float64(rep.Comments) / float64(denom))
	}
	return rep, nil
}

func round3(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }
