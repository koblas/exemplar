package scope

import (
	"math"
	"slices"
	"testing"
)

func ruleIDs(cs []Candidate) []string {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.RuleID
	}
	return ids
}

func TestApplyFanOutCap(t *testing.T) {
	in := []Candidate{
		{"low", 0.2},
		{"high", 0.9},
		{"tie-b", 0.5},
		{"tie-a", 0.5},
		{"unknown", math.NaN()},
	}
	tests := []struct {
		name         string
		limit        int
		wantRun      []string
		wantDeferred []string
	}{
		{"under limit", 10, []string{"high", "tie-a", "tie-b", "low", "unknown"}, []string{}},
		{"at limit", 5, []string{"high", "tie-a", "tie-b", "low", "unknown"}, []string{}},
		{"defers lowest precision", 2, []string{"high", "tie-a"}, []string{"tie-b", "low", "unknown"}},
		{"zero", 0, []string{}, []string{"high", "tie-a", "tie-b", "low", "unknown"}},
		{"negative treated as zero", -1, []string{}, []string{"high", "tie-a", "tie-b", "low", "unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := slices.Clone(in)
			run, deferred := ApplyFanOutCap(in, tt.limit)
			if got := ruleIDs(run); !equalStrings(got, tt.wantRun) {
				t.Errorf("run = %v, want %v", got, tt.wantRun)
			}
			if got := ruleIDs(deferred); !equalStrings(got, tt.wantDeferred) {
				t.Errorf("deferred = %v, want %v", got, tt.wantDeferred)
			}
			if !slices.EqualFunc(in, orig, func(a, b Candidate) bool { return a.RuleID == b.RuleID }) {
				t.Error("input slice was reordered")
			}
		})
	}
}

func TestApplyFanOutCapRunIsNotAliasedByDeferred(t *testing.T) {
	run, deferred := ApplyFanOutCap([]Candidate{{"a", 1}, {"b", 0.5}, {"c", 0.1}}, 1)
	_ = append(run, Candidate{"x", 0})
	if deferred[0].RuleID != "b" {
		t.Fatalf("appending to run overwrote deferred: %v", ruleIDs(deferred))
	}
}
