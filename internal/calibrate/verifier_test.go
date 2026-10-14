package calibrate

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/koblas/exemplar/internal/compiled"
	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/engine"
	"github.com/koblas/exemplar/internal/redact"
	"github.com/koblas/exemplar/internal/schema"
)

func setup(t *testing.T, verdictFor func(task string) string) (*Runner, *engine.ScriptedTransport, []Label) {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "cal")
	if err != nil {
		t.Fatal(err)
	}
	labels := SeedLabels(snap)
	if len(labels) < 4 {
		t.Fatalf("seed set has %d labels", len(labels))
	}
	sc, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	tr := &engine.ScriptedTransport{Script: engine.Script{
		VerdictFor: verdictFor,
		// Confirmed verdicts quote the cited line, which the stage resolves.
		Quote: func(string, int) string { return "" },
	}}
	h := engine.NewHarness(engine.HarnessConfig{GatewayURL: "http://scripted.invalid", GatewayToken: "t", HTTPClient: &http.Client{Transport: tr}})
	return &Runner{Engine: engine.New(h, sc, compiled.Runner{ASTGrep: "ast-grep-not-installed"}), Scanner: sc, Snapshot: snap, WorkDir: t.TempDir()}, tr, labels
}

var thresholds = &schema.VerifierThreshold{KnownWrongRefutedMin: 0.9, KnownTrueRefutedMax: 0.1}

// A good verifier refutes exactly the wrong claims (here, by their text).
func goodVerifier(task string) string {
	if strings.Contains(task, "c.Do(req) does not pass ctx") || strings.Contains(task, "main calls http.Get") {
		return engine.VerdictRefuted
	}
	return engine.VerdictConfirmed
}

func TestVerifierCalibrationPasses(t *testing.T) {
	r, tr, labels := setup(t, goodVerifier)
	rep, err := r.Verifier(t.Context(), labels, thresholds)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass || rep.KnownWrong.N != 2 || rep.KnownWrong.Rate != 1 || rep.KnownTrue.N != 2 || rep.KnownTrue.Rate != 0 || rep.Errors != 0 {
		t.Fatalf("report = %+v", rep)
	}
	// Only the verifier ran: calibration has no generator.
	for _, c := range tr.Calls {
		if c.Stage != "verifier" {
			t.Errorf("a %s call ran during calibration", c.Stage)
		}
	}
	if len(tr.Calls) == 0 {
		t.Fatal("the verifier never ran")
	}
}

func TestVerifierCalibrationFails(t *testing.T) {
	// Refutes everything: perfect on known-wrong, unacceptable on known-true.
	r, _, labels := setup(t, func(string) string { return engine.VerdictRefuted })
	rep, _ := r.Verifier(t.Context(), labels, thresholds)
	if rep.Pass || !strings.Contains(strings.Join(rep.Reasons, ";"), "known-true") {
		t.Errorf("refutes all = %+v", rep)
	}
	// Confirms everything: misses the known-wrong ones.
	r, _, labels = setup(t, func(string) string { return engine.VerdictConfirmed })
	rep, _ = r.Verifier(t.Context(), labels, thresholds)
	if rep.Pass || !strings.Contains(strings.Join(rep.Reasons, ";"), "known-wrong") {
		t.Errorf("confirms all = %+v", rep)
	}
	// Uncertain is not refuted: a verifier that hedges on wrong findings fails.
	r, _, labels = setup(t, func(task string) string {
		if goodVerifier(task) == engine.VerdictRefuted {
			return engine.VerdictUncertain
		}
		return engine.VerdictConfirmed
	})
	rep, _ = r.Verifier(t.Context(), labels, thresholds)
	if rep.Pass || rep.KnownWrong.Refuted != 0 {
		t.Errorf("hedging = %+v", rep)
	}
}

func TestVerifierCalibrationErrors(t *testing.T) {
	r, _, labels := setup(t, goodVerifier)
	labels[0].Candidate.RuleID = "no-such-rule"
	rep, err := r.Verifier(t.Context(), labels, thresholds)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pass || rep.Errors != 1 || rep.Outcomes[0].Error == "" {
		t.Errorf("a label that cannot be replayed must fail the run: %+v", rep)
	}
	if _, err := r.Verifier(t.Context(), labels, nil); err == nil {
		t.Error("missing thresholds accepted")
	}
	if rep, _ := r.Verifier(t.Context(), nil, thresholds); rep.Pass {
		t.Error("an empty label set passed")
	}
}
