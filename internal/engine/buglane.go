package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/koblas/exemplar/internal/buglane"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/schema"
	"github.com/koblas/exemplar/internal/scope"
)

// The bug lane: one agent per enabled bug class, hunting novel logic bugs.

const bugSystemPrompt = `You look for one class of bug in one pull request. The class and what to look for are given in the <bug_class> block. Report ` +
	`only bugs of that class that the change introduces or exposes.

` + untrustedNotice + `

How to work:
- Call get_hunk for every file listed under "Files in scope" before you answer. Files you do not open are recorded as not reviewed.
- Use read_file, grep and list_dir to follow the code paths you need. Prefer a few targeted reads.
- Report a bug only when you can name a concrete trigger: an input, call sequence, state or path through the code that makes it happen. If you cannot, leave it out.
- Do not report style, naming, readability, performance preferences, missing tests, or "consider ..." suggestions. Those are out of scope even when true.

Anchors: point each bug at lines that appear in the diff. Use side RIGHT with new-file line numbers for added or unchanged lines, and side LEFT with ` +
	`old-file line numbers for deleted lines. start_line and end_line must fall in the same hunk.

Answer with JSON only: {"violations": [...]}. Each has path, side, start_line, end_line, explanation (what is wrong), trigger (the concrete input or ` +
	`path that causes it), impact (what goes wrong when it happens), and optionally suggested_fix: the complete replacement for lines start_line to ` +
	`end_line of the new file, with their original indentation and nothing from the lines around them, posted as a one-click suggestion. Leave it out ` +
	`when the fix needs changes elsewhere. Return {"violations": []} when there is no such bug.`

func bugTask(c buglane.Class, files []string) string {
	var b strings.Builder
	b.WriteString("<bug_class>\n")
	b.WriteString("Class: " + c.LooksFor + "\n\nWhat to look for:\n" + c.Guidance + "\n")
	b.WriteString("\nFiles in scope:\n")
	for _, f := range files {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("</bug_class>")
	return b.String()
}

// bugSchema is violationSchema plus the required trigger and impact.
var bugSchema = func() map[string]any {
	item := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"path", "side", "start_line", "end_line", "explanation", "trigger", "impact"},
		"properties": map[string]any{
			"path":          map[string]any{"type": "string"},
			"side":          map[string]any{"type": "string", "enum": []string{"RIGHT", "LEFT"}},
			"start_line":    map[string]any{"type": "integer"},
			"end_line":      map[string]any{"type": "integer"},
			"explanation":   map[string]any{"type": "string"},
			"trigger":       map[string]any{"type": "string"},
			"impact":        map[string]any{"type": "string"},
			"suggested_fix": map[string]any{"type": "string"},
		},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"violations"},
		"properties": map[string]any{"violations": map[string]any{"type": "array", "items": item}},
	}
}()

const maxTriggerLen = 500

// bugViolation is a Violation with the bug lane's required fields.
type bugViolation struct {
	Violation

	Trigger string `json:"trigger"`
	Impact  string `json:"impact"`
}

// decodeBugViolations decodes and validates a bug-lane answer. A finding
// without a concrete trigger or impact fails the whole answer, like any
// other schema violation.
func decodeBugViolations(text string) ([]bugViolation, error) {
	var out struct {
		Violations []bugViolation `json:"violations"`
	}
	if err := decodeStrict(text, &out); err != nil {
		return nil, fmt.Errorf("answer does not match the bug-lane contract: %w", err)
	}
	plain := make([]Violation, len(out.Violations))
	for i, v := range out.Violations {
		plain[i] = v.Violation
		switch {
		case strings.TrimSpace(v.Trigger) == "" || utf8.RuneCountInString(v.Trigger) > maxTriggerLen:
			return nil, fmt.Errorf("violation %d has no concrete trigger, or one over %d characters", i, maxTriggerLen)
		case strings.TrimSpace(v.Impact) == "" || utf8.RuneCountInString(v.Impact) > maxTriggerLen:
			return nil, fmt.Errorf("violation %d has no impact, or one over %d characters", i, maxTriggerLen)
		}
	}
	if err := validateViolations(plain); err != nil {
		return nil, err
	}
	return out.Violations, nil
}

// bugSeverity and bugCategory label every bug-lane finding.
const (
	bugSeverity = schema.SeverityImportant
	bugCategory = "Bug"
)

