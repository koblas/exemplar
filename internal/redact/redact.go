// Package redact finds and removes secrets before any text reaches a model
// provider (PRD FR-H3, spec §6.3 step 4). The worker redacts every model
// input; the gateway scans every request again and refuses any that still
// contains a match.
//
// Matches never carry the secret itself, so they are safe to log and store.
package redact

import (
	"errors"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/logging"
)

// Placeholder replaces each secret in model inputs.
const Placeholder = "‹redacted:secret›"

// Match is one detected secret, without its value.
type Match struct {
	RuleID string
	// Line is the 1-based line of the match within the scanned text.
	Line int
}

// ErrResidualSecret means redaction left a detectable secret behind. The
// caller must treat the input as unredactable: the review is incomplete
// and nothing is sent.
var ErrResidualSecret = errors.New("redact: secret still detected after redaction")

// Scanner wraps the gitleaks detector with its default rules.
type Scanner struct {
	mu  sync.Mutex
	det *detect.Detector
}

var silenceOnce sync.Once

// New builds a Scanner. gitleaks' own logging is silenced so matched text
// can never reach a log stream.
func New() (*Scanner, error) {
	silenceOnce.Do(func() { logging.Logger = zerolog.Nop() })
	det, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return nil, err
	}
	return &Scanner{det: det}, nil
}

// Scan reports the secrets in text.
func (s *Scanner) Scan(text string) []Match {
	matches, _ := s.scan(text)
	return matches
}

func (s *Scanner) scan(text string) ([]Match, []string) {
	if text == "" {
		return nil, nil
	}
	s.mu.Lock()
	findings := s.det.DetectString(text)
	s.mu.Unlock()
	matches := make([]Match, 0, len(findings))
	secrets := make([]string, 0, len(findings))
	for _, f := range findings {
		matches = append(matches, Match{RuleID: f.RuleID, Line: f.StartLine + 1})
		secret := f.Secret
		if secret == "" {
			secret = f.Match
		}
		secrets = append(secrets, secret)
	}
	return matches, secrets
}

// Redact replaces every detected secret in text with Placeholder and
// returns the matches. If a secret is still detectable afterwards, it
// returns ErrResidualSecret and the redacted text must not be used.
func (s *Scanner) Redact(text string) (string, []Match, error) {
	matches, secrets := s.scan(text)
	if len(matches) == 0 {
		return text, nil, nil
	}
	out := text
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, Placeholder)
		}
	}
	if residual, _ := s.scan(out); len(residual) > 0 {
		return "", matches, ErrResidualSecret
	}
	return out, matches, nil
}
