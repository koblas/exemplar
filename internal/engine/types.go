// Package engine runs one review: prepare the PR (eligibility, exclusions,
// secret redaction, scope), run the lanes, verify, validate in code, and
// assemble findings, coverage and the Stage 1 trace.
//
// Model output is untrusted (FR-T1–T6). The model-facing types here carry
// only what a model may say: a location, an explanation and an optional
// fix. Rule ids, citations, lanes, severities and coverage are attached by
// code.
package engine

import (
	"github.com/koblas/exemplar/internal/scope"
)

// Lanes (rule_runs.lane).
const (
	LaneCompiled = "compiled"
	LaneExemplar = "exemplar"
	LaneBug      = "bug"
	LaneBaseline = "baseline"
	LaneSecrets  = "secrets"
	// LaneCategorizer marks Stage 2's categorizer calls. They are not
	// rules and are not persisted as rule runs.
	LaneCategorizer = "categorizer"
)

// Rule run statuses (rule_runs.status).
const (
	RunOK           = "ok"
	RunZeroCoverage = "zero_coverage"
	RunSchemaError  = "schema_error"
	RunTimeout      = "timeout"
	RunShed         = "shed"
	RunDeferred     = "deferred"
	RunDegradedSkip = "degraded_skip"
	RunError        = "error"
)

// Finding states and suppression reasons (findings.state, suppressed_reason).
const (
	StateCandidate = "candidate"
	StatePosted    = "posted"
	StateShadow    = "shadow"
	StateDropped   = "dropped"

	SuppressRefuted     = "refuted"
	SuppressUncertain   = "uncertain"
	SuppressUnimportant = "unimportant"
	SuppressBadEvidence = "bad_evidence"
	SuppressBadAnchor   = "bad_anchor"
	SuppressDuplicate   = "duplicate"
	SuppressBudget      = "budget"
	// SuppressAlreadyPosted: an earlier review of this PR posted the same
	// fingerprint, which stays open there (spec §6.2 finding identity).
	SuppressAlreadyPosted = "already_posted"
	// SuppressRepoShadow: the finding would have posted, but the repo is
	// reviewed in shadow (spec §6.9). Shadow-rule findings carry no reason.
	SuppressRepoShadow = "repo_shadow"
	// SuppressLinterDuplicate: a configured linter already reports it on
	// the head (spec §6.10). It counts against neither the rule's
	// precision nor the author's feedback.
	SuppressLinterDuplicate = "linter_duplicate"
	// SuppressHumanDuplicate: a reviewer already raised it on the same
	// lines (spec §6.10). It counts as agreement with the reviewer.
	SuppressHumanDuplicate = "human_duplicate"
)

// Verification verdicts.
const (
	VerdictConfirmed = "confirmed"
	VerdictRefuted   = "refuted"
	VerdictUncertain = "uncertain"
)

// SecretRuleID is the deterministic finding for secrets in a diff.
const SecretRuleID = "possible-secret-in-diff"

// Violation is the model-facing output type for rule agents (spec §6.5).
// It has no rule id, citation or coverage field by design.
type Violation struct {
	Path         string `json:"path"`
	Side         string `json:"side"                    jsonschema:"enum=RIGHT,enum=LEFT"`
	StartLine    int    `json:"start_line"              jsonschema:"minimum=1"`
	EndLine      int    `json:"end_line"                jsonschema:"minimum=1"`
	Explanation  string `json:"explanation"             jsonschema:"maxLength=1500"`
	SuggestedFix string `json:"suggested_fix,omitempty" jsonschema:"maxLength=1500"`
}

// AgentOutput is the whole model answer for one rule.
type AgentOutput struct {
	Violations []Violation `json:"violations" jsonschema:"maxItems=20"`
}

// Finding is a candidate or posted finding with code-attached provenance.
type Finding struct {
	RuleID string
	// ID is findings.id once persisted.
	ID        int64
	RuleRunID int64 // set when persisted
	runIndex  int   // index into Result.Runs
	Lane      string
	Severity  string
	Category  string
	CCLabel   string
	Citation  string
	Anchor    scope.Anchor
	Message   string
	Fix       string
	// FixValid is set when Fix was validated (spec §6.11): only a valid
	// fix is posted, as a suggestion. FixProblem says why one was not.
	FixValid   *bool
	FixProblem string
	// HumanMatch is the reviewer comment a human_duplicate repeats.
	HumanMatch *HumanComment

	Fingerprint string
	// AnchorCode and Ordinal are what Fingerprint was computed from: the
	// normalized anchor with context, and k (FR-C4 re-anchoring).
	AnchorCode string
	Ordinal    int

	VerifyVerdict     string
	VerifyImportance  *float64
	VerifyModel       string
	VerifySameFamily  bool
	VerifyEvidenceRaw []byte

	State      string
	Suppressed string
}

// RunResult is one rule (or lane) run.
type RunResult struct {
	RuleID      string
	RuleVersion string
	Lane        string
	ScopedFiles []string
	Status      string
	Coverage    float64
	Model       string
	Family      string
	Failover    bool
	Usage       Usage
	CostUSD     float64
	LatencyMS   int
	Budget      string
	// ContextDegraded is set when a repo-context tool answered in
	// degraded mode or failed during the run (spec §6.4.4).
	ContextDegraded bool
	Detail          string // why a run failed, for the check run
	// Carried is set for a rule carried forward from an earlier review of
	// identical code instead of run (spec §6.2); CarriedFrom is the rule
	// run that did the work. A carried run re-checked nothing, so it never
	// closes an earlier finding.
	Carried     bool
	CarriedFrom int64
}

// Usage sums token usage across an agent's turns.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int
}

func (u *Usage) add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}
