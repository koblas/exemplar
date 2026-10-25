package calibrate

import (
	"os"
	"testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/schema"
)

func exampleSnapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	snap, err := config.LoadSnapshot(os.DirFS("../../examples/exemplar-config"), "cfg-test")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// A verifier result is valid for the verifier's own inputs: providers,
// settings, thresholds and seed labels change the key; rules and repo
// files do not.
func TestVerifierKey(t *testing.T) {
	t.Parallel()
	base := VerifierKey(exampleSnapshot(t))
	if len(base) != 64 || VerifierKey(exampleSnapshot(t)) != base {
		t.Fatalf("key not stable: %q", base)
	}
	for name, change := range map[string]func(*config.Snapshot){
		"provider":  func(s *config.Snapshot) { s.Global.Providers.Verifier[0].Model = "other-model" },
		"settings":  func(s *config.Snapshot) { s.Global.Verifier.ImportanceMin += 0.1 },
		"threshold": func(s *config.Snapshot) { s.Global.EvalThresholds.Verifier.KnownWrongRefutedMin -= 0.05 },
		"seed label": func(s *config.Snapshot) {
			s.VerifierLabels = append(s.VerifierLabels, config.VerifierLabel{ID: "zz-new", Label: schema.LabelWrong})
		},
	} {
		s := exampleSnapshot(t)
		change(s)
		if VerifierKey(s) == base {
			t.Errorf("%s change kept the key", name)
		}
	}
	for name, change := range map[string]func(*config.Snapshot){
		"rule":      func(s *config.Snapshot) { s.Rules = s.Rules[1:] },
		"repo file": func(s *config.Snapshot) { s.Repos["acme/new"] = schema.RepoConfig{Enabled: true} },
		"quorum":    func(s *config.Snapshot) { s.Global.Verifier.Quorum = &schema.Quorum{N: 5, K: 4} },
		"label order": func(s *config.Snapshot) {
			s.VerifierLabels[0], s.VerifierLabels[1] = s.VerifierLabels[1], s.VerifierLabels[0]
		},
	} {
		s := exampleSnapshot(t)
		if len(s.VerifierLabels) < 2 {
			t.Fatal("example config needs two verifier labels")
		}
		change(s)
		if VerifierKey(s) != base {
			t.Errorf("%s change moved the key", name)
		}
	}
	if got := VerifierProviders(exampleSnapshot(t)); len(got) == 0 || got[0] != "claude:claude-opus-5-5" {
		t.Errorf("providers = %v", got)
	}
}
