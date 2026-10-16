// Package evalrun evaluates rules against their eval cases:
// compiled rules by running the engine, exemplar rules by the full lane
// and verifier. It scores each case (a positive or synthetic case is hit
// when a kept finding overlaps an expected range; a negative with any kept
// finding is a false positive), reports historical and synthetic cases
// separately, and gates on global.yaml eval_thresholds.
//
// An eval case is a commit, not a PR, so an exemplar rule reviews a
// synthesized PR that adds the case's files as they are at the commit
// (with the case's patch applied for synthetic cases). The rule sees whole
// files, not the original hunk, and context tools see only those files.
package evalrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gitexec"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/synth"
)

// Limits on what one case materializes.
const (
	maxCaseFiles = 20
	maxFileBytes = 1 << 20
)

// Case is one resolved eval case.
type Case struct {
	Name      string
	Repo      string
	SHA       string
	Synthetic bool
	Patch     string // the diff itself, read from the config tree
	Paths     []string
	Expect    []schema.Expectation
}

// Cases resolves a rule's eval cases: refs are split, patch files are read
// from evals/<rule>/ in cfg, and Paths default to the expected paths.
func Cases(cfg fs.FS, ec schema.EvalCases) ([]Case, error) {
	var out []Case
	for _, c := range ec.Cases {
		repo, sha, ok := strings.Cut(c.Ref, "@")
		if !ok {
			return nil, fmt.Errorf("evalrun: case %q: bad ref %q", c.Name, c.Ref)
		}
		rc := Case{Name: c.Name, Repo: repo, SHA: sha, Synthetic: c.Synthetic, Paths: slices.Clone(c.Paths), Expect: c.Expect}
		if c.Patch != "" {
			p := path.Join("evals", ec.Rule, c.Patch)
			if !strings.HasPrefix(p, path.Join("evals", ec.Rule)+"/") {
				return nil, fmt.Errorf("evalrun: case %q: patch %q escapes evals/%s", c.Name, c.Patch, ec.Rule)
			}
			data, err := fs.ReadFile(cfg, p)
			if err != nil {
				return nil, fmt.Errorf("evalrun: case %q: %w", c.Name, err)
			}
			rc.Patch = string(data)
		}
		if len(rc.Paths) == 0 {
			for _, e := range c.Expect {
				if !slices.Contains(rc.Paths, e.Path) {
					rc.Paths = append(rc.Paths, e.Path)
				}
			}
		}
		out = append(out, rc)
	}
	return out, nil
}

// FileSource reads a file at a commit (evalgen.CheckoutFiles in production).
type FileSource interface {
	FileAt(ctx context.Context, repo, sha, path string) ([]byte, error)
}

// Matcher runs compiled rules (compiled.Runner).
type Matcher interface {
	Run(ctx context.Context, dir, engine, ruleBody string, files []string) ([]compiled.Match, error)
}

// Evaluator runs rules on their cases.
type Evaluator struct {
	// Snapshot is the config whose rules are evaluated (a config PR's head).
	Snapshot *config.Snapshot
	// Eligible is the base branch's eligibility check, so a PR cannot
	// un-denylist a repo to evaluate on it. Required.
	Eligible func(repo string) bool
	Engine   *engine.Engine // exemplar rules
	Matcher  Matcher        // compiled rules
	Files    FileSource
	Scanner  *redact.Scanner
	WorkDir  string
}

