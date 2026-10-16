package schema

import (
	"errors"
	"strings"
	"testing"
)

// compiledRuleYAML is the reference compiled-rule example.
const compiledRuleYAML = `
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
citation:
  doc: https://docs.internal/security/sql
export:
  agents: true
  summary: "Never build SQL by string concatenation; use placeholders."
eval_cases: evals/no-raw-sql-concat/
`

func TestDecodeRule(t *testing.T) {
	var r Rule
	if err := Decode(DocRule, []byte(compiledRuleYAML), &r); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if r.ID != "no-raw-sql-concat" || r.Kind != KindCompiled || r.Matcher == nil || r.Matcher.Engine != "ast-grep" {
		t.Fatalf("decoded rule = %+v", r)
	}
	if r.ReviewBy != "2027-10-01" {
		t.Errorf("review_by = %q, want 2027-10-01", r.ReviewBy)
	}
	if r.Export == nil || !r.Export.Agents {
		t.Errorf("export not decoded: %+v", r.Export)
	}
}

func TestDecodeRuleRejects(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
	}{
		{"unknown field", func(s string) string { return s + "rule_id_override: x\n" }},
		{"bad kind", func(s string) string { return strings.Replace(s, "kind: compiled", "kind: magic", 1) }},
		{"bad severity", func(s string) string { return strings.Replace(s, "severity: blocking", "severity: critical", 1) }},
		{"compiled without matcher", func(s string) string {
			before, _, _ := strings.Cut(s, "matcher:")
			j := strings.Index(s, "description:")
			return before + s[j:]
		}},
		{"exemplar with matcher", func(s string) string { return strings.Replace(s, "kind: compiled", "kind: exemplar", 1) }},
		{"bad owner", func(s string) string { return strings.Replace(s, `"@security/appsec"`, "security", 1) }},
		{"empty file patterns", func(s string) string {
			return strings.Replace(s, `file_patterns: ["**/*.go", "!**/*_test.go"]`, "file_patterns: []", 1)
		}},
		{"bad citation uri", func(s string) string {
			return strings.Replace(s, "https://docs.internal/security/sql", "not a uri", 1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r Rule
			err := Decode(DocRule, []byte(tt.edit(compiledRuleYAML)), &r)
			if _, ok := errors.AsType[*ValidationError](err); !ok {
				t.Fatalf("Decode = %v, want ValidationError", err)
			}
		})
	}
}

const globalYAML = `
enabled: true
auto_approve: true
cache_ttl_seconds: 900
budgets:
  soft_ceiling_usd: 10
degrade:
  stage_timeout_seconds: 300
max_exemplar_rules: 40
queues:
  per_repo_concurrency: 4
  large_repos: [acme/game-engine]
concurrency:
  agents_per_review: 3
providers:
  generator:   [{ provider: anthropic, standard: claude-sonnet-5-5, high: claude-opus-5-5 }, { provider: provider-b, model: "b-model" }]
  verifier:    [{ provider: provider-b, model: "b-model" }, { provider: anthropic, model: claude-opus-5-5 }]
  light:       [{ provider: anthropic, model: claude-haiku-5-5 }, { provider: provider-b, model: "b-light" }]
  categorizer: { a: { provider: anthropic, model: claude-opus-5-5 }, b: { provider: provider-b, model: "b-model" } }
merge_safety:
  weights: { incident_180d: 0.30, revert_180d: 0.20, churn_30d: 0.10, no_tests: 0.15, sensitive_path: 0.25, new_author: 0.10, force_push: 0.05 }
verifier:
  importance_min: 0.5
  max_candidates_per_call: 8
comment_budget:
  per_pr: 7
  per_file: 3
linter_wait_seconds: 120
bug_lane:
  min_changed_lines: 80
  importance_min: 0.6
  classes:
    bug.nil-deref:      { enabled: true }
    bug.resource-leak:  { enabled: false }
baseline:
  sample_rate: 0.10
  strata: [repo, size_bucket]
eval_thresholds:
  min_cases: 10
  precision: 0.80
  recall: 0.50
  verifier: { known_wrong_refuted_min: 0.90, known_true_refuted_max: 0.10 }
feedback:
  efp_bar: 0.10
  role_weights: { author: 1, reviewer: 2, owner: 4, observer: 0 }
  reaction_weight: 0.5
  demotion_wilson_floor: 0.80
  demotion_requires: owner_adjudication_or_two_non_author_actors
  owner_response_days: 14
expiry:
  default_months: 12
  auto_demote_after_days: 14
`

