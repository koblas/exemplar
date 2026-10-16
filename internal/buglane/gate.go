package buglane

import (
	"github.com/bmatcuk/doublestar/v4"
)

// Risk-gate defaults.
const (
	DefaultMinChangedLines = 80
	// RiskyMergeSafety: a merge_safety_score below this triggers the lane.
	RiskyMergeSafety = 0.6
)

// Triggers recorded in the gate trace.
const (
	TriggerForced         = "auto_approve_candidate"
	TriggerCriticalPath   = "critical_path"
	TriggerChangedLines   = "changed_lines"
	TriggerMergeSafety    = "merge_safety_below_0.6"
	TriggerRecentIncident = "incident_or_revert_90d"
)

// GateInput holds the facts the risk gate decides on. All come from the
// diff, config or the merge-safety collector; none from a model.
type GateInput struct {
	// Files are the reviewable changed files.
	Files []string
	// NonTestChangedLines counts added plus deleted lines in reviewable
	// non-test files.
	NonTestChangedLines int
	CriticalPaths       []string
	MinChangedLines     int // 0: the default
	// MergeSafety is the score; nil when it was not measured.
	MergeSafety *float64
	// RecentIncident is whether a touched path had an incident or revert in
	// the last 90 days; nil when it was not measured.
	RecentIncident *bool
	// AutoApproveCandidate forces the lane: a PR that may be
	// approved without a human must have been checked for bugs.
	AutoApproveCandidate bool
}

// Gate is the risk gate's decision and why.
type Gate struct {
	Run      bool     `json:"run"`
	Forced   bool     `json:"forced"`
	Triggers []string `json:"triggers,omitempty"`
	// Unmeasured lists triggers that could not be evaluated. An unmeasured
	// trigger does not run the lane by itself.
	Unmeasured []string `json:"unmeasured,omitempty"`
}

// Decide applies the risk gate: the lane runs if any trigger holds, and
// always for an auto-approve candidate.
func Decide(in GateInput) Gate {
	var g Gate
	if in.AutoApproveCandidate {
		g.Forced = true
		g.Triggers = append(g.Triggers, TriggerForced)
	}
	for _, f := range in.Files {
		if matchAny(in.CriticalPaths, f) {
			g.Triggers = append(g.Triggers, TriggerCriticalPath)
			break
		}
	}
	minLines := in.MinChangedLines
	if minLines <= 0 {
		minLines = DefaultMinChangedLines
	}
	if in.NonTestChangedLines >= minLines {
		g.Triggers = append(g.Triggers, TriggerChangedLines)
	}
	switch {
	case in.MergeSafety == nil:
		g.Unmeasured = append(g.Unmeasured, TriggerMergeSafety)
	case *in.MergeSafety < RiskyMergeSafety:
		g.Triggers = append(g.Triggers, TriggerMergeSafety)
	}
	switch {
	case in.RecentIncident == nil:
		g.Unmeasured = append(g.Unmeasured, TriggerRecentIncident)
	case *in.RecentIncident:
		g.Triggers = append(g.Triggers, TriggerRecentIncident)
	}
	g.Run = len(g.Triggers) > 0
	return g
}

// MatchAny reports whether p matches any of the globs.
func MatchAny(patterns []string, p string) bool { return matchAny(patterns, p) }

func matchAny(patterns []string, p string) bool {
	for _, g := range patterns {
		if doublestar.MatchUnvalidated(g, p) {
			return true
		}
	}
	return false
}
