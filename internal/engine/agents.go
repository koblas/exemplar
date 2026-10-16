package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/koblas/exemplar/internal/llm"
	"github.com/koblas/exemplar/internal/scope"
)

// Output limits from the Violation contract.
const (
	maxViolations     = 20
	maxExplanationLen = 1500
	maxFixLen         = 1500
)

// agentRun is the result of one model lane run: the measured run plus the
// candidate violations that passed validation in code.
type agentRun struct {
	run        RunResult
	candidates []Finding
}

// runExemplar runs one exemplar rule agent.
func (e *Engine) runExemplar(ctx context.Context, p *Prepared, s ScopedRule, meta CallMeta) agentRun {
	meta.Stage, meta.Tier = llm.StageGenerator, s.Rule.ModelTier
	tb := e.toolbox(p)
	res := e.harness.Run(ctx, AgentSpec{
		Meta: meta, System: ruleSystemPrompt, Context: prContext(p), Task: ruleTask(s),
		OutputSchema: violationSchema, Tools: tb,
	})
	ar := agentRun{run: runFromAgent(s.Rule.ID, s.Rule.VersionSHA, LaneExemplar, s.Files, res)}
	ar.run.ContextDegraded = tb.contextDegraded()
	if res.Status != RunOK {
		return ar
	}
	violations, err := decodeViolations(res.Output)
	if err != nil {
		ar.run.Status, ar.run.Detail = RunSchemaError, err.Error()
		return ar
	}
	ar.run.Coverage = coverage(res.Calls, s.Files)
	if ar.run.Coverage < 1 {
		ar.run.Status = RunZeroCoverage
		ar.run.Detail = fmt.Sprintf("agent opened %d of %d files in scope", len(examined(res.Calls, s.Files)), len(s.Files))
	}
	cite := ""
	if s.Rule.Citation != nil {
		cite = s.Rule.Citation.Doc
	}
	for _, v := range violations {
		f := Finding{
			RuleID: s.Rule.ID, Lane: LaneExemplar, Severity: s.Rule.Severity, Category: s.Rule.Category,
			CCLabel: ccLabel(s.Rule.Severity, s.Rule.Category), Citation: cite,
			Anchor:  scope.Anchor{Path: v.Path, Side: scope.Side(v.Side), StartLine: v.StartLine, EndLine: v.EndLine},
			Message: strings.TrimSpace(v.Explanation), Fix: v.SuggestedFix, State: StateCandidate,
		}
		if !containsString(s.Files, v.Path) {
			f.State, f.Suppressed = StateDropped, SuppressBadAnchor
		}
		ar.candidates = append(ar.candidates, f)
	}
	return ar
}

// runBaseline runs the rule-less baseline reviewer.
func (e *Engine) runBaseline(ctx context.Context, p *Prepared, meta CallMeta) agentRun {
	meta.Stage, meta.Tier = llm.StageGenerator, llm.TierStandard
	tb := e.toolbox(p)
	res := e.harness.Run(ctx, AgentSpec{
		Meta: meta, System: baselineSystemPrompt, Context: prContext(p), Task: baselineTask(p),
		OutputSchema: violationSchema, Tools: tb,
	})
	ar := agentRun{run: runFromAgent("baseline", "baseline-v1", LaneBaseline, p.Files, res)}
	if res.Status != RunOK {
		return ar
	}
	violations, err := decodeViolations(res.Output)
	if err != nil {
		ar.run.Status, ar.run.Detail = RunSchemaError, err.Error()
		return ar
	}
	ar.run.Coverage = coverage(res.Calls, p.Files)
	for _, v := range violations {
		ar.candidates = append(ar.candidates, Finding{
			RuleID: "baseline", Lane: LaneBaseline, Severity: "suggestion", Category: "Baseline",
			CCLabel: "suggestion (baseline)",
			Anchor:  scope.Anchor{Path: v.Path, Side: scope.Side(v.Side), StartLine: v.StartLine, EndLine: v.EndLine},
			Message: strings.TrimSpace(v.Explanation), Fix: v.SuggestedFix, State: StateShadow,
		})
	}
	return ar
}

func runFromAgent(ruleID, version, lane string, files []string, res AgentResult) RunResult {
	return RunResult{
		RuleID: ruleID, RuleVersion: version, Lane: lane, ScopedFiles: files,
		Status: res.Status, Detail: res.Detail, Model: res.Model, Family: res.Family,
		Failover: res.Failover, Usage: res.Usage, CostUSD: res.CostUSD,
		LatencyMS: int(res.Latency.Milliseconds()), Budget: res.Budget,
	}
}

// decodeViolations decodes and validates an agent answer.
func decodeViolations(text string) ([]Violation, error) {
	var out AgentOutput
	if err := decodeStrict(text, &out); err != nil {
		return nil, fmt.Errorf("answer does not match the output contract: %w", err)
	}
	if err := validateViolations(out.Violations); err != nil {
		return nil, err
	}
	return out.Violations, nil
}

// validateViolations enforces the Violation contract's limits in code.
func validateViolations(vs []Violation) error {
	if len(vs) > maxViolations {
		return fmt.Errorf("answer has %d violations, limit %d", len(vs), maxViolations)
	}
	for i, v := range vs {
		switch {
		case v.Path == "":
			return fmt.Errorf("violation %d has no path", i)
		case v.Side != string(scope.SideRight) && v.Side != string(scope.SideLeft):
			return fmt.Errorf("violation %d has side %q", i, v.Side)
		case v.StartLine < 1 || v.EndLine < v.StartLine:
			return fmt.Errorf("violation %d has range %d-%d", i, v.StartLine, v.EndLine)
		case strings.TrimSpace(v.Explanation) == "" || utf8.RuneCountInString(v.Explanation) > maxExplanationLen:
			return fmt.Errorf("violation %d explanation is empty or too long", i)
		case utf8.RuneCountInString(v.SuggestedFix) > maxFixLen:
			return fmt.Errorf("violation %d suggested fix is too long", i)
		}
	}
	return nil
}

// coverage is the fraction of scoped files the agent opened.
func coverage(calls []ToolCall, scoped []string) float64 {
	if len(scoped) == 0 {
		return 0
	}
	return float64(len(examined(calls, scoped))) / float64(len(scoped))
}