func TestDecodeGlobal(t *testing.T) {
	var g GlobalConfig
	if err := Decode(DocGlobal, []byte(globalYAML), &g); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if g.Concurrency.AgentsPerReview != 3 || len(g.Providers.Generator) != 2 || g.Providers.Generator[0].Standard != "claude-sonnet-5-5" {
		t.Fatalf("decoded = %+v", g)
	}
	tooMany := strings.Replace(globalYAML, "agents_per_review: 3", "agents_per_review: 4", 1)
	if err := Decode(DocGlobal, []byte(tooMany), &g); err == nil {
		t.Error("agents_per_review 4 accepted; FR-E4 caps it at 3")
	}
	noModel := strings.Replace(globalYAML, `{ provider: provider-b, model: "b-model" }]`, `{ provider: provider-b }]`, 1)
	if err := Decode(DocGlobal, []byte(noModel), &g); err == nil {
		t.Error("provider entry without a model accepted")
	}
}

func TestDecodeRepoAndEval(t *testing.T) {
	repo := `
enabled: true
review_drafts: false
forks:
  review: false
sensitive_paths: ["src/auth/**", "migrations/**", "infra/**"]
disabled_global_rules: [legacy-logging-format]
linters: [clang-tidy, cpplint]
bug_lane:
  critical_paths: ["src/net/**", "src/sim/**"]
exports:
  targets: [agents_md, claude_md, copilot]
  auto_merge_generated: true
context:
  extra_repos: [acme/engine-shared]
  include_roots: ["src/"]
  dependents_depth: 1
auto_approve:
  enabled: true
  paths: ["docs/**", "**/*.md", "config/feature-flags/**"]
  allowed_authors: ["@acme/engine-team"]
  bot_authors:
    - identity: "renovate[bot]"
      paths: ["config/feature-flags/**"]
  extra_denylist: ["config/feature-flags/prod/**"]
  title_pattern: "^(docs|chore|flags)(\\(.+\\))?: "
  max_files: 10
  max_changed_lines: 1000
  min_review_confidence: 0.8
  min_merge_safety_score: 0.7
`
	var rc RepoConfig
	if err := Decode(DocRepo, []byte(repo), &rc); err != nil {
		t.Fatalf("Decode repo: %v", err)
	}
	if rc.AutoApprove == nil || len(rc.AutoApprove.BotAuthors) != 1 {
		t.Fatalf("repo = %+v", rc)
	}

	evals := `
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
`
	var ec EvalCases
	if err := Decode(DocEvalCases, []byte(evals), &ec); err != nil {
		t.Fatalf("Decode evals: %v", err)
	}
	if len(ec.Cases) != 3 || ec.Cases[2].Patch == "" {
		t.Fatalf("evals = %+v", ec)
	}
	noPatch := strings.Replace(evals, "    patch: patches/injected-01.diff\n", "", 1)
	if err := Decode(DocEvalCases, []byte(noPatch), &ec); err == nil {
		t.Error("synthetic case without patch accepted")
	}
}

func TestDecodeFixture(t *testing.T) {
	synthetic := `
id: sample-1
repo: acme/sandbox
pr: 1
title: Add fetch loop
synthetic:
  base: { "main.go": "package main\n" }
  head: { "main.go": "package main\n\nfunc main() {}\n" }
human_comments:
  - { path: main.go, line: 3, body: "nit" }
`
	var f Fixture
	if err := Decode(DocFixture, []byte(synthetic), &f); err != nil {
		t.Fatalf("Decode synthetic: %v", err)
	}
	both := synthetic + "base_sha: abcdef1\nhead_sha: abcdef2\n"
	if err := Decode(DocFixture, []byte(both), &f); err == nil {
		t.Error("fixture with both synthetic repo and shas accepted")
	}
	neither := `{id: x, repo: a/b, pr: 1, title: t}`
	if err := Decode(DocFixture, []byte(neither), &f); err == nil {
		t.Error("fixture with neither synthetic repo nor shas accepted")
	}
}
