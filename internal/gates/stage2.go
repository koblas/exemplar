package gates

import (
	"fmt"
	"math"
)

// Stage 2 low-risk categorization (spec §6.13): provider A and provider B
// independently categorize the PR; code combines their answers.

// Risk levels a categorizer returns.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// DefaultMinReviewConfidence is the Stage 2 bar (spec §6.13). A repo's
// auto_approve.min_review_confidence can only raise it.
const DefaultMinReviewConfidence = 0.8

// Categorization is one side's answer and how the call went.
type Categorization struct {
	Side    string   `json:"side"` // a | b
	Risk    string   `json:"risk,omitempty"`
	PLow    float64  `json:"p_low"`
	Reasons []string `json:"reasons,omitempty"`
	Model   string   `json:"model,omitempty"`
	Family  string   `json:"family,omitempty"`
	// OK is false when the call failed or its answer broke the schema;
	// Detail says why.
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Stage2Categorization is both sides and the combined confidence, as
// logged in approval_decisions.stage2.
type Stage2Categorization struct {
	A                Categorization `json:"a"`
	B                Categorization `json:"b"`
	ReviewConfidence float64        `json:"review_confidence"`
	// Why is set when the confidence is 0 for a reason other than a side
	// judging the PR not low risk.
	Why string `json:"why,omitempty"`
	// NotIndependent is set when the sides' families are the same or
	// unknown: no answer from this pair could ever approve.
	NotIndependent bool `json:"not_independent,omitempty"`
}

// ReviewConfidence combines the two sides: min(p_low_A, p_low_B) when both
// answered low, else 0. It is also 0 when either side failed or both came
// from the same model family, since Stage 2 needs two independent judgments.
func ReviewConfidence(a, b Categorization) Stage2Categorization {
	s := Stage2Categorization{A: a, B: b}
	switch {
	case !a.OK || !b.OK:
		s.Why = fmt.Sprintf("a side did not answer validly (a: %s, b: %s)", detail(a), detail(b))
	case a.Family == "" || b.Family == "":
		s.Why, s.NotIndependent = "a side's model family is unknown", true
	case a.Family == b.Family:
		s.Why = fmt.Sprintf("both sides are the %s family; Stage 2 needs two independent providers", a.Family)
		s.NotIndependent = true
	case a.Risk == RiskLow && b.Risk == RiskLow:
		s.ReviewConfidence = math.Round(math.Min(a.PLow, b.PLow)*1000) / 1000
	}
	return s
}

func detail(c Categorization) string {
	if c.OK {
		return "ok"
	}
	if c.Detail == "" {
		return "failed"
	}
	return c.Detail
}
