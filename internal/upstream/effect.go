package upstream

import "fmt"

// Window is the effectiveness window, in days.
const Window = 90

// MinPRs is the fewest in-scope PRs on each side of a comparison for it
// to count.
const MinPRs = 20

// Drop is the largest violation rate, relative to the comparison, that
// counts as the export working: at least a 20% drop.
const Drop = 0.8

// Comparison is one side-by-side: PRs (and their violations) that had
// the export, and PRs that did not.
type Comparison struct {
	Name                     string
	WithPRs, WithViolations  int
	WithoutPRs, WithoutViols int
}

// Rates returns violations per in-scope PR with and without the export.
func (c Comparison) Rates() (with, without float64) {
	return float64(c.WithViolations) / float64(max(c.WithPRs, 1)), float64(c.WithoutViols) / float64(max(c.WithoutPRs, 1))
}

// measurable reports whether both sides are big enough and there was
// something to drop.
func (c Comparison) measurable() bool {
	return c.WithPRs >= MinPRs && c.WithoutPRs >= MinPRs && c.WithoutViols > 0
}

// Verdict is whether a rule's export.summary should be rewritten: every
// measurable comparison shows no drop of at least 20%. Without a
// measurable comparison there is no verdict.
type Verdict struct {
	Rewrite bool
	// Evidence describes each measurable comparison.
	Evidence []string
}

// Judge applies Drop to the comparisons.
func Judge(cs ...Comparison) Verdict {
	var v Verdict
	measured, dropped := 0, false
	for _, c := range cs {
		if !c.measurable() {
			continue
		}
		measured++
		with, without := c.Rates()
		if with <= Drop*without {
			dropped = true
		}
		v.Evidence = append(v.Evidence, fmt.Sprintf("%s: %.2f violations per PR (%d PRs) against %.2f (%d PRs)", c.Name, with, c.WithPRs, without, c.WithoutPRs))
	}
	v.Rewrite = measured > 0 && !dropped
	return v
}