// runBug runs one bug-class agent over the reviewable files.
func (e *Engine) runBug(ctx context.Context, p *Prepared, c buglane.Class, meta CallMeta) agentRun {
	ar := agentRun{run: RunResult{RuleID: c.ID, RuleVersion: c.Version, Lane: LaneBug, ScopedFiles: p.Files}}
	if c.NeedsReferences {
		// This class is skipped whenever search cannot answer
		// find_references fully; a skip blocks auto-approve.
		if ok, why := p.searchUsable(); !ok {
			ar.run.Status, ar.run.Detail = RunDegradedSkip, why
			return ar
		}
		if p.search.degraded {
			ar.run.Status, ar.run.Detail = RunDegradedSkip, "exemplar-search is in degraded mode"
			return ar
		}
	}
	meta.Stage, meta.Tier = llm.StageGenerator, llm.TierHigh
	tb := e.toolbox(p)
	res := e.harness.Run(ctx, AgentSpec{
		Meta: meta, System: bugSystemPrompt, Context: prContext(p), Task: bugTask(c, p.Files),
		OutputSchema: bugSchema, Tools: tb,
	})
	ar.run = runFromAgent(c.ID, c.Version, LaneBug, p.Files, res)
	ar.run.ContextDegraded = tb.contextDegraded()
	if res.Status != RunOK {
		return ar
	}
	if c.NeedsReferences && ar.run.ContextDegraded {
		ar.run.Status, ar.run.Detail = RunDegradedSkip, "exemplar-search degraded or failed during the run"
	}
	violations, err := decodeBugViolations(res.Output)
	if err != nil {
		ar.run.Status, ar.run.Detail = RunSchemaError, err.Error()
		return ar
	}
	ar.run.Coverage = coverage(res.Calls, p.Files)
	if ar.run.Coverage < 1 {
		ar.run.Status = RunZeroCoverage
		ar.run.Detail = fmt.Sprintf("agent opened %d of %d files in scope", len(examined(res.Calls, p.Files)), len(p.Files))
	}
	for _, v := range violations {
		msg := strings.TrimSpace(v.Explanation) + "\n\nTrigger: " + strings.TrimSpace(v.Trigger) + "\nImpact: " + strings.TrimSpace(v.Impact)
		f := Finding{
			RuleID: c.ID, Lane: LaneBug, Severity: bugSeverity, Category: bugCategory,
			CCLabel: ccLabel(bugSeverity, bugCategory),
			Anchor:  scope.Anchor{Path: v.Path, Side: scope.Side(v.Side), StartLine: v.StartLine, EndLine: v.EndLine},
			Message: msg, Fix: v.SuggestedFix, State: StateCandidate,
		}
		if !containsString(p.Files, v.Path) {
			f.State, f.Suppressed = StateDropped, SuppressBadAnchor
		}
		ar.candidates = append(ar.candidates, f)
	}
	return ar
}

// bugGate applies the bug lane's risk gate to a prepared PR.
func (p *Prepared) bugGate(opt Options, candidate bool) buglane.Gate {
	in := buglane.GateInput{Files: p.Files, AutoApproveCandidate: candidate}
	for _, path := range p.Files {
		if gates.IsTestFile(path) {
			continue
		}
		if f, ok := p.Diff.File(path); ok {
			for _, h := range f.Hunks {
				in.NonTestChangedLines += h.Added + h.Deleted
			}
		}
	}
	if bl := p.Effective.RepoConfig.BugLane; bl != nil {
		in.CriticalPaths = bl.CriticalPaths
	}
	if bl := p.Effective.Global.BugLane; bl != nil {
		in.MinChangedLines = bl.MinChangedLines
	}
	if ms := opt.MergeSafety; ms != nil {
		score, recent := ms.Score, false
		for _, pf := range ms.Input.Paths {
			recent = recent || pf.Recent
		}
		in.MergeSafety, in.RecentIncident = &score, &recent
	}
	return buglane.Decide(in)
}

// bugClasses are the classes global.yaml enables.
func (p *Prepared) bugClasses() []buglane.Class {
	flags := map[string]bool{}
	if bl := p.Effective.Global.BugLane; bl != nil {
		for id, f := range bl.Classes {
			flags[id] = f.Enabled
		}
	}
	return buglane.Enabled(flags)
}

// bugPosts reports whether a bug finding may post: its class has
// post_on_critical_paths and the finding is in the repo's critical paths.
func (p *Prepared) bugPosts(f *Finding) bool {
	g, r := p.Effective.Global.BugLane, p.Effective.RepoConfig.BugLane
	if g == nil || r == nil || !g.Classes[f.RuleID].PostOnCriticalPaths {
		return false
	}
	return buglane.MatchAny(r.CriticalPaths, f.Anchor.Path)
}
