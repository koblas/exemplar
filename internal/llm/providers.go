package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"sort"

	"github.com/koblas/exemplar/internal/schema"
)

// Spec describes one provider implementation. Providers are pluggable in
// code: adding one means writing a Spec and registering it below. Config
// can only choose among registered providers, so this registry is still
// the hardcoded provider allowlist; register a provider only once its
// data-processing terms are approved.
type Spec struct {
	// Family returns the model family serving model on this provider. Two
	// routes with the same family are not independent for verification or
	// the auto-approve risk categorizer (same_family).
	Family func(model string) string
	// ValidateModel rejects model ids this provider cannot serve.
	ValidateModel func(model string) error
	// New builds a client from deployment settings and credentials.
	New func(ctx context.Context, s Settings) (Provider, error)
}

// Settings carries deployment configuration to provider constructors.
// Credentials come from the environment (mounted secrets, workload
// identity), never from exemplar-config.
type Settings struct {
	Getenv     func(string) string
	HTTPClient *http.Client
}

// EnvSettings reads settings from the process environment.
func EnvSettings() Settings { return Settings{Getenv: os.Getenv} }

func (s Settings) env(k string) string {
	if s.Getenv == nil {
		return ""
	}
	return s.Getenv(k)
}

// registry holds the approved provider implementations.
var registry = map[string]Spec{
	ProviderClaude:  claudeSpec,
	ProviderBedrock: bedrockSpec,
	ProviderOpenAI:  openAISpec,
}

// Registered returns the approved provider names.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Family returns the model family for a model on an approved provider.
func Family(provider, model string) (string, bool) {
	spec, ok := registry[provider]
	if !ok {
		return "", false
	}
	return spec.Family(model), true
}

// ConfiguredProviders returns every provider a stage configuration names.
func ConfiguredProviders(p schema.Providers) []string {
	entries := slices.Concat(p.Generator, p.Verifier, p.Light)
	if p.Categorizer != nil {
		entries = append(entries, p.Categorizer.A, p.Categorizer.B)
	}
	var names []string
	for _, e := range entries {
		if !slices.Contains(names, e.Provider) {
			names = append(names, e.Provider)
		}
	}
	sort.Strings(names)
	return names
}

// BuildProviders constructs every provider p routes to. A provider whose
// credentials are missing is skipped with a warning, so an example config
// that lists a fallback the installation hasn't set up still starts; it
// fails only when a stage would be left with no provider at all, naming
// every provider that didn't build.
func BuildProviders(ctx context.Context, p schema.Providers, s Settings, log *slog.Logger) (map[string]Provider, error) {
	if log == nil {
		log = slog.Default()
	}
	clients := map[string]Provider{}
	failed := map[string]error{}
	for _, name := range ConfiguredProviders(p) {
		c, err := NewProvider(ctx, name, s)
		if err != nil {
			failed[name] = err
			continue
		}
		clients[name] = c
	}
	if len(failed) == 0 {
		return clients, nil
	}
	stages := map[string][]schema.ProviderModel{"generator": p.Generator, "verifier": p.Verifier, "light": p.Light}
	if p.Categorizer != nil {
		stages["categorizer.a"] = []schema.ProviderModel{p.Categorizer.A}
		stages["categorizer.b"] = []schema.ProviderModel{p.Categorizer.B}
	}
	var errs []error
	for _, stage := range slices.Sorted(maps.Keys(stages)) {
		entries := stages[stage]
		if len(entries) == 0 {
			continue
		}
		if !slices.ContainsFunc(entries, func(e schema.ProviderModel) bool { _, ok := clients[e.Provider]; return ok }) {
			errs = append(errs, fmt.Errorf("stage %s %w", stage, errNoWorkingProvider))
		}
	}
	if len(errs) > 0 {
		for _, name := range slices.Sorted(maps.Keys(failed)) {
			errs = append(errs, fmt.Errorf("provider %s: %w", name, failed[name]))
		}
		return nil, errors.Join(errs...)
	}
	for _, name := range slices.Sorted(maps.Keys(failed)) {
		log.Warn("provider skipped: calls routed to it fail over to the next one", "provider", name, "err", failed[name])
	}
	return clients, nil
}

// Request is one Messages API call. Body is Anthropic Messages JSON with
// the routed model already set.
type Request struct {
	Body []byte
	// Beta is the anthropic-beta header to forward, if any.
	Beta string
}

// Response is a provider's answer, normalized to Anthropic Messages JSON.
type Response struct {
	Status int
	Body   []byte
	Usage  Usage
}

// ProviderError is a failed call. Retryable errors move the call to the
// next provider in the stage's list.
type ProviderError struct {
	Status    int
	Retryable bool
	Body      []byte
	Err       error
}

func (e *ProviderError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("provider: %v", e.Err)
	}
	return fmt.Sprintf("provider: status %d", e.Status)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Provider sends Messages requests. Providers that speak the Messages API
// pass requests through; one that does not implements this by translating
// the subset of the Messages API Exemplar uses (text and tool blocks,
// tools, the output schema) to its own API and back.
type Provider interface {
	Name() string
	Send(ctx context.Context, req Request) (*Response, error)
}

var (
	errNotApproved       = errors.New("not approved")
	errRequired          = errors.New("is required")
	errModelName         = errors.New("unsupported model name")
	errNoWorkingProvider = errors.New("has no working provider")
)

// NewProvider constructs an approved provider.
func NewProvider(ctx context.Context, name string, s Settings) (Provider, error) {
	spec, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("llm: provider %q is %w (approved: %v)", name, errNotApproved, Registered())
	}
	p, err := spec.New(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("llm: configure provider %s: %w", name, err)
	}
	return p, nil
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// IsRetryable reports whether err should fail over to the next provider.
func IsRetryable(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.Retryable
}
