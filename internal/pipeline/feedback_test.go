package pipeline

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/schema"
)

func TestSuggestionAt(t *testing.T) {
	file := strings.Split("package a\n\nfunc f() {\n\tx := compute(ctx)\n\t_ = x\n}\n\nfunc g() {\n\t}\n}", "\n")
	tests := []struct {
		name  string
		fix   []string
		start int
		want  bool
	}{
		{"at the anchor", []string{"\tx := compute(ctx)", "\t_ = x"}, 4, true},
		{"moved down by other edits", []string{"\tx := compute(ctx)"}, 2, true},
		{"not there", []string{"\tx := other(ctx)"}, 4, false},
		{"short line away from the anchor", []string{"\t}"}, 7, false},
		{"short line at the anchor", []string{"}"}, 6, true},
		{"past the end", []string{"}", "extra"}, 10, false},
	}
	for _, tt := range tests {
		if got := suggestionAt(file, tt.fix, tt.start); got != tt.want {
			t.Errorf("%s = %v", tt.name, got)
		}
	}
	// Found twice near the anchor: ambiguous, so not counted.
	twice := []string{"\tx := compute(ctx)", "y", "\tx := compute(ctx)"}
	if suggestionAt(twice, []string{"\tx := compute(ctx)"}, 2) {
		t.Error("ambiguous match counted")
	}
}

func TestRoleWeight(t *testing.T) {
	for role, want := range map[string]float32{RoleAuthor: 1, RoleReviewer: 2, RoleOwner: 4, RoleObserver: 0, "stranger": 0} {
		if got := RoleWeight(nil, role); got != want {
			t.Errorf("default %s = %v", role, got)
		}
	}
	cfg := &schema.FeedbackConfig{RoleWeights: map[string]float64{"reviewer": 3}}
	if RoleWeight(cfg, RoleReviewer) != 3 || RoleWeight(cfg, RoleOwner) != 4 {
		t.Error("configured weights not used, or defaults lost")
	}
}
