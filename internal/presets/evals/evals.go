// Package evals embeds the defence evals that ship with the binary: the
// adversarial set, the verifier labels and the categorizer labels. They
// name rules from the shipped exemplar:recommended preset and run against
// that preset as embedded, whatever the organization's config extends,
// disables or overrides. internal/config loads them next to the
// organization's own evals/_adversarial, evals/_verifier and
// evals/_categorizer.
package evals

import "embed"

// FS holds one directory per set.
//
//go:embed adversarial/*.yaml verifier/*.yaml categorizer/*.yaml
var FS embed.FS

// Directories in FS.
const (
	AdversarialDir = "adversarial"
	VerifierDir    = "verifier"
	CategorizerDir = "categorizer"
)

// Preset is the shipped preset whose rules the shipped evals name.
const Preset = "exemplar:recommended"

// PathPrefix marks a shipped eval's path in reports and problems, since it
// has no path in the config repo.
const PathPrefix = "exemplar:evals/"
