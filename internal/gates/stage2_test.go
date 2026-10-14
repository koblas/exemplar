package gates

import (
	"strings"
	"testing"
)

func side(s, risk string, p float64, family string) Categorization {
	return Categorization{Side: s, Risk: risk, PLow: p, Family: family, OK: true}
}

func TestReviewConfidence(t *testing.T) {
	tests := []struct {
		name string
		a, b Categorization
		want float64
		why  string
	}{
		{"both low", side("a", RiskLow, 0.93, "anthropic"), side("b", RiskLow, 0.88, "openai"), 0.88, ""},
		{"one medium", side("a", RiskLow, 0.95, "anthropic"), side("b", RiskMedium, 0.4, "openai"), 0, ""},
		{"both high", side("a", RiskHigh, 0.1, "anthropic"), side("b", RiskHigh, 0.05, "openai"), 0, ""},
		{"same family", side("a", RiskLow, 0.99, "anthropic"), side("b", RiskLow, 0.99, "anthropic"), 0, "independent"},
		{"unknown family", side("a", RiskLow, 0.99, ""), side("b", RiskLow, 0.99, "openai"), 0, "unknown"},
		{"a failed", Categorization{Side: "a", Detail: "timeout"}, side("b", RiskLow, 0.99, "openai"), 0, "timeout"},
	}
	for _, tt := range tests {
		got := ReviewConfidence(tt.a, tt.b)
		if got.ReviewConfidence != tt.want || (tt.why != "" && !strings.Contains(got.Why, tt.why)) || (tt.why == "" && got.Why != "") {
			t.Errorf("%s: %+v", tt.name, got)
		}
	}
}
