// Package schema defines the Go types for Exemplar's configuration contracts:
// rules, global and repo config, eval cases, and the Security-owned platform
// files. The JSON Schema files in the repository's schema/ directory are the
// source of truth; drift_test.go fails when these types and the schemas
// disagree on field names.
package schema

// Rule kinds (rules.kind).
const (
	KindExemplar = "exemplar"
	KindCompiled = "compiled"
	KindBugClass = "bug_class"
)

// Rule statuses. Only active rules post; shadow rules run and are stored.
const (
	StatusCandidate = "candidate"
	StatusShadow    = "shadow"
	StatusActive    = "active"
	StatusKilled    = "killed"
)

// Severities.
const (
	SeverityBlocking   = "blocking"
	SeverityImportant  = "important"
	SeveritySuggestion = "suggestion"
)

// Fix policies (fix_policy).
const (
	FixNone     = "none"
	FixDescribe = "describe"
	FixSuggest  = "suggest"
)

// Rule is one CodeReviewRule file: exemplar-config/rules/<category>/<id>.yaml
// or <target repo>/.exemplar/rules/<id>.yaml.
type Rule struct {
	ID                string    `json:"id"`
	Kind              string    `json:"kind"`
	Category          string    `json:"category"`
	Severity          string    `json:"severity"`
	Owner             string    `json:"owner"`
	Status            string    `json:"status"`
	ReviewBy          string    `json:"review_by,omitempty"`
	FilePatterns      []string  `json:"file_patterns"`
	Description       string    `json:"description"`
	PreferredApproach string    `json:"preferred_approach,omitempty"`
	WhatToFlag        string    `json:"what_to_flag,omitempty"`
	Matcher           *Matcher  `json:"matcher,omitempty"`
	FixPolicy         string    `json:"fix_policy,omitempty"`
	Citation          *Citation `json:"citation,omitempty"`
	Export            *Export   `json:"export,omitempty"`
	EvalCases         string    `json:"eval_cases,omitempty"`
	ModelTier         string    `json:"model_tier,omitempty"`
	Context           string    `json:"context,omitempty"`
	Eval              string    `json:"eval,omitempty"`
	// LinterMap maps a linter check run name (repo config linters) to the
	// linter rule ids that report the same issue as this rule, for linter
	// dedupe.
	LinterMap map[string][]string `json:"linter_map,omitempty"`
	// Verify is "quorum" when the rule's findings go to the verifier
	// quorum (FR-V7) instead of one verifier.
	Verify string `json:"verify,omitempty"`
}

// Matcher is the deterministic matcher of a compiled rule.
type Matcher struct {
	Engine string `json:"engine"`
	Rule   string `json:"rule"`
}

// Citation is the provenance code attaches to every finding of a rule.
type Citation struct {
	Doc      string   `json:"doc,omitempty"`
	Comments []string `json:"comments,omitempty"`
	PRs      []string `json:"prs,omitempty"`
}

// Export controls what a rule contributes to coding-agent instruction files.
type Export struct {
	Agents  bool   `json:"agents"`
	Summary string `json:"summary,omitempty"`
}

// GlobalConfig is exemplar-config/global.yaml.
type GlobalConfig struct {
	Enabled          bool           `json:"enabled"`
	AutoApprove      bool           `json:"auto_approve"`
	CacheTTLSeconds  int            `json:"cache_ttl_seconds,omitempty"`
	Budgets          Budgets        `json:"budgets"`
	Degrade          Degrade        `json:"degrade"`
	MaxExemplarRules int            `json:"max_exemplar_rules"`
	Queues           Queues         `json:"queues"`
	Concurrency      Concurrency    `json:"concurrency"`
	Providers        Providers      `json:"providers"`
	MergeSafety      *MergeSafety   `json:"merge_safety,omitempty"`
	Verifier         VerifierConfig `json:"verifier"`
	CommentBudget    CommentBudget  `json:"comment_budget"`
	// LinterWait is how long a review waits for the repo's linters to
	// report before posting; nil means DefaultLinterWait.
	LinterWait *int `json:"linter_wait_seconds,omitempty"`
	// Posting shapes how findings post; nil means the defaults.
	Posting *Posting `json:"posting,omitempty"`
	// Rounds caps review rounds per PR; nil means DefaultRoundsMax.
	Rounds         *Rounds         `json:"rounds,omitempty"`
	BugLane        *GlobalBugLane  `json:"bug_lane,omitempty"`
	Baseline       Baseline        `json:"baseline"`
	EvalThresholds EvalThresholds  `json:"eval_thresholds"`
	Feedback       *FeedbackConfig `json:"feedback,omitempty"`
	Expiry         *Expiry         `json:"expiry,omitempty"`
}

