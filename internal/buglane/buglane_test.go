package buglane

import (
	"slices"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestDecide(t *testing.T) {
	quiet := GateInput{Files: []string{"svc/a.go"}, NonTestChangedLines: 10, MergeSafety: ptr(0.9), RecentIncident: ptr(false)}
	tests := []struct {
		name     string
		edit     func(*GateInput)
		run      bool
		triggers []string
	}{
		{"quiet PR", func(*GateInput) {}, false, nil},
		{"critical path", func(in *GateInput) { in.CriticalPaths = []string{"svc/**"} }, true, []string{TriggerCriticalPath}},
		{"80 lines", func(in *GateInput) { in.NonTestChangedLines = 80 }, true, []string{TriggerChangedLines}},
		{"79 lines", func(in *GateInput) { in.NonTestChangedLines = 79 }, false, nil},
		{"repo threshold", func(in *GateInput) { in.MinChangedLines = 10 }, true, []string{TriggerChangedLines}},
		{"merge safety 0.59", func(in *GateInput) { in.MergeSafety = ptr(0.59) }, true, []string{TriggerMergeSafety}},
		{"merge safety 0.6", func(in *GateInput) { in.MergeSafety = ptr(0.6) }, false, nil},
		{"recent incident", func(in *GateInput) { in.RecentIncident = ptr(true) }, true, []string{TriggerRecentIncident}},
		{"forced", func(in *GateInput) { in.AutoApproveCandidate = true }, true, []string{TriggerForced}},
	}
	for _, tt := range tests {
		in := quiet
		tt.edit(&in)
		g := Decide(in)
		if g.Run != tt.run || !slices.Equal(g.Triggers, tt.triggers) || g.Forced != in.AutoApproveCandidate {
			t.Errorf("%s: %+v", tt.name, g)
		}
	}
	// Unmeasured facts are recorded, and do not run the lane on their own.
	g := Decide(GateInput{Files: []string{"a.go"}})
	if g.Run || !slices.Equal(g.Unmeasured, []string{TriggerMergeSafety, TriggerRecentIncident}) {
		t.Errorf("unmeasured = %+v", g)
	}
}

func TestClasses(t *testing.T) {
	if len(Classes) != 6 {
		t.Fatalf("%d classes", len(Classes))
	}
	got := Enabled(map[string]bool{Contract: true, NilDeref: true, "bug.made-up": true, OffByOne: false})
	if len(got) != 2 || got[0].ID != NilDeref || got[1].ID != Contract || !got[1].NeedsReferences {
		t.Errorf("enabled = %+v", got)
	}
	if _, ok := ByID("bug.made-up"); ok {
		t.Error("unknown class found")
	}
	for _, c := range Classes {
		if c.Version == "" || c.Guidance == "" || c.LooksFor == "" {
			t.Errorf("class %s is incomplete", c.ID)
		}
	}
}
