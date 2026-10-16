package scope

import (
	"cmp"
	"slices"
)

// Candidate is an in-scope exemplar rule competing for a slot under the
// fan-out cap.
type Candidate struct {
	RuleID string
	// Precision is the rule's recent precision. Higher runs first; NaN sorts
	// below every number.
	Precision float64
}

// ApplyFanOutCap keeps at most limit candidates and defers the rest,
// lowest precision first. Ties are broken by rule id so the split is
// deterministic. Both results are in run order; the input is not modified.
func ApplyFanOutCap(candidates []Candidate, limit int) ([]Candidate, []Candidate) {
	sorted := slices.Clone(candidates)
	slices.SortStableFunc(sorted, func(a, b Candidate) int {
		if c := cmp.Compare(b.Precision, a.Precision); c != 0 {
			return c
		}
		return cmp.Compare(a.RuleID, b.RuleID)
	})
	limit = max(0, min(limit, len(sorted)))
	return sorted[:limit:limit], sorted[limit:]
}
