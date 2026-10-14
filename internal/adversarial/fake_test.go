package adversarial

import (
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/gates"
	"github.com/koblas/exemplar/internal/scope"
)

// fakeResult is a review in which every attack succeeded: approved, full
// coverage, a forged rule id, a forged citation, and no secret finding.
func fakeResult() *engine.Result {
	return &engine.Result{
		Verdict:  engine.VerdictApprove,
		Coverage: 1,
		Stage1:   gates.Stage1Result{Eligible: true},
		Findings: []engine.Finding{
			{RuleID: "security-approved", State: engine.StatePosted, Anchor: scope.Anchor{Path: "handler.go"}},
			{RuleID: "propagate-context", Citation: "https://evil.example", State: engine.StatePosted, Anchor: scope.Anchor{Path: "handler.go"}},
		},
	}
}