// DefaultLinterWait is linter_wait_seconds when unset.
const DefaultLinterWait = 120

// LinterWaitSeconds is linter_wait_seconds, or its default.
func (g GlobalConfig) LinterWaitSeconds() int {
	if g.LinterWait == nil {
		return DefaultLinterWait
	}
	return *g.LinterWait
}

// Budgets holds per-run cost limits. Cost is informational; the soft
// ceiling only stops runaway reviews.
type Budgets struct {
	SoftCeilingUSD float64 `json:"soft_ceiling_usd"`
}

// Degrade configures degraded operation.
type Degrade struct {
	StageTimeoutSeconds int `json:"stage_timeout_seconds"`
}

// Queues configures per-repo review concurrency.
type Queues struct {
	PerRepoConcurrency int      `json:"per_repo_concurrency"`
	LargeRepos         []string `json:"large_repos,omitempty"`
}

// Concurrency configures agent fan-out.
type Concurrency struct {
	AgentsPerReview int `json:"agents_per_review"`
}

// Providers maps each gateway stage to its ordered failover list.
type Providers struct {
	Generator   []ProviderModel `json:"generator"`
	Verifier    []ProviderModel `json:"verifier"`
	Light       []ProviderModel `json:"light"`
	Categorizer *Categorizer    `json:"categorizer,omitempty"`
}

// ProviderModel is one entry in a stage's failover list. Generator entries
// for the primary provider name a model per tier; others name one model.
type ProviderModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Standard string `json:"standard,omitempty"`
	High     string `json:"high,omitempty"`
}

// Categorizer is the auto-approve risk categorizer's pair of independent
// providers.
type Categorizer struct {
	A ProviderModel `json:"a"`
	B ProviderModel `json:"b"`
}

// MergeSafety holds the deterministic merge_safety_score weights.
type MergeSafety struct {
	Weights map[string]float64 `json:"weights"`
}

// VerifierConfig configures the verification stage.
type VerifierConfig struct {
	ImportanceMin        float64 `json:"importance_min"`
	MaxCandidatesPerCall int     `json:"max_candidates_per_call"`
	Quorum               *Quorum `json:"quorum,omitempty"`
}

// Quorum is the bug lane's verifier quorum (FR-V7): N verifiers are
// asked and K must confirm.
type Quorum struct {
	N int `json:"n,omitempty"`
	K int `json:"k,omitempty"`
}

// Default quorum when global.yaml sets none.
const (
	DefaultQuorumN = 3
	DefaultQuorumK = 2
)

// QuorumNK is the verifier quorum, n verifiers and k confirmations, with
// defaults for what global.yaml leaves out.
func (v VerifierConfig) QuorumNK() (int, int) {
	n, k := DefaultQuorumN, DefaultQuorumK
	if q := v.Quorum; q != nil {
		if q.N >= 2 {
			n = q.N
		}
		if q.K >= 1 {
			k = q.K
		}
	}
	return n, k
}

// CommentBudget caps posted comments.
type CommentBudget struct {
	PerPR   int `json:"per_pr"`
	PerFile int `json:"per_file"`
}

// GlobalBugLane configures bug lane defaults.
type GlobalBugLane struct {
	MinChangedLines int                    `json:"min_changed_lines"`
	ImportanceMin   float64                `json:"importance_min"`
	Classes         map[string]BugLaneFlag `json:"classes"`
}

