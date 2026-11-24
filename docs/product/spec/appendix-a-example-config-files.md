## Appendix A — Example config files

```yaml
# exemplar-config/global.yaml
enabled: true
auto_approve: true
extends: [exemplar:recommended]   # presets and rule_settings: §3.1
cache_ttl_seconds: 900
budgets:
  soft_ceiling_usd: 10          # per run; triggers shedding (§6.15), not a goal
  # mcp_check_usd: 1             # Appendix C (deferred)
degrade:
  stage_timeout_seconds: 300
max_exemplar_rules: 40          # overflow is ranked by expected value (§6.3 step 6)
queues:
  per_repo_concurrency: 4         # fairness limit for small repos
  large_repos: [acme/game-engine] # monorepo(s): per_repo_concurrency 32
concurrency:
  agents_per_review: 3          # shared by Lanes 2 and 3
  rules_per_agent: 1            # 1–5; above 1 only with a golden-set run attached to the config PR (§6.3 step 7)
providers:                      # ordered lists = failover order; providers must be in internal/llm/providers.go
  generator:   [{ provider: anthropic, standard: claude-sonnet-5-5, high: claude-opus-5-5 }, { provider: provider-b, model: "<approved model id>" }]
  verifier:    [{ provider: provider-b, model: "<approved model id>" }, { provider: anthropic, model: claude-opus-5-5 }]
  light:       [{ provider: anthropic, model: claude-haiku-5-5 }, { provider: provider-b, model: "<approved light model id>" }]
  categorizer: { a: { provider: anthropic, model: claude-opus-5-5 }, b: { provider: provider-b, model: "<approved model id>" } }
merge_safety:
  weights: { incident_180d: 0.30, revert_180d: 0.20, churn_30d: 0.10, no_tests: 0.15, sensitive_path: 0.25, new_author: 0.10, force_push: 0.05 }
verifier:
  importance_min: 0.5
  max_candidates_per_call: 8
  quorum: { n: 3, k: 2 }        # Lane 3 always; a rule opts in with `verify: quorum` (§6.8)
rounds:
  max: 3                        # review rounds per PR before inline posting stops (§6.2, FR-C8)
posting:
  class_threshold: 3            # occurrences of one rule or class that post as one grouped comment (FR-C9)
comment_budget:
  per_pr: 7
  per_file: 3
linter_wait_seconds: 120
review_by_defaults:             # months from promotion, per category (§8.10)
  Security: 6
  Blocker: 6
  Performance: 6
  Correctness: 12
  Style: 18
  Nit: 18
bug_lane:
  min_changed_lines: 80
  importance_min: 0.6
  classes:
    bug.nil-deref:      { enabled: true }
    bug.error-handling: { enabled: true }
    bug.concurrency:    { enabled: true }
    bug.resource-leak:  { enabled: false }
    bug.off-by-one:     { enabled: false }
    bug.contract:       { enabled: true }
baseline:
  sample_rate: 0.10
  strata: [repo, size_bucket]   # ≤50 / ≤300 / >300 changed lines
eval_thresholds:
  min_cases: 10
  precision: 0.80
  recall: 0.50
  verifier: { known_wrong_refuted_min: 0.90, known_true_refuted_max: 0.10 }   # FR-X9, gates prompt/model changes and same_family (§6.13)
  change_gate:                  # FR-X11 + FR-X12 (§8.8): a prompt, tool, model or provider change must hold both
    incident_recall_drop_max: 0.0
    clean_false_alarm_rise_max: 0.0
    clean_harmful_rise_max: 0.0
feedback:
  efp_bar: 0.10
  role_weights: { author: 1, reviewer: 2, owner: 4, observer: 0 }
  reaction_weight: 0.5            # 👎 counts for acceptance only, never eFP
  demotion_wilson_floor: 0.80   # Wilson lower bound on unweighted precision, last 30 judged findings; weighted eFP must also exceed efp_bar
  demotion_requires: owner_adjudication_or_two_non_author_actors
  owner_response_days: 14
expiry:
  default_months: 12
  auto_demote_after_days: 14
```