// Metrics are case-level counts and rates.
type Metrics struct {
	Cases     int     `json:"cases"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
	TN        int     `json:"tn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}

func (m *Metrics) add(result string) {
	m.Cases++
	switch result {
	case "tp":
		m.TP++
	case "fp":
		m.FP++
	case "fn":
		m.FN++
	case "tn":
		m.TN++
	}
}

func (m *Metrics) finish() {
	if m.TP+m.FP > 0 {
		m.Precision = float64(m.TP) / float64(m.TP+m.FP)
	}
	if m.TP+m.FN > 0 {
		m.Recall = float64(m.TP) / float64(m.TP+m.FN)
	}
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}
	m.Precision, m.Recall, m.F1 = round3(m.Precision), round3(m.Recall), round3(m.F1)
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// Outcome is one case's result.
type Outcome struct {
	Case      string `json:"case"`
	Synthetic bool   `json:"synthetic"`
	Expected  bool   `json:"expected_finding"`
	Result    string `json:"result"` // tp | fp | fn | tn | unavailable
	Findings  int    `json:"findings"`
	Detail    string `json:"detail,omitempty"`
}

// Report is one rule's evaluation.
type Report struct {
	Rule       string    `json:"rule"`
	Kind       string    `json:"kind"`
	Historical Metrics   `json:"historical"`
	Synthetic  Metrics   `json:"synthetic"`
	All        Metrics   `json:"all"`
	CostUSD    float64   `json:"cost_usd"`
	Outcomes   []Outcome `json:"outcomes"`
	Pass       bool      `json:"pass"`
	Reasons    []string  `json:"reasons,omitempty"`
}

// Rule evaluates one rule on its cases.
func (e *Evaluator) Rule(ctx context.Context, rule config.Rule, cases []Case) (Report, error) {
	rep := Report{Rule: rule.ID, Kind: rule.Kind}
	if e.Eligible == nil {
		return rep, errors.New("evalrun: an eligibility check is required")
	}
	for i, c := range cases {
		o := Outcome{Case: c.Name, Synthetic: c.Synthetic, Expected: len(c.Expect) > 0}
		if !e.Eligible(c.Repo) {
			o.Result, o.Detail = "unavailable", "repo is not eligible"
			rep.Outcomes = append(rep.Outcomes, o)
			continue
		}
		findings, cost, err := e.runCase(ctx, rule, c, filepath.Join(e.WorkDir, fmt.Sprintf("%s-%03d", rule.ID, i)))
		rep.CostUSD += cost
		var un unavailable
		switch {
		case errors.As(err, &un):
			o.Result, o.Detail = "unavailable", un.reason
			rep.Outcomes = append(rep.Outcomes, o)
			continue
		case err != nil:
			return rep, fmt.Errorf("evalrun: %s case %q: %w", rule.ID, c.Name, err)
		}
		o.Findings = len(findings)
		switch {
		case !o.Expected && len(findings) > 0:
			o.Result = "fp"
		case !o.Expected:
			o.Result = "tn"
		case overlaps(findings, c.Expect):
			o.Result = "tp"
		default:
			o.Result = "fn"
		}
		if c.Synthetic {
			rep.Synthetic.add(o.Result)
		} else {
			rep.Historical.add(o.Result)
		}
		rep.All.add(o.Result)
		rep.Outcomes = append(rep.Outcomes, o)
	}
	rep.Historical.finish()
	rep.Synthetic.finish()
	rep.All.finish()
	return rep, nil
}

// Gate applies eval_thresholds to a report.
func Gate(rep *Report, t schema.EvalThresholds) {
	rep.Reasons = nil
	if rep.All.Cases < t.MinCases {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("%d scored cases, below the minimum %d", rep.All.Cases, t.MinCases))
	}
	if rep.All.Precision < t.Precision {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("precision %.2f below %.2f", rep.All.Precision, t.Precision))
	}
	if rep.All.Recall < t.Recall {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("recall %.2f below %.2f", rep.All.Recall, t.Recall))
	}
	rep.Pass = len(rep.Reasons) == 0
}

type unavailable struct{ reason string }

func (u unavailable) Error() string { return u.reason }

// span is a finding's location.
type span struct {
	path       string
	start, end int
}

func overlaps(fs []span, expect []schema.Expectation) bool {
	for _, f := range fs {
		for _, e := range expect {
			if len(e.Lines) == 2 && f.path == e.Path && f.start <= e.Lines[1] && f.end >= e.Lines[0] {
				return true
			}
		}
	}
	return false
}

