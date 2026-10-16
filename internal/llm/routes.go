package llm

import (
	"errors"
	"fmt"

	"github.com/koblas/exemplar/internal/schema"
)

// Gateway stages. The categorizer has two independent sides with no
// failover between them: the auto-approve risk categorizer needs two
// different providers.
const (
	StageGenerator    = "generator"
	StageVerifier     = "verifier"
	StageLight        = "light"
	StageCategorizerA = "categorizer-a"
	StageCategorizerB = "categorizer-b"
)

// Model tiers for the generator stage.
const (
	TierStandard = "standard"
	TierHigh     = "high"
)

var (
	errNoCategorizer = errors.New("llm: no categorizer configured")
	errUnknownStage  = errors.New("unknown stage")
	errUnknownTier   = errors.New("unknown tier")
	errNoModel       = errors.New("has no model")
	errNoProviders   = errors.New("has no providers")
)

// Route is one provider and model in a stage's failover order.
type Route struct {
	Provider string
	Model    string
}

// Routes returns the ordered failover list for stage, rejecting any
// provider that is not on the hardcoded allowlist.
func Routes(p schema.Providers, stage, tier string) ([]Route, error) {
	var entries []schema.ProviderModel
	switch stage {
	case StageGenerator:
		entries = p.Generator
	case StageVerifier:
		entries = p.Verifier
	case StageLight:
		entries = p.Light
	case StageCategorizerA, StageCategorizerB:
		if p.Categorizer == nil {
			return nil, errNoCategorizer
		}
		if stage == StageCategorizerA {
			entries = []schema.ProviderModel{p.Categorizer.A}
		} else {
			entries = []schema.ProviderModel{p.Categorizer.B}
		}
	default:
		return nil, fmt.Errorf("llm: %w %q", errUnknownStage, stage)
	}
	if tier == "" {
		tier = TierStandard
	}
	if tier != TierStandard && tier != TierHigh {
		return nil, fmt.Errorf("llm: %w %q", errUnknownTier, tier)
	}
	routes := make([]Route, 0, len(entries))
	for _, e := range entries {
		spec, ok := registry[e.Provider]
		if !ok {
			return nil, fmt.Errorf("llm: provider %q is %w (approved: %v)", e.Provider, errNotApproved, Registered())
		}
		model := e.Model
		if stage == StageGenerator && e.Standard != "" {
			model = e.Standard
			if tier == TierHigh {
				model = e.High
			}
		}
		if model == "" {
			return nil, fmt.Errorf("llm: provider %q %w for stage %s tier %s", e.Provider, errNoModel, stage, tier)
		}
		if err := spec.ValidateModel(model); err != nil {
			return nil, fmt.Errorf("llm: provider %s cannot serve %q: %w", e.Provider, model, err)
		}
		routes = append(routes, Route{Provider: e.Provider, Model: model})
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("llm: stage %s %w", stage, errNoProviders)
	}
	return routes, nil
}

// ValidateProviders checks every stage in p resolves to approved providers.
// The gateway calls it at startup and refuses to run on failure.
func ValidateProviders(p schema.Providers) error {
	for _, stage := range []string{StageGenerator, StageVerifier, StageLight} {
		for _, tier := range []string{TierStandard, TierHigh} {
			if _, err := Routes(p, stage, tier); err != nil {
				return err
			}
		}
	}
	if p.Categorizer != nil {
		for _, stage := range []string{StageCategorizerA, StageCategorizerB} {
			if _, err := Routes(p, stage, ""); err != nil {
				return err
			}
		}
	}
	return nil
}
