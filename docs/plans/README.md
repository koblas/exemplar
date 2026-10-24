# Plans and status

The map of Exemplar's plans: which document says what, how to read the references in code and commits, and where each part of the roadmap stands. Status as of 2026-10-13.

## Documents

| Document | What it is |
|---|---|
| [`docs/spec/technical-spec.md`](../spec/technical-spec.md) | **The technical specification** (Draft 1.3), and the **canonical copy**: change the spec here. It covers architecture, pipeline, data model, security and the delivery plan, and is the source for every `§` reference. |
| [`docs/spec/prd.md`](../spec/prd.md) | **The PRD** (v0.8), and the **canonical copy**: change the PRD here. It covers the problem, goals, personas, and the functional requirements (`FR-…`) the spec implements, with sample interactions (§17). |
| [`docs/plans/phase-0.md`](phase-0.md) | The Phase 0 implementation plan (§1–§8), its status (§9), and a dated log of the head starts up to 2026-10-17. Each "head start" section records what was built, the decisions taken, how it is tested, and what is still missing. No new sections go here. |
| [`docs/plans/ready-to-install.md`](ready-to-install.md) | **The current plan**: making a first installation short and checkable (setup in the cluster, the GitHub App manifest flow, one root key, a setup page, release manifests). New work is logged in its §7. |
| [`docs/plans/backlog.md`](backlog.md) | **The backlog**: potential enhancements that are not in the PRD or any plan yet, each with where it came from, what it would change and what it would cost. An item leaves the backlog when it is adopted into the PRD (as a requirement) or declined (recorded there). |
| [`docs/security/phase-0-data-handling.md`](../security/phase-0-data-handling.md) | What the code does with repository data, for Security's review and sign-off. |
| [`docs/reference/workloads.md`](../reference/deployment.md) | How to deploy: images, manifests, what an installation supplies, and the rollout risks. |
| [`.claude/CLAUDE.md`](../../.claude/CLAUDE.md) | Repository conventions: binaries, commands, checks, safety rules. |

## Reading the references

- **`§6.15`** is a section of the spec.
- **`FR-D6`** is a functional requirement in the [PRD](../spec/prd.md). The letter is the requirement family. Each family has its own PRD section, and is specified in one place in the spec (spec §1.1).
- **`T-0.3`** is a Phase 0 acceptance test (spec §16).