```yaml
# exemplar-config/repos/acme/game-engine.yaml
enabled: true                 # kill switch for this repo
shadow: false                 # true for a pilot: review and store, post nothing (§6.9)
review_drafts: false
forks:
  review: false
sensitive_paths: ["src/auth/**", "migrations/**", "infra/**"]
disabled_global_rules: [legacy-logging-format]
linters: [clang-tidy, cpplint]           # check runs used for dedupe
bug_lane:
  critical_paths: ["src/net/**", "src/sim/**"]
exports:
  targets: [agents_md, claude_md, copilot]
  auto_merge_generated: true
context:
  extra_repos: [acme/engine-shared]       # mirrored + indexed in exemplar-search; same visibility tier only.
                                          # Without search, this keeps bug.contract skipped and auto-approve off (§6.4.6)
  include_roots: ["src/"]                 # C/C++ package identity for import patterns
  dependents_depth: 1                     # max 2
importance_priors:                        # proposed by exemplar-learn priors from "Not important here" (§8.11); never Security or blocking
  - { prefix: "tools/scripts/", category: Performance, importance_min: 0.8 }
  - { prefix: "examples/", category: Style, suppress: true }
auto_approve:
  enabled: true
  paths: ["docs/**", "**/*.md", "config/feature-flags/**"]
  allowed_authors: ["@acme/engine-team"]          # humans; exempt from min_author_merges
  bot_authors:                                    # FR-A6: explicit, narrower scope
    - identity: "renovate[bot]"
      paths: ["config/feature-flags/**"]
  extra_denylist: ["config/feature-flags/prod/**"] # can only add to the hardcoded denylist
  title_pattern: "^(docs|chore|flags)(\\(.+\\))?: "  # informational only
  max_files: 10
  max_changed_lines: 1000
  min_author_merges: 10          # FR-A10: merged PRs by this author in each touched ownership area; never below 1
  max_owner_areas: 1             # FR-A11: CODEOWNERS entries touched; never above 3
  shared_paths: ["src/common/**", "lib/**"]   # FR-A11: touching these is never auto-approvable; may extend, never narrow
  unowned_paths: refuse          # refuse | allow (paths with no CODEOWNERS entry)
  base_drift_commits: 20         # FR-A9: approval re-judged when the base moves further than this
  min_review_confidence: 0.8     # Stage 2, two-model agreement
  min_merge_safety_score: 0.7    # Stage 2, deterministic
```

```yaml
# exemplar-config/rules/security/no-raw-sql-concat.yaml  (a compiled rule)
id: no-raw-sql-concat
kind: compiled
category: Security
severity: blocking
owner: "@security/appsec"
status: active
review_by: 2027-10-01
file_patterns: ["**/*.go", "!**/*_test.go"]
matcher:
  engine: ast-grep
  rule: |
    id: no-raw-sql-concat
    language: go
    rule:
      pattern: $DB.Query($A + $B, $$$)
description: |
  Build SQL with placeholders, not string concatenation, to avoid injection.
fix_policy: describe
linter_map: { gosec: [G202] }    # linter dedupe (§6.10)
citation:
  doc: https://docs.internal/security/sql
export:
  agents: true
  summary: "Never build SQL by string concatenation; use placeholders."
eval_cases: evals/no-raw-sql-concat/
```

```yaml
# exemplar-config/evals/blocking-fetch-in-loop/cases.yaml
rule: blocking-fetch-in-loop
cases:
  - name: fetch inside update loop (from PR 48213)
    ref: acme/game-engine@9b1e2f7
    expect:
      - path: src/net/fetch.cpp
        lines: [140, 150]
  - name: awaited task, should not flag
    ref: acme/game-engine@c44a0de
    expect: []
  - name: injected violation in loader loop
    synthetic: true
    ref: acme/game-engine@c44a0de
    patch: patches/injected-01.diff
    expect:
      - path: src/net/loader.cpp
        lines: [88, 91]
```

---
