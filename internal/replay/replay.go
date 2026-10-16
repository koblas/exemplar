// Package replay reruns the review pipeline over recorded PRs. It drives
// the same engine as production with caller=eval, stores results when a
// database is configured, can post to a sandbox PR, runs the baseline
// reviewer in shadow, and compares findings with what humans said.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gh"
	"github.com/koblas/exemplar/internal/pipeline"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/synth"
)

// LoadFixtures reads every *.yaml fixture under dir.
func LoadFixtures(dir string) ([]schema.Fixture, error) {
	var out []schema.Fixture
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".yaml" {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var f schema.Fixture
		if err := schema.Decode(schema.DocFixture, data, &f); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, f := range out {
		if ids[f.ID] {
			return nil, fmt.Errorf("duplicate fixture id %s", f.ID)
		}
		ids[f.ID] = true
	}
	slices.SortFunc(out, func(a, b schema.Fixture) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Runner replays fixtures.
type Runner struct {
	Snapshot *config.Snapshot
	Engine   *engine.Engine
	Scanner  *redact.Scanner
	// WorkDir holds checkouts; each fixture gets a subdirectory.
	WorkDir string
	// Store, when set, persists reviews, rule runs, findings and gate traces.
	Store *store.Store
	// PosterFor, when set, returns a poster for a fixture repo; posted
	// findings then go to the fixture's real PR.
	PosterFor func(ctx context.Context, repo string) (*gh.Poster, error)
	// Baseline also runs the baseline reviewer in shadow.
	Baseline bool
	// CloneTokenFor returns a read-only token for real fixtures on GitHub.
	CloneTokenFor func(ctx context.Context, repo string) (string, error)
}

// Outcome summarizes one fixture.
type Outcome struct {
	Fixture    string   `json:"fixture"`
	Skipped    string   `json:"skipped,omitempty"`
	Incomplete string   `json:"incomplete,omitempty"`
	ReviewID   int64    `json:"review_id,omitempty"`
	Coverage   float64  `json:"coverage"`
	Verdict    string   `json:"verdict"`
	Stage1     []string `json:"stage1_reasons"`
	// Stage2 is set only when the deterministic gates passed.
	Stage2     []string           `json:"stage2_reasons,omitempty"`
	Decision   string             `json:"decision"`
	Runs       []RunSummary       `json:"runs"`
	Findings   []FindingOut       `json:"findings"`
	Posted     *PostOutcome       `json:"posted,omitempty"`
	Baseline   *BaselineOut       `json:"baseline,omitempty"`
	Comparison Comparison         `json:"comparison"`
	Excluded   []engine.Exclusion `json:"excluded,omitempty"`
}

// RunSummary is one rule run.
type RunSummary struct {
	Rule     string  `json:"rule"`
	Lane     string  `json:"lane"`
	Status   string  `json:"status"`
	Coverage float64 `json:"coverage"`
	Detail   string  `json:"detail,omitempty"`
	CostUSD  float64 `json:"cost_usd"`
}

// FindingOut is one finding.
type FindingOut struct {
	Rule       string `json:"rule"`
	Lane       string `json:"lane"`
	Path       string `json:"path"`
	Side       string `json:"side"`
	Start      int    `json:"start_line"`
	End        int    `json:"end_line"`
	State      string `json:"state"`
	Suppressed string `json:"suppressed,omitempty"`
	Verdict    string `json:"verify_verdict,omitempty"`
	Label      string `json:"label"`
	Message    string `json:"message"`
}

// PostOutcome records what was posted to GitHub.
type PostOutcome = pipeline.PostOutcome

// BaselineOut summarizes the baseline reviewer's shadow run.
type BaselineOut struct {
	ReviewID   int64        `json:"review_id,omitempty"`
	Coverage   float64      `json:"coverage"`
	Status     string       `json:"status"`
	Findings   []FindingOut `json:"findings"`
	Comparison Comparison   `json:"comparison"`
}

// Comparison matches findings to human comments by path and nearby line.
type Comparison struct {
	HumanComments int `json:"human_comments"`
	Findings      int `json:"findings"`
	Matched       int `json:"matched_findings"`
	HumansCovered int `json:"human_comments_covered"`
}

// Run replays one fixture.
func (r *Runner) Run(ctx context.Context, fx schema.Fixture) (*Outcome, error) {
	out := &Outcome{Fixture: fx.ID}
	dir := filepath.Join(r.WorkDir, fx.ID)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	cloneURL, base, head, err := Materialize(ctx, fx, filepath.Join(dir, "source"))
	if err != nil {
		return nil, err
	}
	token := ""
	if fx.Synthetic == nil && r.CloneTokenFor != nil {
		if token, err = r.CloneTokenFor(ctx, fx.Repo); err != nil {
			return nil, err
		}
	}
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "worktree"), engine.CheckoutOptions{
		CloneURL: cloneURL, BaseSHA: base, HeadSHA: head, Token: token,
	})
	if err != nil {
		return nil, err
	}
	pr := engine.PRInfo{Repo: fx.Repo, Number: fx.PR, Title: fx.Title, Body: fx.Body, Author: fx.Author, Draft: fx.Draft, BaseSHA: base, HeadSHA: head}
	p, err := engine.Prepare(ctx, ws, r.Snapshot, pr, r.Scanner)
	if errors.Is(err, engine.ErrIneligible) {
		out.Skipped = err.Error()
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.Excluded = p.Excluded

	rec, err := pipeline.Begin(ctx, r.Store, p, "eval", 1, p.Scoped)
	if err != nil {
		return nil, err
	}
	res, err := r.Engine.Review(ctx, p, engine.Options{ReviewID: rec.ReviewID, Caller: "eval", RuleRunIDs: rec.RunIDs})
	if err != nil {
		return nil, err
	}
	if err := pipeline.Finish(ctx, r.Store, rec, res); err != nil {
		return nil, err
	}
	fill(out, rec.ReviewID, res)
	out.Comparison = compare(fx.HumanComments, res.Findings, false)

	if r.PosterFor != nil && fx.Synthetic == nil && res.Incomplete == "" {
		poster, err := r.PosterFor(ctx, fx.Repo)
		if err != nil {
			return out, fmt.Errorf("post: %w", err)
		}
		po, err := pipeline.Post(ctx, r.Store, poster, p, rec, res, 0)
		if err != nil {
			return out, fmt.Errorf("post: %w", err)
		}
		out.Posted = po
	}

	if r.Baseline {
		brec, err := pipeline.Begin(ctx, r.Store, p, "baseline", 1, nil)
		if err != nil {
			return nil, err
		}
		bres, err := r.Engine.Baseline(ctx, p, engine.Options{ReviewID: brec.ReviewID, Caller: "baseline", RuleRunIDs: brec.RunIDs})
		if err != nil {
			return nil, err
		}
		if err := pipeline.Finish(ctx, r.Store, brec, bres); err != nil {
			return nil, err
		}
		b := &BaselineOut{ReviewID: brec.ReviewID, Coverage: bres.Coverage, Comparison: compare(fx.HumanComments, bres.Findings, true)}
		if len(bres.Runs) > 0 {
			b.Status = bres.Runs[0].Status
		}
		for _, f := range bres.Findings {
			b.Findings = append(b.Findings, findingOut(f))
		}
		out.Baseline = b
	}
	return out, nil
}

// Materialize prepares fx's repository: synthetic fixtures are built in
// dir; recorded ones return their clone URL and commits.
func Materialize(ctx context.Context, fx schema.Fixture, dir string) (cloneURL, base, head string, err error) {
	if fx.Synthetic != nil {
		repo, err := synth.Build(ctx, dir, fx.Synthetic.Base, fx.Synthetic.Head, fx.Synthetic.Deleted)
		if err != nil {
			return "", "", "", err
		}
		return repo.Dir, repo.BaseSHA, repo.HeadSHA, nil
	}
	url := fx.CloneURL
	if url == "" {
		url = "https://github.com/" + fx.Repo + ".git"
	}
	return url, fx.BaseSHA, fx.HeadSHA, nil
}

func fill(out *Outcome, reviewID int64, res *engine.Result) {
	out.ReviewID = reviewID
	out.Coverage = res.Coverage
	out.Verdict = res.Verdict
	out.Stage1 = res.Stage1.Reasons
	if res.Stage2 != nil {
		out.Stage2 = res.Stage2.Reasons
	}
	out.Decision = res.Decision()
	out.Incomplete = res.Incomplete
	for _, r := range res.Runs {
		out.Runs = append(out.Runs, RunSummary{Rule: r.RuleID, Lane: r.Lane, Status: r.Status, Coverage: r.Coverage, Detail: r.Detail, CostUSD: r.CostUSD})
	}
	for _, f := range res.Findings {
		out.Findings = append(out.Findings, findingOut(f))
	}
}

func findingOut(f engine.Finding) FindingOut {
	return FindingOut{
		Rule: f.RuleID, Lane: f.Lane, Path: f.Anchor.Path, Side: string(f.Anchor.Side),
		Start: f.Anchor.StartLine, End: f.Anchor.EndLine, State: f.State, Suppressed: f.Suppressed,
		Verdict: f.VerifyVerdict, Label: f.CCLabel, Message: f.Message,
	}
}

// compare matches findings to human comments: same path, and the human
// comment's line within three lines of the finding's range. For the
// production lanes only posted findings count; the baseline counts all
// of its shadow findings.
func compare(humans []schema.HumanComment, findings []engine.Finding, shadow bool) Comparison {
	c := Comparison{HumanComments: len(humans)}
	covered := make([]bool, len(humans))
	for _, f := range findings {
		counts := f.State == engine.StatePosted || (shadow && f.State == engine.StateShadow)
		if !counts {
			continue
		}
		c.Findings++
		hit := false
		for i, h := range humans {
			if h.Path == f.Anchor.Path && h.Line >= f.Anchor.StartLine-3 && h.Line <= f.Anchor.EndLine+3 {
				hit = true
				covered[i] = true
			}
		}
		if hit {
			c.Matched++
		}
	}
	for _, v := range covered {
		if v {
			c.HumansCovered++
		}
	}
	return c
}

// MarshalOutcomes renders outcomes as indented JSON.
func MarshalOutcomes(outs []*Outcome) ([]byte, error) {
	return json.MarshalIndent(outs, "", "  ")
}
