package engine

import (
	"context"
	"fmt"

	"github.com/koblas/exemplar/internal/scope"
)

// Candidate is a finding to verify on its own, as calibration does:
// the claim a generator would have made, without the generator.
type Candidate struct {
	RuleID    string
	Path      string
	Side      string // RIGHT (default) or LEFT
	StartLine int
	EndLine   int
	Claim     string
}

// Verified is the verification stage's decision on one candidate.
type Verified struct {
	Verdict    string // confirmed | refuted | uncertain
	Suppressed string // why it would not post; "" when it would
	Importance *float64
	Model      string
}

// VerifyOnly runs the verification stage (same prompt, batching, evidence
// resolution and importance threshold) on candidates in p.
func (e *Engine) VerifyOnly(ctx context.Context, p *Prepared, cands []Candidate, caller string) ([]Verified, []RunResult, error) {
	findings := make([]*Finding, len(cands))
	for i, c := range cands {
		var rule *ScopedRule
		for j := range p.Scoped {
			if p.Scoped[j].Rule.ID == c.RuleID {
				rule = &p.Scoped[j]
			}
		}
		if rule == nil {
			return nil, nil, fmt.Errorf("engine: rule %s is not in scope for %s", c.RuleID, c.Path)
		}
		side := scope.Side(c.Side)
		if side == "" {
			side = scope.SideRight
		}
		findings[i] = &Finding{
			RuleID: c.RuleID, Lane: LaneExemplar, Severity: rule.Rule.Severity, Category: rule.Rule.Category,
			CCLabel: ccLabel(rule.Rule.Severity, rule.Rule.Category), Message: c.Claim, State: StateCandidate,
			Anchor: scope.Anchor{Path: c.Path, Side: side, StartLine: c.StartLine, EndLine: c.EndLine},
		}
	}
	vrs := e.verify(ctx, p, findings, map[*Finding]string{}, CallMeta{Caller: caller, Repo: p.PR.Repo})
	out := make([]Verified, len(findings))
	for i, f := range findings {
		out[i] = Verified{Verdict: f.VerifyVerdict, Importance: f.VerifyImportance, Model: f.VerifyModel}
		if f.State == StateDropped {
			out[i].Suppressed = f.Suppressed
		}
	}
	runs := make([]RunResult, len(vrs))
	for i, vr := range vrs {
		runs[i] = vr.run
	}
	return out, runs, nil
}
