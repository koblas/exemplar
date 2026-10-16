// Package evaluate is the wire format of exemplar-api's /v1/evaluate:
// `exemplar review` sends a working-tree
// diff against a pushed base commit; exemplar-worker reviews the base
// with the diff applied, every lane and the verifier, and the requester
// polls for what would have been posted.
//
// Mode eval is config CI's: Repo is exemplar-config, BaseSHA
// the config PR's merge-base and HeadSHA its head, both pushed. The
// worker evaluates the rules the PR changes (plus Rules) on their eval
// cases at the head, as `exemplar eval` does, and returns the reports.
package evaluate

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxDiff bounds a request's diff (engine.MaxPatch).
const MaxDiff = 1 << 20

// Request is POST /v1/evaluate.
type Request struct {
	Repo    string `json:"repo"`     // owner/name
	BaseSHA string `json:"base_sha"` // a commit GitHub has: the merge-base with the default branch
	Diff    string `json:"diff"`     // git diff against BaseSHA
	// Agent is the coding agent the caller declares it runs for, if any.
	Agent string `json:"agent,omitempty"`
	// Mode is review (the default) or eval.
	Mode string `json:"mode,omitempty"`
	// HeadSHA is the config PR's head (eval).
	HeadSHA string `json:"head_sha,omitempty"`
	// Rules are rule ids to evaluate besides those the PR changes (eval).
	Rules []string `json:"rules,omitempty"`
}

// Modes.
const (
	ModeReview = "review"
	ModeEval   = "eval"
)

// MaxRules bounds Rules.
const MaxRules = 50

var (
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	shaRE   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	agentRE = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
	ruleRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
)

// Validate checks a request before anything is stored.
func (r Request) Validate() error {
	switch {
	case !repoRE.MatchString(r.Repo):
		return errors.New("repo must be owner/name")
	case !shaRE.MatchString(r.BaseSHA):
		return errors.New("base_sha must be a full commit SHA")
	case r.Agent != "" && !agentRE.MatchString(r.Agent):
		return errors.New("agent must be 1-64 of [a-z0-9_.-]")
	case r.Mode == ModeEval:
		switch {
		case !shaRE.MatchString(r.HeadSHA):
			return errors.New("eval needs head_sha, a full commit SHA")
		case r.Diff != "":
			return errors.New("eval takes no diff: the head is a pushed commit")
		case len(r.Rules) > MaxRules:
			return fmt.Errorf("at most %d rules", MaxRules)
		}
		for _, id := range r.Rules {
			if !ruleRE.MatchString(id) {
				return fmt.Errorf("rule id %q is not valid", id)
			}
		}
		return nil
	case r.Mode != "" && r.Mode != ModeReview:
		return errors.New("mode must be review or eval")
	case r.HeadSHA != "" || len(r.Rules) > 0:
		return errors.New("head_sha and rules are for mode eval")
	case r.Diff == "" || len(r.Diff) > MaxDiff:
		return errors.New("diff must be 1 byte to 1 MiB")
	case !strings.HasPrefix(r.Diff, "diff --git "):
		return errors.New("diff must be a git diff")
	}
	return nil
}

// Statuses.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusRefused = "refused" // ineligible repo, a patch that does not apply, no installation
	StatusFailed  = "failed"
)

// Finding is one finding that would have been posted.
type Finding struct {
	RuleID     string `json:"rule_id"`
	Lane       string `json:"lane"`
	Severity   string `json:"severity"`
	Category   string `json:"category,omitempty"`
	Path       string `json:"path"`
	Side       string `json:"side"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"` // a validated fix
}

// Result is a finished evaluation.
type Result struct {
	Findings []Finding `json:"findings"`
	Coverage float64   `json:"coverage"`
	Degraded bool      `json:"degraded,omitempty"`
	// Incomplete, when set, means the review did not complete and the
	// findings are not the whole answer.
	Incomplete string `json:"incomplete,omitempty"`
	// HeldBack is findings the comment budget would have held back.
	HeldBack int `json:"held_back,omitempty"`
	// Rules is how many rules ran.
	Rules int `json:"rules"`
	// Eval is mode eval's answer (nothing else is set then).
	Eval *EvalResult `json:"eval,omitempty"`
}

// EvalResult is the eval check: per-rule reports (evalrun.Report, kept
// raw so this package stays light), their summary table, and whether
// every rule met global.yaml eval_thresholds.
type EvalResult struct {
	Reports  json.RawMessage `json:"reports"`
	Markdown string          `json:"markdown"`
	Pass     bool            `json:"pass"`
	Rules    []string        `json:"rules"`
}

// Status is GET /v1/evaluate/{id}, and the POST's answer.
type Status struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Reason  string  `json:"reason,omitempty"`
	Repo    string  `json:"repo"`
	BaseSHA string  `json:"base_sha"`
	Result  *Result `json:"result,omitempty"`
	CostUSD float64 `json:"cost_usd"`
}

// Final reports whether the evaluation is over.
func (s Status) Final() bool {
	return s.Status == StatusDone || s.Status == StatusRefused || s.Status == StatusFailed
}

// TryResult is a web UI playground run's answer: a draft
// rule alone on one PR.
type TryResult struct {
	// Ran is false when the rule could not run; Note says why.
	Ran      bool         `json:"ran"`
	Note     string       `json:"note,omitempty"`
	Head     string       `json:"head,omitempty"`
	Findings []TryFinding `json:"findings"`
}

// TryFinding is one of the draft rule's findings. Kept is false for one
// the verifier or validation dropped, with Reason.
type TryFinding struct {
	Path    string `json:"path"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Message string `json:"message"`
	Kept    bool   `json:"kept"`
	Reason  string `json:"reason,omitempty"`
}