| Family | PRD area | PRD | Spec | Status |
|---|---|---|---|---|
| FR-E | Engine: review every PR with org rules, one agent per rule, never block, repo context (E12–E14) | §8.1–8.3 | §5, §6.4–6.7, §6.12 | Built: three lanes, agent harness, exemplar-search tools, linter dedupe (E7) and human-comment dedupe (E15). Lane 1 needs ast-grep/Semgrep installed. |
| FR-V | Verification with model diversity | §8.4 | §6.8 | Built, plus calibration. There is no provider from a second model family yet, so every finding is flagged same-family (plan §9, decision 7). |
| FR-T | Model output is untrusted and checked by code | §8.5 | §6.9, §13 | Built. |
| FR-P | Comment budget, labels, SARIF, dedupe | §8.6 | §6.10, §6.12 | Budget, ranking, Conventional Comments labels, exact dedupe, linter dedupe (before posting and late CI) human-comment dedupe (FR-E15), and SARIF for Security findings with dismissals as feedback are built, as are code-scanning alerts as a linter-dedupe source. |
| FR-F | Validated one-click fixes | §8.7 | §6.11 | F1–F2 built: fixes are validated in code (Go with go/parser and gofmt; C, C++, C#, Java, JavaScript, TypeScript, Python, Ruby and Rust with tree-sitter) and posted as suggestion blocks. F3 (applied-suggestion tracking) is built. F4 is Phase 5. |
| FR-B | Structured feedback | §9 | §9 | The `feedback_events` table, with `human_agreed` (B8) and `applied_suggestion` recorded by code, slash commands (B1) including owner adjudication, the roll-ups (precedence, acceptance, weighted eFP: `finding_outcomes`, `rule_weekly_stats`), and nightly auto-demotion (§9.4). Signed links, reply classification, reactions, weighting roll-ups, adjudication and demotion are not built. |
| FR-A | Auto-approve behind deterministic gates | §12 | §6.13 | Built: Stage 1, Stage 2, approval and revocation. The `merge_policy_check` status and the audit boards are not built. |
| FR-C | PR lifecycle | §8.9 | §6.2 | Built, except FR-C3 dismissed-stays-dismissed, which needs feedback events. |
| **FR-D** | **Degraded operation** | **§8.10** | **§6.15** | **D2 fan-out cap, D3 provider failover, D4 explicit coverage, D5 degraded runs cannot approve and D6 backpressure (per-repo concurrency) are built. D1, the shedding order, is partly built: only step 1, shedding bug classes not yet started once the budget leaves `ok`. Step 2 (exemplar rules, lowest precision first), step 3 (verifier importance scoring) and the p95-latency trigger are not.** |
| FR-H | Data handling and eligibility | §8.11 | §13.1 | Built, including retention. The periods await Security sign-off. |
| FR-S | Shift left: rule exports, pre-PR checks | §10 | §10 | Export-block approval and summary lint for config PRs; [rule exports](phase-0.md#head-start-rule-exports-2026-10-14) (daily, one PR per repo) with [drift](phase-0.md#head-start-export-drift-2026-10-14) on the shift-left board and in the platform digest; [`exemplar review`](phase-0.md#head-start-exemplar-review-and-v1evaluate-2026-10-14) through `/v1/evaluate`. MCP is deferred (Appendix C). |
| FR-X | Evaluation before rollout | §11 | §8.8–8.9, §11 | Built: eval CI, replay, adversarial set, verifier and categorizer calibration, baseline reviewer, and mutation evals for bug classes (Go). |
| FR-L | Learning plane: mining, extraction, ranking | §6 | §8 | Built through candidate PRs. Needs real data to meet the Phase 1 exit criteria. |

**Where FR-D6 fits:** it's the last of the degraded-operation requirements (PRD §8.10).
- **The PRD asks** that reviews queue per repo, that a burst (a mass rebase, a release-branch cut) drains in order with incremental review, and that queue depth be visible to the platform team.
- **The spec (§6.15, "Backpressure") makes that concrete:** at most `queues.per_repo_concurrency` reviews of a repo at once, 32 for the monorepo, so one busy repo can't starve the rest.
- **Built:** the limit, in [per-repo concurrency](phase-0.md#head-start-per-repo-concurrency-2026-10-13), as part of the online review path that Phase 2 stands up.
- **Not built:** queue-depth visibility, which waits for the Grafana boards.

## Roadmap

Phases are from spec §16. Work has gone past Phase 0 in "head starts", each logged in the Phase 0 plan. Work since 2026-10-17 is making Exemplar ready to install, in [its own plan](ready-to-install.md). **Built** means implemented and tested in this repository; nothing has run against a real GitHub org, real model providers or production data yet. Every phase's exit criteria need those.

### Phase 0 — Foundations

Built; see [plan §9](phase-0.md#9-status-2026-10-12).

Waiting on outside inputs:
- the sandbox GitHub org and App registration (T-0.1, T-0.3);
- the monorepo benchmark run (T-0.4);
- Security sign-off of eligibility and retention.

### Phase 1 — Mine and curate

**Built:**
- [ingest](phase-0.md#phase-1-head-start-ingest-2026-10-13), with [path events](phase-0.md#phase-1-head-start-path-events-2026-10-13) and [revert seeds](phase-0.md#phase-1-head-start-revert-seeds-2026-10-13);
- [extraction](phase-0.md#phase-1-head-start-extraction-driver-2026-10-12);
- [Python jobs](phase-0.md#phase-1-head-start-python-setup-2026-10-12): [clustering](phase-0.md#phase-1-head-start-clustering-job-2026-10-12) and [ranking](phase-0.md#phase-1-head-start-rank-job-2026-10-12);
- [candidate PRs](phase-0.md#phase-1-head-start-config-writer-2026-10-12), with [volume and linter-overlap checks](phase-0.md#phase-1-head-start-volume-estimate-and-linter-overlap-2026-10-13);
- [compile-to-deterministic](phase-0.md#phase-1-head-start-compile-to-deterministic-2026-10-13);
- [`/exemplar try`](phase-0.md#phase-1-head-start-exemplar-try-2026-10-13);
- [eval generation](phase-0.md#phase-1-head-start-eval-generation-2026-10-12) and [eval CI](phase-0.md#phase-1-head-start-eval-ci-2026-10-13);
- [export approval](phase-0.md#phase-1-head-start-export-approval-and-summary-lint-2026-10-13);
- [verifier calibration](phase-0.md#phase-1-head-start-verifier-calibration-2026-10-13).

**Not met:** the exit criteria (≥ 30 active rules, extractor in band, ≥ 15% compiled), which need mined data.

### Phase 2 — Shadow

**Built:**
- the [webhook receiver and event routing](phase-0.md#head-start-the-webhook-receiver-and-event-routing-2026-10-13);
- the [production worker](phase-0.md#head-start-the-production-worker-2026-10-13) (orchestrator, worker and poster in one process);
- exemplar-search: [mirror](phase-0.md#head-start-exemplar-search-mirror-2026-10-13) and [index](phase-0.md#head-start-zoekt-index-2026-10-13);
- [redelivery reconciler and sweeper](phase-0.md#head-start-the-redelivery-reconciler-and-the-pr-sweeper-2026-10-13);
- [superseding](phase-0.md#head-start-superseding-running-reviews-2026-10-13);
- [per-repo concurrency](phase-0.md#head-start-per-repo-concurrency-2026-10-13) (FR-D6);
- [retention](phase-0.md#head-start-retention-2026-10-13);
- the [repo-level shadow switch](phase-0.md#head-start-repo-level-shadow-2026-10-13) for pilots;
- [linter dedupe](phase-0.md#head-start-linter-dedupe-2026-10-13) before posting, and [late CI](phase-0.md#head-start-late-ci-linter-dedupe-2026-10-13);
- [Kubernetes manifests and images](phase-0.md#head-start-kubernetes-manifests-and-images-2026-10-14);
- [Grafana boards](phase-0.md#head-start-grafana-boards-2026-10-14) over the SQL views.

**Not built:**
- OTEL-backed panels (latency, rate limits) and alerting.

### Phase 3 — Comment

**Built:**
- the PR lifecycle:
  - [re-review across pushes](phase-0.md#head-start-re-review-across-pushes-2026-10-13), with stable identity and self-resolving threads;
  - [interdiff scoping](phase-0.md#head-start-interdiff-scoping-2026-10-13);
  - [rebase re-anchoring](phase-0.md#head-start-rebase-and-force-push-2026-10-13);
- [human-comment dedupe](phase-0.md#head-start-human-comment-dedupe-2026-10-13) and [late-CI linter dedupe](phase-0.md#head-start-late-ci-linter-dedupe-2026-10-13);
- [validated suggestions](phase-0.md#head-start-validated-suggestions-2026-10-13), [in ten languages](phase-0.md#head-start-fix-validation-in-more-languages-2026-10-14);
- [feedback events](phase-0.md#head-start-feedback-events-2026-10-13), with reviewer agreement and applied suggestions;
- [slash commands](phase-0.md#head-start-feedback-slash-commands-2026-10-14), including owner adjudication;
- [feedback roll-ups](phase-0.md#head-start-feedback-roll-ups-2026-10-14): precedence, acceptance and weighted eFP;
- [auto-demotion](phase-0.md#head-start-auto-demotion-2026-10-14), with [flags and owner-response demotion](phase-0.md#head-start-flags-and-owner-response-demotion-2026-10-14);
- [rule expiry and owner digests](phase-0.md#head-start-rule-expiry-and-owner-digests-2026-10-14);
- [teach-the-bot and routing by reason](phase-0.md#head-start-teach-the-bot-and-routing-by-reason-2026-10-14);
- [signed feedback links](phase-0.md#head-start-signed-feedback-links-and-exemplar-api-2026-10-14) and a first `exemplar-api`;
- [formatter checks and the CLI release](phase-0.md#head-start-formatter-checks-and-the-cli-release-2026-10-14).
- [webhook feedback signals](phase-0.md#head-start-webhook-feedback-signals-2026-10-14): resolution, reply classes, reviewers raising a finding later, ignored at merge;
- [fixed-without-suggestion and reactions](phase-0.md#head-start-fixed-without-suggestion-and-reactions-2026-10-14), completing the §9.1 signals but teach-the-bot;
- [SARIF and alert dismissals](phase-0.md#head-start-sarif-and-alert-dismissals-2026-10-14).

**Not built:**
- the Homebrew tap and internal mirror for the CLI.

### Phase 4 — Shift left and bug lane

**Built:**
- the [bug lane](phase-0.md#phase-4-head-start-the-bug-lane-2026-10-13), in shadow;
- exemplar-search completed: [`find_definition`](phase-0.md#head-start-find_definition-2026-10-13), [`find_references` and `dependents`](phase-0.md#head-start-find_references-and-dependents-2026-10-13), and degraded mode;
- [`path_history`](phase-0.md#head-start-path_history-tool-2026-10-13);
- [bug findings posting on critical paths](phase-0.md#head-start-bug-findings-on-critical-paths-2026-10-14), per class, off by default;
- [mutation evals](phase-0.md#head-start-mutation-evals-2026-10-14) for the bug classes (Go);
- [rule exports](phase-0.md#head-start-rule-exports-2026-10-14) to AGENTS.md, CLAUDE.md, Copilot and Cursor files, with [drift tracking](phase-0.md#head-start-export-drift-2026-10-14).

- [upstream effectiveness](phase-0.md#head-start-upstream-effectiveness-2026-10-14): agent PRs, violation rates, and rewrite flags in the digest;
- [`exemplar review` and `/v1/evaluate`](phase-0.md#head-start-exemplar-review-and-v1evaluate-2026-10-14): pre-PR review of the working tree, with device-flow sign-in.

### Phase 5 — Auto-approve

**Built:**
- [Stage 1](phase-0.md#phase-5-head-start-the-rest-of-stage-1-2026-10-13): path denylist, author policy, fork and size, and the forced bug lane;
- [merge safety score](phase-0.md#head-start-merge_safety_score-2026-10-13);
- Stage 2:
  - [categorizer](phase-0.md#phase-5-head-start-stage-2-categorizer-and-its-calibration-2026-10-13);
  - [CODEOWNERS and branch rules](phase-0.md#phase-5-head-start-stage-2-codeowners-and-branch-rule-lookups-2026-10-13);
  - [combination](phase-0.md#phase-5-head-start-stage-2-combination-2026-10-13);
  - [wiring into review](phase-0.md#phase-5-head-start-stage-2-in-review-2026-10-13);
- approval and revocation in the worker;
- the [`merge_policy_check` status](phase-0.md#head-start-the-exemplarmerge-policy-check-2026-10-14);
- [audit and counter-metric boards with alerts](phase-0.md#head-start-counter-metrics-and-alerts-2026-10-14);
- compliance constants and kill switches.

**Not built:**
- the counter-metrics on unviewed owned paths and on human comment volume;
- the red-team exercise (an activity, not code).

Auto-approve stays off in config.

### Phase 6 — Platform

**Built:**
- the [web UI's read-only pages](phase-0.md#head-start-the-web-ui-2026-10-14): rules, rule and finding detail, candidates, the auto-approve audit and cost, filtered by the viewer's repo access;
- [sign-in sources](phase-0.md#head-start-sign-in-sources-github-oauth-and-oidc-2026-10-14) following Coder's: GitHub OAuth and OIDC, with OIDC groups mapped to repos;
- the engine as a judge for other callers, through [`/v1/evaluate`](phase-0.md#head-start-exemplar-review-and-v1evaluate-2026-10-14) (GitHub users, and services with Auth0 tokens).

- [owner actions](phase-0.md#head-start-web-ui-owner-actions-2026-10-14) from the UI, through a worker job;

- the [playground](phase-0.md#head-start-the-web-ui-playground-2026-10-14), with save-as-draft-PR (which also edits a rule);

**Not built:**
- the transcript viewer;
- opt-in rollout waves (operations, not code);
- the Fixer for human comments;
- editor integration.

## Open questions

These are decisions for the team rather than the code:
- The decisions recorded in each head-start section, listed there as "Decisions" or noted inline, are open to revision.
