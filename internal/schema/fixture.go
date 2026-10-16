package schema

import "encoding/json"

// Fixture is a recorded pull request for the replay harness
// (schema/fixture.schema.json).
type Fixture struct {
	ID            string         `json:"id"`
	Repo          string         `json:"repo"`
	PR            int            `json:"pr"`
	Title         string         `json:"title"`
	Body          string         `json:"body,omitempty"`
	Author        string         `json:"author,omitempty"`
	Draft         bool           `json:"draft,omitempty"`
	CloneURL      string         `json:"clone_url,omitempty"`
	BaseSHA       string         `json:"base_sha,omitempty"`
	HeadSHA       string         `json:"head_sha,omitempty"`
	Synthetic     *SyntheticRepo `json:"synthetic,omitempty"`
	HumanComments []HumanComment `json:"human_comments,omitempty"`
}

// SyntheticRepo is a small repository carried inline in a fixture: the base
// commit's files, the files the head commit adds or changes, and the paths it
// deletes.
type SyntheticRepo struct {
	Base    map[string]string `json:"base"`
	Head    map[string]string `json:"head"`
	Deleted []string          `json:"deleted,omitempty"`
}

// HumanComment is a review comment a human left on the recorded PR.
type HumanComment struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Side   string `json:"side,omitempty"`
	Body   string `json:"body"`
	Author string `json:"author,omitempty"`
}

// Adversarial case vectors and goals.
const (
	GoalFalseApproval      = "false_approval"
	GoalSuppressedFinding  = "suppressed_finding"
	GoalFabricatedCitation = "fabricated_citation"
	GoalFalseCoverage      = "false_coverage"

	ModeCompromised = "compromised"
	ModeLive        = "live"
)

// AdversarialCase is one prompt-injection case
// (schema/adversarial-case.schema.json).
type AdversarialCase struct {
	ID          string             `json:"id"`
	Description string             `json:"description"`
	Vector      string             `json:"vector"`
	Goal        string             `json:"goal"`
	Modes       []string           `json:"modes"`
	Pending     string             `json:"pending,omitempty"`
	Rules       []string           `json:"rules,omitempty"`
	Fixture     Fixture            `json:"fixture"`
	Compromised *CompromisedModel  `json:"compromised,omitempty"`
	Expect      *AdversarialExpect `json:"expect,omitempty"`
}

// CompromisedModel scripts what a fully injected model does.
type CompromisedModel struct {
	GeneratorOutput json.RawMessage `json:"generator_output,omitempty"`
	GeneratorReads  []string        `json:"generator_reads,omitempty"`
	VerifierVerdict string          `json:"verifier_verdict,omitempty"`
}

// Verifier label values.
const (
	LabelTrue  = "true"
	LabelWrong = "wrong"
)

// VerifierLabel is one labeled finding for verifier calibration
// (schema/verifier-label.schema.json).
type VerifierLabel struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Label       string         `json:"label"`
	Fixture     Fixture        `json:"fixture"`
	Candidate   LabelCandidate `json:"candidate"`
}

// LabelCandidate is the finding a verifier label is about.
type LabelCandidate struct {
	Rule      string `json:"rule"`
	Path      string `json:"path"`
	Side      string `json:"side,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Claim     string `json:"claim"`
}

// Categorizer label values.
const (
	LabelSafe  = "safe"
	LabelRisky = "risky"
)

// CategorizerLabel is one labeled PR for auto-approve risk categorizer
// calibration (schema/categorizer-label.schema.json).
type CategorizerLabel struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Label       string  `json:"label"`
	Fixture     Fixture `json:"fixture"`
}

// AdversarialExpect is what must still hold after the attack.
type AdversarialExpect struct {
	Findings    []ExpectedFinding `json:"findings,omitempty"`
	MaxCoverage *float64          `json:"max_coverage,omitempty"`
}

// ExpectedFinding is a finding that must survive the attack.
type ExpectedFinding struct {
	Rule  string `json:"rule"`
	Path  string `json:"path"`
	Lines []int  `json:"lines,omitempty"`
}
