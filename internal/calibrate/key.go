package calibrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/koblas/exemplar/internal/config"
)

// Kinds of calibration_results rows.
const (
	KindVerifier    = "verifier"
	KindCategorizer = "categorizer"
)

// VerifierKey names what a verifier calibration result is valid for:
// the verifier providers and settings, its thresholds, and the
// evals/_verifier seed set. A result recorded for one key stays valid
// until one of them changes; editing a rule or a repo file does not
// touch it. Labels recorded from feedback (verifier_labels) are replayed
// but are not part of the key: they grow every day.
func VerifierKey(snap *config.Snapshot) string {
	g := snap.Global
	seed := slices.Clone(snap.VerifierLabels)
	slices.SortFunc(seed, func(a, b config.VerifierLabel) int { return strings.Compare(a.ID, b.ID) })
	labels := make([]any, 0, len(seed))
	for _, l := range seed {
		labels = append(labels, l.VerifierLabel)
	}
	return hashOf(map[string]any{
		"providers": g.Providers.Verifier, "verifier": g.Verifier,
		"thresholds": g.EvalThresholds.Verifier, "labels": labels,
	})
}

// CategorizerKey names what a categorizer calibration result is valid
// for: the categorizer pair, its thresholds and the evals/_categorizer
// seed set.
func CategorizerKey(snap *config.Snapshot) string {
	g := snap.Global
	seed := slices.Clone(snap.CategorizerLabels)
	slices.SortFunc(seed, func(a, b config.CategorizerLabel) int { return strings.Compare(a.ID, b.ID) })
	labels := make([]any, 0, len(seed))
	for _, l := range seed {
		labels = append(labels, l.CategorizerLabel)
	}
	return hashOf(map[string]any{"providers": g.Providers.Categorizer, "thresholds": g.EvalThresholds.Categorizer, "labels": labels})
}

// CategorizerProviders lists the categorizer pair as provider:model.
func CategorizerProviders(snap *config.Snapshot) []string {
	c := snap.Global.Providers.Categorizer
	if c == nil {
		return []string{}
	}
	return []string{c.A.Provider + ":" + c.A.Model, c.B.Provider + ":" + c.B.Model}
}

// VerifierProviders lists the configured verifiers as provider:model.
func VerifierProviders(snap *config.Snapshot) []string {
	out := make([]string, 0, len(snap.Global.Providers.Verifier))
	for _, p := range snap.Global.Providers.Verifier {
		out = append(out, p.Provider+":"+p.Model)
	}
	return out
}

func hashOf(v any) string {
	// Maps encode with sorted keys; every value here decodes from JSON
	// and re-encodes without error.
	data, _ := json.Marshal(v) //nolint:errchkjson // see above
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
