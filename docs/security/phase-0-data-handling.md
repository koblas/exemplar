# Phase 0 data handling: for Security sign-off

**Purpose.** This document supports the Phase 0 exit criterion "eligibility list and retention signed off by Security" (spec §16, W9 in [the Phase 0 plan](../plans/phase-0.md)). It states what the Phase 0 code does with repository data, where each control lives, and what Security needs to decide.

## Decisions needed from Security

| # | Decision | Where it goes | Current state |
|---|---|---|---|
| 1 | Repos that must never be reviewed, mined, indexed or sent to a model (FR-H1) | `exemplar-config/platform/eligibility.yaml` | Empty denylist placeholder |
| 2 | Retention periods (FR-H7) | `exemplar-config/platform/retention.yaml` | Spec §13.1 defaults, unconfirmed |
| 3 | Data terms for each registered provider: the Claude API and Amazon Bedrock (zero retention, no training; spec §17 #4) | `internal/llm/providers.go` registry | Both are registered in code; Security approval of their terms is pending. A provider from a second model family is still needed for cross-family verification |
| 4 | Repos where transcripts may not be stored (spec §17 #10) | Transcript policy (Phase 2) | Not decided; Phase 0 stores no transcripts |

Both platform files are owned by `@security/appsec` in the config repo's CODEOWNERS. Nothing in a target repo can override them.

## What leaves the system, and to whom

Only `exemplar-gateway` calls a model provider. It holds the provider key; workers never do.

| Control | Implementation | Test |
|---|---|---|
| Provider allowlist | The provider registry in `internal/llm/providers.go` (`claude`, `bedrock`). Config can only choose registered providers; the gateway refuses to start on any other provider, on a model id the provider cannot serve, or on a missing credential. Bedrock uses AWS credentials (workload identity or a Bedrock API key), held only by the gateway. | `TestUnapprovedProvidersRefused`, `TestRoutesValidateModelsPerProvider`, `TestBedrockProviderSigV4` |
| Eligibility before any model call | `config.Snapshot.CheckEligible` checks the global kill switch, the denylist (which wins over opt-in), per-repo opt-in, and the repo kill switch. `engine.Prepare` stops ineligible PRs before any model call. | `TestCheckEligible` |
| Path exclusions (FR-H2) | Removed, binary, over 1 MB, lockfiles, minified files, `linguist-generated`/`linguist-vendored`, and `.exemplarignore` (read from the base branch). Excluded files are never shown to a model, and `get_hunk` refuses them. | `TestPrepareExclusionsAndSecrets`, `TestToolSandbox` |
| Secret redaction, layer 1 (FR-H3) | The worker redacts the diff, the PR title and body, and every tool result (file reads, grep) using the gitleaks default rules. Each match also produces a deterministic `possible-secret-in-diff` finding (blocking, Security). An input that cannot be redacted makes the run incomplete, and nothing is sent. | `TestRedact*`, `TestReviewEndToEnd` |
| Secret redaction, layer 2 | The gateway re-scans every JSON string in every request and refuses (HTTP 422) any request with a match. The secret is never echoed or logged; gitleaks' own logging is silenced. | `TestGatewayRefusesSecrets` |
| Data-class logging (FR-H4) | Every call must declare `X-Exemplar-Data-Classes`, from {diff, file_context, search_context, ticket, review_comments, pr_text}. The gateway writes provider, model, stage, repo, caller, data classes, tokens and cost to `model_calls`. | `TestGatewayRoutesTierAndRecords` |
| Code scanning (spec §6.12) | Security-category findings (path, lines, rule id, explanation) are also uploaded to the PR repo's own GitHub code scanning, with the poster token (`security_events: write`). No code or secret is in the upload, and nothing leaves GitHub. | `TestSARIFAndDismissal` |
| Dedupe judges (spec §6.10) | The light model is asked whether a finding repeats a linter warning or a reviewer's existing review comment. Reviewer comments are declared as `review_comments`. Both kinds of input are redacted like the diff before sending, and an input that can't be redacted is not judged. Only comments by the repo's owners, members and collaborators, other than the PR author, are sent. | `TestHumanJudgeRedacts`, `TestReviewHumanDedupe`, `TestReviewLinterDedupe` |
| No credentials in model processes | Model-facing code runs in the worker. It gets read-only installation tokens (`contents:read`, `pull_requests:read`) for the PR's repo only. Only the poster gets write scopes, and the config writer only for `exemplar-config`. | `TestTokenFlavors`, `TestTokenScopeRefusals` |
| Model tools | Read-only, confined to the checkout: no `..`, absolute paths, `.git` or escaping symlinks. No shell, network or writes. Output is capped and redacted. | `TestToolSandbox` |

## What is stored, and for how long

Phase 0 creates these tables (spec §4.3 subset): `gh_events`, `rules`, `reviews`, `rule_runs`, `findings`, `approval_decisions`, `eval_cases`, `model_calls`. Phase 0 stores **no model transcripts**: `rule_runs.transcript_key` is unused.

| Data | Contains | Proposed retention (spec §13.1 default) |
|---|---|---|
| `approval_decisions` | Gate traces (code-measured numbers only) | 7 years |
| `findings`, `feedback_events` | File path, line range, model explanation, suggested fix | 2 years |
| `model_calls` | Metadata only: no prompts, no code | 90 days |
| Transcripts | Not stored in Phase 0 | 90 days, once added |
| `gh_events.payload` | Webhook bodies (PR bodies, comment text) | 30 days; the row stays for idempotency |

`exemplar-webhook` now stores webhook payloads in `gh_events`. A daily retention job in `exemplar-worker` applies `platform/retention.yaml`, and runs once at startup too:
- **`gh_events.payload`:** nulled after its period. The row stays as the delivery's idempotency key.
- **`model_calls`:** deleted after its period.
- **`findings`:** deleted after its period, with the calibration labels (`verifier_labels`) that point at them.
- **`approval_decisions`:** deleted by their review's age.

The job works in batches. A period below 1 is refused rather than read as "delete everything". Transcripts are still not stored, and `feedback_events` does not exist yet, so those two periods have nothing to apply to.

## Known gaps in Phase 0

- `.gitleaks.toml` allowlists are not read yet; only `.gitleaksignore` is honoured. An allowlisted match is still redacted but produces no finding.
- `.gitattributes` and `.exemplarignore` are read from the repository root only; nested files are ignored.
- The per-repo provider restriction for compliance repos depends on the second provider and arrives with Stage 2 (Phase 5). Until then every repo can reach every provider that global.yaml routes to.
- Fork PRs are not reviewed in Phase 0 (the replay harness uses recorded PRs only).
