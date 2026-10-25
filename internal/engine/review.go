package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/koblas/exemplar/internal/buglane"
	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/pathhistory"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// Engine runs reviews.
type Engine struct {
	harness  *Harness
	scanner  *redact.Scanner
	compiled compiled.Runner
	history  PathHistory
	refs     References
	// local answers find_references and path_history from each review's
	// checkout when exemplar-search is not configured (refs nil);
	// localEvents adds path_events to its history (nil: commits only).
	local       bool
	localEvents *pathhistory.Service
	// judged caches linter-dedupe verdicts per pair.
	judged judgeCache
}

// New returns an Engine.
func New(h *Harness, s *redact.Scanner, c compiled.Runner) *Engine {
	return &Engine{harness: h, scanner: s, compiled: c}
}

// WithLocalContext answers find_references and path_history from each
// review's own checkout, for installs without exemplar-search. events
// supplies incidents and reverts from path_events (nil: commits only).
// A configured exemplar-search (WithReferences) takes precedence.
func (e *Engine) WithLocalContext(events *pathhistory.Service) *Engine {
	e.local, e.localEvents = true, events
	return e
}

// WithPathHistory offers agents the path_history tool, answered by h.
func (e *Engine) WithPathHistory(h PathHistory) *Engine {
	e.history = h
	return e
}

// Options identify a review run.
type Options struct {
	ReviewID int64
	// Caller is reviews.caller: production, eval, baseline, ...
	Caller string
	// VerifierCalibrated is set when the installation's verifier
	// calibration passed for the deployed verifier inputs: same-family
	// verification then does not gate (FR-E10).
	VerifierCalibrated bool
	// RuleRunIDs, when set, maps rule id to its persisted rule_runs.id so
	// gateway calls are attributed per rule run.
	RuleRunIDs map[string]int64
	// MergeSafety, when the caller measured it (internal/mergesafety), is
	// logged in the deterministic-gate trace and gates the approval checks.
	MergeSafety *gates.MergeSafetyResult
	// BranchRules looks up the base branch's merge requirements for
	// the approval checks (gh.BranchRules with a gate token). Nil means no
	// lookup, and the approval checks then fail closed.
	BranchRules func(ctx context.Context) (gates.BranchRules, error)
	// Carry offers rule results to carry forward instead of re-running
	// (interdiff scoping), by rule id. The caller offers only
	// exemplar rules whose scoped files are unchanged since a complete
	// review on the same base. Review uses an offer only when the rule
	// version matches, and ignores them all for an auto-approve candidate:
	// the deterministic gates need every lane to have run.
	Carry map[string]CarriedRun
	// CarryFrom is the head the offers come from, for the check run.
	CarryFrom string
	// PostedFingerprints are the fingerprints earlier production reviews
	// of this PR posted and that are still open. A finding with one is the
	// same finding: it is not posted again and does not count against the
	// comment budget.
	PostedFingerprints map[string]bool
	// TeamMember reports whether login is in team ("@org/team"), for
	// teams in auto_approve.allowed_authors (gh.TeamMember). Nil, or an
	// error, leaves membership unknown, which fails the gates for authors
	// allowed only through a team.
	TeamMember func(ctx context.Context, team, login string) (bool, error)
	// Linters returns the annotations of the repo's configured linter
	// check runs on the head, for linter dedupe; the caller
	// waits up to linter_wait_seconds for them to report. Review calls it
	// at most once, and only when a finding could otherwise post or be
	// stored as would-have-posted. Nil, or an error, dedupes nothing.
	Linters func(ctx context.Context) ([]LinterAnnotation, error)
	// HumanComments returns the PR's existing review comments, for
	// human-comment dedupe. Review calls it at most once, and
	// only when a finding could post. Nil, or an error, dedupes nothing.
	HumanComments func(ctx context.Context) ([]HumanComment, error)
	// Compiled, when set, runs this review's compiled rules instead of the
	// engine's runner: rule bodies nobody merged yet (the playground) run
	// under resource limits (compiled.LimitCommand), merged rules do not.
	Compiled CompiledMatcher
}

// CompiledMatcher runs one compiled rule body (compiled.Runner).
type CompiledMatcher interface {
	Run(ctx context.Context, dir, engine, ruleBody string, files []string) ([]compiled.Match, error)
}

// CarriedRun is an earlier, complete run of a rule on identical code.
type CarriedRun struct {
	// FromRunID is the rule run that did the work.
	FromRunID int64
	Version   string
	// Candidates are that run's candidate findings; Gating are those of
	// blocking or important severity, which a clean run may not have.
	Candidates int
	Gating     int
}