// BugLaneFlag enables one bug class.
type BugLaneFlag struct {
	Enabled bool `json:"enabled"`
	// PostOnCriticalPaths lets the class post findings in a repo's
	// bug_lane.critical_paths. Elsewhere, and by default, it stays in shadow.
	PostOnCriticalPaths bool `json:"post_on_critical_paths,omitempty"`
}

// Baseline configures the sampled baseline reviewer.
type Baseline struct {
	SampleRate float64  `json:"sample_rate"`
	Strata     []string `json:"strata,omitempty"`
}

// EvalThresholds gate rule and prompt changes in eval CI.
type EvalThresholds struct {
	MinCases    int                   `json:"min_cases"`
	Precision   float64               `json:"precision"`
	Recall      float64               `json:"recall"`
	Verifier    *VerifierThreshold    `json:"verifier,omitempty"`
	Categorizer *CategorizerThreshold `json:"categorizer,omitempty"`
}

// VerifierThreshold is the verifier calibration bar.
type VerifierThreshold struct {
	KnownWrongRefutedMin float64 `json:"known_wrong_refuted_min"`
	KnownTrueRefutedMax  float64 `json:"known_true_refuted_max"`
}

// CategorizerThreshold is the auto-approve risk categorizer calibration
// bar: at most KnownRiskyApprovedMax of labeled risky PRs may reach
// MinReviewConfidence, and at least KnownSafeApprovedMin of safe ones must.
type CategorizerThreshold struct {
	KnownRiskyApprovedMax float64 `json:"known_risky_approved_max"`
	KnownSafeApprovedMin  float64 `json:"known_safe_approved_min"`
	MinReviewConfidence   float64 `json:"min_review_confidence,omitempty"`
}

// FeedbackConfig configures feedback weighting and demotion.
type FeedbackConfig struct {
	EFPBar            float64            `json:"efp_bar"`
	RoleWeights       map[string]float64 `json:"role_weights"`
	ReactionWeight    float64            `json:"reaction_weight"`
	DemotionWilsonFlr float64            `json:"demotion_wilson_floor"`
	DemotionRequires  string             `json:"demotion_requires"`
	OwnerResponseDays int                `json:"owner_response_days"`
}

// Expiry configures rule review_by handling.
type Expiry struct {
	DefaultMonths       int `json:"default_months"`
	AutoDemoteAfterDays int `json:"auto_demote_after_days"`
}

// RepoConfig is exemplar-config/repos/<org>/<repo>.yaml.
type RepoConfig struct {
	Enabled      bool `json:"enabled"`
	ReviewDrafts bool `json:"review_drafts,omitempty"`
	// Shadow reviews the repo without writing to its PRs.
	Shadow              bool             `json:"shadow,omitempty"`
	Forks               *Forks           `json:"forks,omitempty"`
	SensitivePaths      []string         `json:"sensitive_paths,omitempty"`
	DisabledGlobalRules []string         `json:"disabled_global_rules,omitempty"`
	Linters             []string         `json:"linters,omitempty"`
	BugLane             *RepoBugLane     `json:"bug_lane,omitempty"`
	Exports             *RepoExports     `json:"exports,omitempty"`
	Context             *RepoContext     `json:"context,omitempty"`
	AutoApprove         *RepoAutoApprove `json:"auto_approve,omitempty"`
	ApprovalMode        string           `json:"approval_mode,omitempty"`
	// Rounds may lower global.yaml's round cap, never raise it.
	Rounds *Rounds `json:"rounds,omitempty"`
}

// DefaultClassThreshold is the most occurrences of one rule or bug class
// a run posts as separate comments; more post as one grouped comment
// (FR-C9).
const DefaultClassThreshold = 3

// Posting shapes how findings post.
type Posting struct {
	ClassThreshold int `json:"class_threshold,omitempty"`
}

// ClassThreshold is global.yaml's posting.class_threshold.
func (g GlobalConfig) ClassThreshold() int {
	if g.Posting == nil || g.Posting.ClassThreshold < 1 {
		return DefaultClassThreshold
	}
	return g.Posting.ClassThreshold
}