// runCase returns the rule's kept findings on the case.
func (e *Evaluator) runCase(ctx context.Context, rule config.Rule, c Case, dir string) ([]span, float64, error) {
	defer func() { _ = os.RemoveAll(dir) }()
	files, err := e.materialize(ctx, c, filepath.Join(dir, "files"))
	if err != nil {
		return nil, 0, err
	}
	switch rule.Kind {
	case schema.KindCompiled:
		if rule.Matcher == nil {
			return nil, 0, errors.New("compiled rule has no matcher")
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		ms, err := e.Matcher.Run(ctx, filepath.Join(dir, "files"), rule.Matcher.Engine, rule.Matcher.Rule, paths)
		if err != nil {
			return nil, 0, err
		}
		out := make([]span, len(ms))
		for i, m := range ms {
			out[i] = span{m.Path, m.StartLine, m.EndLine}
		}
		return out, 0, nil
	case schema.KindExemplar, schema.KindBugClass:
		return e.review(ctx, rule, c, files, dir)
	}
	return nil, 0, fmt.Errorf("rule kind %s is not evaluated here", rule.Kind)
}

// review runs the exemplar lane and verifier on a PR that adds files.
func (e *Evaluator) review(ctx context.Context, rule config.Rule, c Case, files map[string]string, dir string) ([]span, float64, error) {
	repo, err := synth.Build(ctx, filepath.Join(dir, "repo"), map[string]string{}, files, nil)
	if err != nil {
		return nil, 0, err
	}
	ws, err := engine.Checkout(ctx, filepath.Join(dir, "wt"), engine.CheckoutOptions{CloneURL: repo.Dir, BaseSHA: repo.BaseSHA, HeadSHA: repo.HeadSHA})
	if err != nil {
		return nil, 0, err
	}
	pr := engine.PRInfo{Repo: c.Repo, Title: "eval: " + c.Name, BaseSHA: repo.BaseSHA, HeadSHA: repo.HeadSHA}
	p, err := engine.Prepare(ctx, ws, e.Snapshot, pr, e.Scanner)
	if errors.Is(err, engine.ErrIneligible) {
		return nil, 0, unavailable{"repo is not eligible"}
	}
	if err != nil {
		return nil, 0, err
	}
	p.Scoped = slices.DeleteFunc(p.Scoped, func(s engine.ScopedRule) bool { return s.Rule.ID != rule.ID })
	p.Deferred, p.Secrets = nil, nil
	if rule.Kind == schema.KindBugClass {
		// Only this class runs, and its lane always does: every file is a
		// critical path. The snapshot's settings are copied, not changed.
		bl := schema.GlobalBugLane{}
		if g := p.Effective.Global.BugLane; g != nil {
			bl = *g
		}
		bl.Classes = map[string]schema.BugLaneFlag{rule.ID: {Enabled: true}}
		p.Effective.Global.BugLane = &bl
		p.Effective.RepoConfig.BugLane = &schema.RepoBugLane{CriticalPaths: []string{"**"}}
	} else if len(p.Scoped) == 0 {
		return nil, 0, unavailable{"the rule's file_patterns match none of the case's files"}
	}
	if p.Incomplete != "" {
		return nil, 0, unavailable{"incomplete: " + p.Incomplete}
	}
	res, err := e.Engine.Review(ctx, p, engine.Options{Caller: "eval"})
	if err != nil {
		return nil, 0, err
	}
	cost := 0.0
	for _, r := range res.Runs {
		if r.RuleID == rule.ID {
			cost += r.CostUSD
			if r.Status != engine.RunOK {
				return nil, cost, unavailable{"rule run " + r.Status}
			}
		}
	}
	var out []span
	for _, f := range res.Findings {
		if f.RuleID == rule.ID && f.State != engine.StateDropped {
			out = append(out, span{f.Anchor.Path, f.Anchor.StartLine, f.Anchor.EndLine})
		}
	}
	return out, cost, nil
}

// materialize writes the case's files at its commit, with the patch
// applied, under dir and returns their contents by path.
func (e *Evaluator) materialize(ctx context.Context, c Case, dir string) (map[string]string, error) {
	paths := c.Paths
	if len(paths) > maxCaseFiles {
		paths = paths[:maxCaseFiles]
	}
	files := map[string]string{}
	for _, p := range paths {
		if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.HasPrefix(p, "../") || p == ".." {
			return nil, unavailable{fmt.Sprintf("path %q is not clean", p)}
		}
		data, err := e.Files.FileAt(ctx, c.Repo, c.SHA, p)
		if err != nil {
			return nil, unavailable{fmt.Sprintf("cannot read %s@%s:%s", c.Repo, c.SHA, p)}
		}
		if len(data) > maxFileBytes {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			return nil, err
		}
		files[p] = string(data)
	}
	if len(files) == 0 {
		return nil, unavailable{"no files to evaluate"}
	}
	if c.Patch != "" {
		pf := filepath.Join(filepath.Dir(dir), "case.patch")
		if err := os.WriteFile(pf, []byte(c.Patch), 0o600); err != nil {
			return nil, err
		}
		env := append(gitexec.Env(""), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		if _, err := gitexec.Run(ctx, dir, env, "apply", "--", pf); err != nil {
			return nil, unavailable{"patch does not apply: " + err.Error()}
		}
		for p := range files {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil {
				return nil, err
			}
			files[p] = string(data)
		}
	}
	return files, nil
}
