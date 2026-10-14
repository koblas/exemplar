package gates

import (
	"math"
	"slices"
	"testing"
)

func passing() Stage2Input {
	return Stage2Input{
		Repo:           "acme/app",
		Owners:         &OwnersResult{Pass: true},
		BranchRules:    &BranchRulesResult{Pass: true},
		MergeSafety:    &MergeSafetyResult{Score: 0.7},
		Categorization: &Stage2Categorization{ReviewConfidence: 0.8},
	}
}

func TestEvaluateStage2(t *testing.T) {
	complianceRepos["acme/ledger"] = true
	t.Cleanup(func() { delete(complianceRepos, "acme/ledger") })
	tests := []struct {
		name     string
		edit     func(*Stage2Input)
		decision string
		reasons  []string
	}{
		{"all at the bars", func(*Stage2Input) {}, DecisionApproved, nil},
		{"owners fail", func(in *Stage2Input) { in.Owners = &OwnersResult{} }, DecisionIneligible, []string{ReasonOwners}},
		{"branch rules fail", func(in *Stage2Input) { in.BranchRules = &BranchRulesResult{} }, DecisionIneligible, []string{ReasonBranchRules}},
		{"merge safety below", func(in *Stage2Input) { in.MergeSafety.Score = 0.65 }, DecisionIneligible, []string{ReasonMergeSafetyBelowBar}},
		{"merge safety NaN", func(in *Stage2Input) { in.MergeSafety.Score = math.NaN() }, DecisionIneligible, []string{ReasonMergeSafetyBelowBar}},
		{"confidence below", func(in *Stage2Input) { in.Categorization.ReviewConfidence = 0.79 }, DecisionIneligible, []string{ReasonConfidenceBelowBar}},
		{"repo raises merge bar", func(in *Stage2Input) { in.MinMergeSafety = 0.9 }, DecisionIneligible, []string{ReasonMergeSafetyBelowBar}},
		{"repo raises confidence bar", func(in *Stage2Input) { in.MinReviewConfidence = 0.95 }, DecisionIneligible, []string{ReasonConfidenceBelowBar}},
		{"a lower configured bar is not honored", func(in *Stage2Input) {
			in.MinMergeSafety, in.MinReviewConfidence = 0.1, 0.1
			in.MergeSafety.Score, in.Categorization.ReviewConfidence = 0.5, 0.5
		}, DecisionIneligible, []string{ReasonMergeSafetyBelowBar, ReasonConfidenceBelowBar}},
		{"nothing measured", func(in *Stage2Input) { *in = Stage2Input{Repo: in.Repo} }, DecisionIneligible,
			[]string{ReasonOwnersNotChecked, ReasonBranchRulesNotChecked, ReasonMergeSafetyNotScored, ReasonCategorizationNotRun}},
		{"categorizer skipped", func(in *Stage2Input) { in.Owners = &OwnersResult{}; in.Categorization = nil }, DecisionIneligible,
			[]string{ReasonOwners, ReasonCategorizationNotRun}},
		{"compliance, one family", func(in *Stage2Input) {
			in.Repo = "acme/ledger"
			in.Categorization = &Stage2Categorization{NotIndependent: true}
		}, DecisionBlockedCompliance, []string{ReasonConfidenceBelowBar}},
		{"compliance, independent but medium", func(in *Stage2Input) {
			in.Repo = "acme/ledger"
			in.Categorization = &Stage2Categorization{}
		}, DecisionIneligible, []string{ReasonConfidenceBelowBar}},
		{"one family, not compliance", func(in *Stage2Input) { in.Categorization = &Stage2Categorization{NotIndependent: true} }, DecisionIneligible,
			[]string{ReasonConfidenceBelowBar}},
	}
	for _, tt := range tests {
		in := passing()
		tt.edit(&in)
		got := EvaluateStage2(in)
		if got.Decision != tt.decision || got.Pass != (tt.decision == DecisionApproved) || !slices.Equal(got.Reasons, tt.reasons) && len(got.Reasons)+len(tt.reasons) > 0 {
			t.Errorf("%s: %+v", tt.name, got)
		}
	}
	got := EvaluateStage2(passing())
	if got.MergeSafety == nil || *got.MergeSafety != 0.7 || got.MergeSafetyBar != 0.7 || got.ReviewConfidenceBar != 0.8 {
		t.Errorf("trace = %+v", got)
	}
}

func TestDecide(t *testing.T) {
	pass := EvaluateStage2(passing())
	if Decide(Stage1Result{Eligible: true}, &pass) != DecisionApproved {
		t.Error("both stages passed but not approved")
	}
	if Decide(Stage1Result{}, &pass) != DecisionIneligible {
		t.Error("a Stage 2 pass approved a Stage 1 failure")
	}
	if Decide(Stage1Result{Eligible: true}, nil) != DecisionIneligible {
		t.Error("approved without Stage 2")
	}
}

func TestReviewConfidenceIndependence(t *testing.T) {
	if s := ReviewConfidence(side("a", RiskLow, 0.9, "x"), side("b", RiskLow, 0.9, "x")); !s.NotIndependent {
		t.Error("same family not flagged")
	}
	if s := ReviewConfidence(side("a", RiskLow, 0.9, ""), side("b", RiskLow, 0.9, "y")); !s.NotIndependent {
		t.Error("unknown family not flagged")
	}
	if s := ReviewConfidence(side("a", RiskHigh, 0.1, "x"), side("b", RiskLow, 0.9, "y")); s.NotIndependent {
		t.Error("independent pair flagged")
	}
	// A failed side is an outage, not a property of the provider pair.
	if s := ReviewConfidence(Categorization{Side: "a"}, side("b", RiskLow, 0.9, "y")); s.NotIndependent {
		t.Error("failed side flagged as not independent")
	}
}