// Result is a finished review.
type Result struct {
	Runs     []RunResult
	Findings []Finding
	Coverage float64
	Degraded bool
	// Incomplete, when set, means nothing may be posted.
	Incomplete string
	Stage1     gates.Stage1Result
	// CarriedFrom is the head carried rules come from ("" for none).
	CarriedFrom string
	// BugLane is the bug lane's risk-gate decision.
	BugLane *buglane.Gate
	// Stage2 is nil unless the deterministic gates passed.
	Stage2  *gates.Stage2Result
	Verdict string // APPROVE or NO_OP
	// Held back by the comment budget, for the check run.
	HeldBack int
	// Linters is what linter dedupe did; nil when it did not run.
	Linters *DedupeTrace
	// Humans is what human-comment dedupe did; nil when it did not run.
	Humans *DedupeTrace
	// SearchOff is set when no exemplar-search is configured: agents had
	// no find_references or path_history, and no caller in another
	// repository was checked.
	SearchOff bool
}

// dedupers are assemble's model-judged dedupes; either may be nil.
type dedupers struct {
	linters *linterDeduper
	humans  *humanDeduper
}

// Verdicts.
const (
	VerdictApprove = "APPROVE"
	VerdictNoOp    = "NO_OP"
)

// Review runs every scoped rule and assembles the result.
func (e *Engine) Review(ctx context.Context, p *Prepared, opt Options) (*Result, error) {
	res := &Result{Verdict: VerdictNoOp}
	if p.Incomplete != "" {
		res.Incomplete = p.Incomplete
		res.Stage1 = gates.EvaluateStage1(gates.Stage1Input{})
		return res, nil
	}
	meta := CallMeta{Caller: opt.Caller, Repo: p.PR.Repo, ReviewID: opt.ReviewID}
	e.pinSearch(ctx, p)
	res.SearchOff = e.refs == nil

	// The auto-approve pre-check: the deterministic gates that do not
	// depend on findings. A candidate forces the bug lane.
	policy := gates.CheckPolicy(p.policyInput(ctx, opt))
	aa := p.Effective.RepoConfig.AutoApprove
	candidate := p.Effective.Global.AutoApprove && aa != nil && aa.Enabled && !p.Effective.RepoConfig.Shadow &&
		policy.PathsOK && policy.AuthorOK && policy.NotFork && policy.SizeOK
	gate := p.bugGate(opt, candidate)
	res.BugLane = &gate
	var bugClasses []buglane.Class
	if gate.Run {
		bugClasses = p.bugClasses()
	}

	// Lane runs. One failed rule never fails the review; it is recorded
	// with its status and counts against coverage. Bug classes share the
	// concurrency slots, start after the rule agents, and are the first work shed
	// once any call reports the run's budget past its soft ceiling.
	runs := make([]agentRun, len(p.Scoped)+len(bugClasses))
	var overBudget atomic.Bool
	note := func(ar agentRun) agentRun {
		if ar.run.Budget != "" && ar.run.Budget != llm.BudgetOK {
			overBudget.Store(true)
		}
		return ar
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, p.Effective.Global.Concurrency.AgentsPerReview))
	carriedCandidates, carriedGating := 0, 0
	for i, s := range p.Scoped {
		if c, ok := opt.Carry[s.Rule.ID]; ok && !candidate && s.Lane == LaneExemplar && c.Version == s.Rule.VersionSHA && c.FromRunID != 0 {
			runs[i] = agentRun{run: RunResult{
				RuleID: s.Rule.ID, RuleVersion: s.Rule.VersionSHA, Lane: LaneExemplar, ScopedFiles: s.Files,
				Status: RunOK, Coverage: 1, Carried: true, CarriedFrom: c.FromRunID,
			}}
			carriedCandidates += c.Candidates
			if isSecurity(s.Rule.Category) {
				carriedGating += c.Candidates
			} else {
				carriedGating += c.Gating
			}
			res.CarriedFrom = opt.CarryFrom
			continue
		}
		m := meta
		m.RuleRunID = opt.RuleRunIDs[s.Rule.ID]
		g.Go(func() error {
			switch s.Lane {
			case LaneCompiled:
				runs[i] = e.runCompiled(gctx, p, s, opt.Compiled)
			default:
				runs[i] = note(e.runExemplar(gctx, p, s, m))
			}
			return nil
		})
	}
	for j, c := range bugClasses {
		i := len(p.Scoped) + j
		m := meta
		m.RuleRunID = opt.RuleRunIDs[c.ID]
		g.Go(func() error {
			if overBudget.Load() {
				runs[i] = agentRun{run: RunResult{
					RuleID: c.ID, RuleVersion: c.Version, Lane: LaneBug, ScopedFiles: p.Files,
					Status: RunShed, Detail: "shed: the run reached its soft cost ceiling",
				}}
				return nil
			}
			runs[i] = note(e.runBug(gctx, p, c, m))
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("engine: review: %w", err) // superseded or shut down: the job retries or is cancelled
	}

	// Deferred rules are recorded and keep coverage below 100%.
	for _, d := range p.Deferred {
		res.Runs = append(res.Runs, RunResult{RuleID: d.Rule.ID, RuleVersion: d.Rule.VersionSHA, Lane: d.Lane, ScopedFiles: d.Files, Status: RunDeferred})
		res.Degraded = true
	}
	secretRun := RunResult{RuleID: SecretRuleID, RuleVersion: "builtin", Lane: LaneSecrets, ScopedFiles: p.Files, Status: RunOK, Coverage: 1}

	var all []*Finding
	genFamily := map[*Finding]string{}
	for i := range runs {
		res.Runs = append(res.Runs, runs[i].run)
		for j := range runs[i].candidates {
			f := &runs[i].candidates[j]
			f.runIndex = len(res.Runs) - 1
			genFamily[f] = runs[i].run.Family
			all = append(all, f)
		}
		if runs[i].run.Budget != "" && runs[i].run.Budget != llm.BudgetOK || runs[i].run.Status == RunShed {
			res.Degraded = true
		}
		if strings.Contains(runs[i].run.Detail, ErrProvidersUnavailable.Error()) {
			res.Incomplete = "every model provider was unavailable"
		}
	}
	res.Runs = append(res.Runs, secretRun)
	for i := range p.Secrets {
		f := p.Secrets[i]
		f.runIndex = len(res.Runs) - 1
		all = append(all, &f)
	}

	// Auto-approve counts candidates before verification (system rule 6):
	// a clean run has none of blocking or important severity and no
	// Security one (FR-A13). Carried rules count their earlier candidates.
	candidates, candidatesAll := carriedGating, len(all)+carriedCandidates
	for _, f := range all {
		if gating(f) {
			candidates++
		}
	}

	// Validate anchors in code; invalid anchors are dropped, never moved.
	for _, f := range all {
		if f.State == StateCandidate {
			if err := p.Diff.ValidateAnchor(f.Anchor); err != nil {
				f.State, f.Suppressed = StateDropped, SuppressBadAnchor
			}
		}
	}

	// Verify model-lane findings. Compiled and secret findings are
	// deterministic and skip the verifier.
	var toVerify []*Finding
	for _, f := range all {
		if f.State == StateCandidate && (f.Lane == LaneExemplar || f.Lane == LaneBug) {
			toVerify = append(toVerify, f)
		}
	}
	sameFamily := false
	if len(toVerify) > 0 {
		for _, vr := range e.verify(ctx, p, toVerify, genFamily, meta) {
			res.Runs = append(res.Runs, vr.run)
			if strings.Contains(vr.run.Detail, ErrProvidersUnavailable.Error()) {
				res.Incomplete = "every verifier provider was unavailable"
			}
		}
		for _, f := range toVerify {
			sameFamily = sameFamily || f.VerifySameFamily
		}
	}

	var linters *linterDeduper
	if len(p.Effective.RepoConfig.Linters) > 0 {
		linters = newLinterDeduper(e, p.Effective.Rules, meta, opt.Linters)
	}
	humans := newHumanDeduper(e, meta, p.PR.Author, opt.HumanComments)
	e.assemble(ctx, p, all, opt.PostedFingerprints, dedupers{linters: linters, humans: humans}, res)
	if linters != nil && linters.loaded {
		res.Linters = linters.trace
	}
	if humans != nil && humans.loaded {
		res.Humans = humans.trace
	}

	res.Coverage = reviewCoverage(res.Runs)
	res.Stage1 = evaluateStage1(gates.Stage1Input{
		Complete:               res.Incomplete == "",
		Coverage:               res.Coverage,
		Candidates:             candidates,
		CandidatesAll:          candidatesAll,
		Unreviewed:             len(p.Excluded),
		Degraded:               res.Degraded,
		SameFamilyVerification: sameFamily,
		VerifierCalibrated:     opt.VerifierCalibrated,
		BugLaneCovered:         bugLaneCovered(res.Runs),
		GlobalAutoApprove:      p.Effective.Global.AutoApprove,
		RepoAutoApprove:        p.Effective.RepoConfig.AutoApprove != nil && p.Effective.RepoConfig.AutoApprove.Enabled,
		RepoShadow:             p.Effective.RepoConfig.Shadow,
		PathPolicy:             policy.PathsOK,
		AuthorPolicy:           policy.AuthorOK,
		NotFork:                policy.NotFork,
		WithinSize:             policy.SizeOK,
		Files:                  policy.Files,
		ChangedLines:           policy.ChangedLines,
	})
	res.Stage1.MergeSafety = opt.MergeSafety
	res.Stage1.Policy = &policy
	if res.Stage1.Eligible {
		res.Stage2 = e.stage2(ctx, p, opt, meta, res)
	}
	if res.Decision() == gates.DecisionApproved {
		res.Verdict = VerdictApprove
	}
	if res.Incomplete != "" {
		for i := range res.Findings {
			if res.Findings[i].State == StatePosted {
				res.Findings[i].State = StateShadow
			}
		}
	}
	return res, nil
}

// runCompiled runs one compiled rule. Matches are deterministic findings.
func (e *Engine) runCompiled(ctx context.Context, p *Prepared, s ScopedRule, m CompiledMatcher) agentRun {
	ar := agentRun{run: RunResult{RuleID: s.Rule.ID, RuleVersion: s.Rule.VersionSHA, Lane: LaneCompiled, ScopedFiles: s.Files}}
	if s.Rule.Matcher == nil {
		ar.run.Status, ar.run.Detail = RunError, "compiled rule has no matcher"
		return ar
	}
	if m == nil {
		m = e.compiled
	}
	matches, err := m.Run(ctx, p.Workspace.Dir, s.Rule.Matcher.Engine, s.Rule.Matcher.Rule, s.Files)
	if err != nil {
		ar.run.Status, ar.run.Detail = RunError, err.Error()
		if errors.Is(err, compiled.ErrEngineUnavailable) {
			ar.run.Status = RunDegradedSkip
		}
		return ar
	}
	ar.run.Status, ar.run.Coverage = RunOK, 1
	cite := ""
	if s.Rule.Citation != nil {
		cite = s.Rule.Citation.Doc
	}
	for _, m := range matches {
		ar.candidates = append(ar.candidates, Finding{
			RuleID: s.Rule.ID, Lane: LaneCompiled, Severity: s.Rule.Severity, Category: s.Rule.Category,
			CCLabel: ccLabel(s.Rule.Severity, s.Rule.Category), Citation: cite,
			Anchor:  scope.Anchor{Path: m.Path, Side: scope.SideRight, StartLine: m.StartLine, EndLine: m.EndLine},
			Message: strings.TrimSpace(s.Rule.Description), State: StateCandidate,
		})
	}
	return ar
}

// assemble dedupes, fingerprints, applies the comment budget and decides
// which findings post. Only active rules post; shadow rules are stored.
func (e *Engine) assemble(ctx context.Context, p *Prepared, all []*Finding, posted map[string]bool, dd dedupers, res *Result) {
	status, fixPolicy := map[string]string{}, map[string]string{}
	for _, r := range p.Effective.Rules {
		status[r.ID] = r.Status
		fixPolicy[r.ID] = r.FixPolicy
	}
	// Validate fixes of findings that post or are stored as shadow (to
	// measure fix quality before rules post), after the budget so dropped
	// findings cost nothing.
	defer func() {
		for i := range res.Findings {
			f := &res.Findings[i]
			if f.Fix != "" && (f.State == StatePosted || f.State == StateShadow) {
				f.FixProblem = e.checkFix(ctx, p, f, fixPolicy)
			}
		}
	}()
	status[SecretRuleID] = schema.StatusActive

	// Stable order: severity, path, line, rule.
	slices.SortStableFunc(all, func(a, b *Finding) int {
		return cmp.Or(
			cmp.Compare(severityRank(a.Severity), severityRank(b.Severity)),
			cmp.Compare(a.Anchor.Path, b.Anchor.Path),
			cmp.Compare(a.Anchor.StartLine, b.Anchor.StartLine),
			cmp.Compare(a.RuleID, b.RuleID),
		)
	})

	fp := newFingerprinter(p.Workspace)
	exact := map[string]bool{}
	perFile := map[string]int{}
	perPR := 0
	budget := p.Effective.Global.CommentBudget
	for _, f := range all {
		f.Fingerprint, f.AnchorCode, f.Ordinal = fp.identify(ctx, f.RuleID, f.Anchor)
		if f.State != StateCandidate {
			res.Findings = append(res.Findings, *f)
			continue
		}
		key := fmt.Sprintf("%s|%s|%s|%d|%d", f.RuleID, f.Anchor.Path, f.Anchor.Side, f.Anchor.StartLine, f.Anchor.EndLine)
		if exact[key] {
			f.State, f.Suppressed = StateDropped, SuppressDuplicate
			res.Findings = append(res.Findings, *f)
			continue
		}
		exact[key] = true
		if posted[f.Fingerprint] {
			f.State, f.Suppressed = StateDropped, SuppressAlreadyPosted
			res.Findings = append(res.Findings, *f)
			continue
		}
		// Before the budget, so a duplicate takes no slot. A reviewer's
		// comment first: that duplicate counts as agreement.
		if c, dup := dd.humans.duplicate(ctx, f); dup {
			f.State, f.Suppressed, f.HumanMatch = StateDropped, SuppressHumanDuplicate, &c
			res.Findings = append(res.Findings, *f)
			continue
		}
		if _, dup := dd.linters.duplicate(ctx, f); dup {
			f.State, f.Suppressed = StateDropped, SuppressLinterDuplicate
			res.Findings = append(res.Findings, *f)
			continue
		}
		// Bug classes are not config rules. The bug lane is in shadow unless
		// its class may post on critical paths and the finding is in one.
		if f.Lane == LaneBug && !p.bugPosts(f) || f.Lane != LaneBug && status[f.RuleID] != schema.StatusActive {
			f.State = StateShadow
			res.Findings = append(res.Findings, *f)
			continue
		}
		// Security and blocking findings are exempt from the budget.
		exempt := f.Severity == schema.SeverityBlocking || isSecurity(f.Category)
		if !exempt && (perPR >= budget.PerPR || perFile[f.Anchor.Path] >= budget.PerFile) {
			f.State, f.Suppressed = StateDropped, SuppressBudget
			res.HeldBack++
			res.Findings = append(res.Findings, *f)
			continue
		}
		if !exempt {
			perPR++
			perFile[f.Anchor.Path]++
		}
		f.State = StatePosted
		if p.Effective.RepoConfig.Shadow {
			// It would have posted; the repo is in shadow.
			f.State, f.Suppressed = StateShadow, SuppressRepoShadow
		}
		res.Findings = append(res.Findings, *f)
	}
}

func severityRank(s string) int {
	switch s {
	case schema.SeverityBlocking:
		return 0
	case schema.SeverityImportant:
		return 1
	}
	return 2
}

// reviewCoverage is completed in-scope rules ÷ in-scope rules.
// Verifier runs are not rules and do not count.
func reviewCoverage(runs []RunResult) float64 {
	total, done := 0, 0
	for _, r := range runs {
		if r.Lane == "verifier" {
			continue
		}
		total++
		if r.Status == RunOK && r.Coverage >= 1 {
			done++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(done) / float64(total)
}

// Baseline runs the rule-less baseline reviewer in shadow. Its
// findings are stored and never posted.
func (e *Engine) Baseline(ctx context.Context, p *Prepared, opt Options) (*Result, error) {
	res := &Result{Verdict: VerdictNoOp}
	if p.Incomplete != "" {
		res.Incomplete = p.Incomplete
		return res, nil
	}
	ar := e.runBaseline(ctx, p, CallMeta{Caller: "baseline", Repo: p.PR.Repo, ReviewID: opt.ReviewID, RuleRunID: opt.RuleRunIDs["baseline"]})
	res.Runs = []RunResult{ar.run}
	fp := newFingerprinter(p.Workspace)
	for i := range ar.candidates {
		f := ar.candidates[i]
		if err := p.Diff.ValidateAnchor(f.Anchor); err != nil {
			f.State, f.Suppressed = StateDropped, SuppressBadAnchor
		}
		f.Fingerprint, f.AnchorCode, f.Ordinal = fp.identify(ctx, f.RuleID, f.Anchor)
		res.Findings = append(res.Findings, f)
	}
	res.Coverage = ar.run.Coverage
	res.Stage1 = gates.EvaluateStage1(gates.Stage1Input{})
	return res, nil
}

// isSecurity reports whether category is Security.
func isSecurity(category string) bool { return strings.EqualFold(category, "Security") }

// gating reports whether f keeps a run from being clean: blocking or
// important severity, or Security at any severity (FR-A13).
func gating(f *Finding) bool {
	return f.Severity == schema.SeverityBlocking || f.Severity == schema.SeverityImportant || isSecurity(f.Category)
}
