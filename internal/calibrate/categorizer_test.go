package calibrate

import (
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/schema"
)

var catThresholds = &schema.CategorizerThreshold{KnownRiskyApprovedMax: 0.05, KnownSafeApprovedMin: 0.5}

// categorizerSetup scripts both sides with answer, on independent families.
func categorizerSetup(t *testing.T, answer func(stage, prContext string) string) (*Runner, *engine.ScriptedTransport, []PRLabel) {
	t.Helper()
	r, tr, _ := setup(t, goodVerifier)
	tr.Script.Categorizer = answer
	tr.Script.CategorizerFamily = map[string]string{"categorizer-a": "anthropic", "categorizer-b": "openai"}
	labels := SeedPRLabels(r.Snapshot)
	if len(labels) < 4 {
		t.Fatalf("seed set has %d PR labels", len(labels))
	}
	return r, tr, labels
}

const (
	lowAnswer  = `{"risk": "low", "p_low": 0.95, "reasons": ["no behavior change"]}`
	highAnswer = `{"risk": "high", "p_low": 0.05, "reasons": ["changes behavior"]}`
)

// A good categorizer says low only for the doc-only changes (by their text).
func goodCategorizer(_, pr string) string {
	if strings.Contains(pr, "README") || strings.Contains(pr, "Document Total") {
		return lowAnswer
	}
	return highAnswer
}

func TestCategorizerCalibrationPasses(t *testing.T) {
	r, tr, labels := categorizerSetup(t, goodCategorizer)
	rep, err := r.Categorizer(t.Context(), labels, catThresholds)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass || rep.KnownRisky.N != 2 || rep.KnownRisky.Rate != 0 || rep.KnownSafe.N != 2 || rep.KnownSafe.Rate != 1 || rep.Bar != 0.8 {
		t.Fatalf("report = %+v", rep)
	}
	for _, c := range tr.Calls {
		if c.Stage != "categorizer-a" && c.Stage != "categorizer-b" {
			t.Errorf("a %s call ran during categorizer calibration", c.Stage)
		}
	}
	if len(tr.Calls) != 2*len(labels) {
		t.Errorf("%d calls for %d labels", len(tr.Calls), len(labels))
	}
}

func TestCategorizerCalibrationFails(t *testing.T) {
	// Approves everything: fails on the risky PRs.
	r, _, labels := categorizerSetup(t, func(string, string) string { return lowAnswer })
	rep, _ := r.Categorizer(t.Context(), labels, catThresholds)
	if rep.Pass || !strings.Contains(strings.Join(rep.Reasons, ";"), "known-risky") {
		t.Errorf("approves all = %+v", rep)
	}
	// Approves nothing: fails on the safe PRs.
	r, _, labels = categorizerSetup(t, func(string, string) string { return highAnswer })
	rep, _ = r.Categorizer(t.Context(), labels, catThresholds)
	if rep.Pass || !strings.Contains(strings.Join(rep.Reasons, ";"), "known-safe") {
		t.Errorf("approves none = %+v", rep)
	}
	// A raised bar turns the same answers into no approvals.
	r, _, labels = categorizerSetup(t, goodCategorizer)
	strict := *catThresholds
	strict.MinReviewConfidence = 0.99
	if rep, _ = r.Categorizer(t.Context(), labels, &strict); rep.Pass || rep.KnownSafe.Approved != 0 {
		t.Errorf("bar 0.99 = %+v", rep)
	}
	// Same-family sides never approve, so the safe half fails.
	r, tr, labels := categorizerSetup(t, goodCategorizer)
	tr.Script.CategorizerFamily = map[string]string{"categorizer-a": "openai", "categorizer-b": "openai"}
	if rep, _ = r.Categorizer(t.Context(), labels, catThresholds); rep.Pass || rep.KnownSafe.Approved != 0 {
		t.Errorf("same family = %+v", rep)
	}
}

func TestCategorizerCalibrationErrors(t *testing.T) {
	// A side that breaks the schema is an unreplayable label, not a "not
	// approved" that would pass the risky half.
	r, _, labels := categorizerSetup(t, func(stage, pr string) string {
		if stage == "categorizer-b" && strings.Contains(pr, "X-Debug") {
			return `{"risk": "none"}`
		}
		return goodCategorizer(stage, pr)
	})
	rep, err := r.Categorizer(t.Context(), labels, catThresholds)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pass || rep.Errors != 1 || !strings.Contains(strings.Join(rep.Reasons, ";"), "could not be replayed") {
		t.Errorf("schema violation = %+v", rep)
	}
	if _, err := r.Categorizer(t.Context(), labels, nil); err == nil {
		t.Error("missing thresholds accepted")
	}
	if rep, _ := r.Categorizer(t.Context(), nil, catThresholds); rep.Pass {
		t.Error("an empty label set passed")
	}
}