// DefaultRoundsMax is the round cap when global.yaml sets none (FR-C8).
const DefaultRoundsMax = 3

// Rounds is the round cap: after Max review rounds that posted new
// findings, a PR's new findings go to one summary comment until a person
// reviews it (FR-C8).
type Rounds struct {
	Max int `json:"max,omitempty"`
}

// RoundsMax is global.yaml's round cap.
func (g GlobalConfig) RoundsMax() int {
	if g.Rounds == nil || g.Rounds.Max < 1 {
		return DefaultRoundsMax
	}
	return g.Rounds.Max
}

// Forks controls fork PR review.
type Forks struct {
	Review bool `json:"review"`
}

// RepoBugLane narrows the bug lane for a repo.
type RepoBugLane struct {
	CriticalPaths []string `json:"critical_paths,omitempty"`
}

// RepoExports configures rule exports for a repo.
type RepoExports struct {
	Targets            []string `json:"targets,omitempty"`
	AutoMergeGenerated bool     `json:"auto_merge_generated,omitempty"`
}

// RepoContext configures repository context for exemplar-search.
type RepoContext struct {
	ExtraRepos      []string `json:"extra_repos,omitempty"`
	IncludeRoots    []string `json:"include_roots,omitempty"`
	DependentsDepth int      `json:"dependents_depth,omitempty"`
}

// RepoAutoApprove holds per-repo auto-approve settings.
type RepoAutoApprove struct {
	Enabled             bool        `json:"enabled"`
	Paths               []string    `json:"paths,omitempty"`
	AllowedAuthors      []string    `json:"allowed_authors,omitempty"`
	ApprovableOwners    []string    `json:"approvable_owners,omitempty"`
	BotAuthors          []BotAuthor `json:"bot_authors,omitempty"`
	ExtraDenylist       []string    `json:"extra_denylist,omitempty"`
	TitlePattern        string      `json:"title_pattern,omitempty"`
	MaxFiles            int         `json:"max_files,omitempty"`
	MaxChangedLines     int         `json:"max_changed_lines,omitempty"`
	MinReviewConfidence float64     `json:"min_review_confidence,omitempty"`
	MinMergeSafetyScore float64     `json:"min_merge_safety_score,omitempty"`
}

// BotAuthor is a bot identity allowed to be auto-approved within paths.
type BotAuthor struct {
	Identity string   `json:"identity"`
	Paths    []string `json:"paths"`
}

// EvalCases is exemplar-config/evals/<rule-id>/cases.yaml.
type EvalCases struct {
	Rule  string     `json:"rule"`
	Cases []EvalCase `json:"cases"`
}

// EvalCase is one eval case. Code is referenced by repo@sha, never copied.
type EvalCase struct {
	Name      string `json:"name"`
	Ref       string `json:"ref"`
	Synthetic bool   `json:"synthetic,omitempty"`
	Patch     string `json:"patch,omitempty"`
	// Paths are the files the rule runs on; default the Expect paths.
	Paths  []string      `json:"paths,omitempty"`
	Expect []Expectation `json:"expect"`
}

// Expectation is an expected finding overlapping lines [start, end].
type Expectation struct {
	Path  string `json:"path"`
	Lines []int  `json:"lines"`
}

// Eligibility is exemplar-config/platform/eligibility.yaml (CODEOWNERS:
// Security). Denylisted repos are never sent to a model provider.
type Eligibility struct {
	Denylist []DenylistEntry `json:"denylist"`
}

// DenylistEntry is one repo that must never be reviewed, mined or indexed.
type DenylistEntry struct {
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

// Retention is exemplar-config/platform/retention.yaml (CODEOWNERS:
// Security). Periods are in days.
type Retention struct {
	ApprovalDecisionsDays int `json:"approval_decisions_days"`
	FindingsDays          int `json:"findings_days"`
	FeedbackEventsDays    int `json:"feedback_events_days"`
	ModelCallsDays        int `json:"model_calls_days"`
	TranscriptsDays       int `json:"transcripts_days"`
	GHEventsPayloadDays   int `json:"gh_events_payload_days"`
}
