package gates

import (
	"fmt"
	"maps"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Merge-safety terms (spec §6.13), the keys of global.yaml
// merge_safety.weights.
const (
	TermIncident  = "incident_180d"  // incident linked to the path in the last 180 days
	TermRevert    = "revert_180d"    // revert touching the path in the last 180 days
	TermChurn     = "churn_30d"      // path changed in > 10 merged PRs in the last 30 days
	TermNoTests   = "no_tests"       // non-test code changed with no test file changed
	TermSensitive = "sensitive_path" // migration, infra or auth path (repo sensitive_paths)
	TermNewAuthor = "new_author"     // author's first 30 days in the repo
	TermForcePush = "force_push"     // force-push on this PR
)

// Windows and thresholds the terms are measured over.
const (
	IncidentWindowDays = 180
	RevertWindowDays   = 180
	ChurnWindowDays    = 30
	ChurnMinPRs        = 10 // churn counts when a path changed in more than this many PRs
	NewAuthorDays      = 30
	RecentEventDays    = 90 // bug-lane trigger window
)

// DefaultWeights are the spec §6.13 penalties. Configured weights replace
// them term by term; a term left out of global.yaml keeps its default, so
// deleting a line never silently removes a penalty (set it to 0 instead).
var DefaultWeights = map[string]float64{
	TermIncident: 0.30, TermRevert: 0.20, TermChurn: 0.10, TermNoTests: 0.15,
	TermSensitive: 0.25, TermNewAuthor: 0.10, TermForcePush: 0.05,
}

// Weights returns the effective weights: DefaultWeights overlaid with
// configured. Unknown terms and weights outside [0, 1] are errors, so a
// typo cannot drop a penalty.
func Weights(configured map[string]float64) (map[string]float64, error) {
	w := make(map[string]float64, len(DefaultWeights))
	maps.Copy(w, DefaultWeights)
	for k, v := range configured {
		if _, ok := DefaultWeights[k]; !ok {
			return nil, fmt.Errorf("gates: unknown merge_safety weight %q", k)
		}
		if v < 0 || v > 1 || math.IsNaN(v) {
			return nil, fmt.Errorf("gates: merge_safety weight %s = %v outside [0, 1]", k, v)
		}
		w[k] = v
	}
	return w, nil
}

// PathFacts are the code-measured facts about one touched path.
type PathFacts struct {
	Path      string `json:"path"`
	Incident  bool   `json:"incident_180d"`
	Revert    bool   `json:"revert_180d"`
	MergedPRs int    `json:"merged_prs_30d"`
	Sensitive bool   `json:"sensitive"`
	// Recent is an incident or revert in the last RecentEventDays. It is
	// not a merge-safety term; it triggers the bug lane (spec §6.7).
	Recent bool `json:"incident_or_revert_90d"`
}

// MergeSafetyInput holds the facts the score is computed from. Like
// Stage1Input, nothing in it may come from model output.
type MergeSafetyInput struct {
	Paths     []PathFacts `json:"paths"`
	NoTests   bool        `json:"no_tests"`
	NewAuthor bool        `json:"new_author"`
	ForcePush bool        `json:"force_push"`
}

// Term is one applied penalty. Path is set for per-path terms.
type Term struct {
	Name    string  `json:"name"`
	Penalty float64 `json:"penalty"`
	Path    string  `json:"path,omitempty"`
}

// MergeSafetyResult is the score and every term behind it, logged in
// approval_decisions.stage1.
type MergeSafetyResult struct {
	Score float64 `json:"score"`
	// WorstPath is the path whose per-path terms subtracted the most.
	WorstPath string             `json:"worst_path,omitempty"`
	Terms     []Term             `json:"terms"`
	Weights   map[string]float64 `json:"weights"`
	Input     MergeSafetyInput   `json:"input"`
}

// MergeSafety computes the deterministic score (spec §6.13): 1.0 minus
// the worst path's per-path penalties minus the PR-level penalties,
// floored at 0. weights must come from Weights.
func MergeSafety(in MergeSafetyInput, weights map[string]float64) MergeSafetyResult {
	res := MergeSafetyResult{Terms: []Term{}, Weights: weights, Input: in}
	var worst []Term
	worstSum := 0.0
	for _, p := range in.Paths {
		var terms []Term
		add := func(on bool, name string) {
			if on && weights[name] > 0 {
				terms = append(terms, Term{Name: name, Penalty: weights[name], Path: p.Path})
			}
		}
		add(p.Incident, TermIncident)
		add(p.Revert, TermRevert)
		add(p.MergedPRs > ChurnMinPRs, TermChurn)
		add(p.Sensitive, TermSensitive)
		sum := 0.0
		for _, t := range terms {
			sum += t.Penalty
		}
		// Round so float noise (0.2+0.1 > 0.3) cannot break a tie; ties
		// keep the first path in diff order, so the result is stable.
		sum = round6(sum)
		if sum > worstSum {
			worst, worstSum, res.WorstPath = terms, sum, p.Path
		}
	}
	res.Terms = append(res.Terms, worst...)
	for _, t := range []struct {
		on   bool
		name string
	}{{in.NoTests, TermNoTests}, {in.NewAuthor, TermNewAuthor}, {in.ForcePush, TermForcePush}} {
		if t.on && weights[t.name] > 0 {
			res.Terms = append(res.Terms, Term{Name: t.name, Penalty: weights[t.name]})
		}
	}
	score := 1.0
	for _, t := range res.Terms {
		score -= t.Penalty
	}
	// Round away float noise (1 - 0.3 - 0.2 ...) so thresholds compare exactly.
	res.Score = math.Max(0, round6(score))
	return res
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// Sensitive reports whether p matches one of the repo's sensitive_paths.
func Sensitive(p string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(g string) bool { return doublestar.MatchUnvalidated(g, p) })
}

// NoTests reports whether paths change non-test code without changing any
// test file (the no_tests term). Docs, config and data files are neither.
func NoTests(paths []string) bool {
	code := false
	for _, p := range paths {
		switch {
		case IsTestFile(p):
			return false
		case isCode(p):
			code = true
		}
	}
	return code
}

// IsTestFile recognizes test files by the common conventions of the
// languages Exemplar reviews.
func IsTestFile(p string) bool {
	base := path.Base(p)
	for _, dir := range []string{"test/", "tests/", "__tests__/", "testdata/", "spec/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	return strings.HasSuffix(stem, "_test") || strings.HasPrefix(stem, "test_") ||
		strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") ||
		strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests") || strings.HasSuffix(stem, "_spec")
}

var codeExts = []string{
	".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".java", ".kt", ".kts", ".scala",
	".rb", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".php", ".swift", ".m", ".sql", ".sh",
}

func isCode(p string) bool { return slices.Contains(codeExts, strings.ToLower(path.Ext(p))) }
