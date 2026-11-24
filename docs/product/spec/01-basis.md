## 1. Basis

### 1.1 What the PRD requires of the system

Based on the PRD's goals (G1–G7), constraints (PRD §2.1) and functional requirements, Exemplar is built as follows.

| PRD requirement | Specification |
|---|---|
| Review every PR on GitHub with org-specific rules; never block [FR-E8] | A GitHub App backed by an in-cluster Go control plane (§2, §5). Reviews post as `COMMENT` ; the verdict type has no `REQUEST_CHANGES` value (§6.12). |
| Learn offline, enforce online, one human gate [PRD §5 invariants] | Learning plane writes candidate rules as draft PRs to `exemplar-config` ; only merged config is enforced (§8). |
| One agent per rule, ≤ 3 concurrent; rules per agent and overflow by expected value [FR-E3, FR-E4, FR-E16] | The engine fans out with a bounded `errgroup`; `concurrency.rules_per_agent` may group same-category rules after a golden-set run; overflow past the fan-out cap is ranked by precision × severity weight × hit rate (§6.3, §6.5). |
| Three lanes: compiled, exemplar, bug [PRD §8.1] | ast-grep/Semgrep runners, exemplar agents, gated bug-class agents (§6.6–6.7). |
| Verification with model diversity and a bug-lane quorum [FR-V1–V7, FR-E10–E11] | Independent verifier in a fresh context, on a second family where one exists; `n`-of-`k` quorum for Lane 3; evidence resolved in code; importance check for Lane 1 Performance/Style/Nit; `same_family` gates approval only until the calibration passes (§6.6, §6.8). |
| Model output is untrusted and checked by code [FR-T1–T6] | Validation in code; provenance attached by code; read-only tools; no write tokens in model-facing processes (§6.9, §13). |
| Self-contained repository context, no builds [constraint 6, FR-E12–E14] | `exemplar-search` : one container embedding zoekt with ctags symbols and the only git mirror; import-based scoping (§6.4). Search is an optional install: without it, references and history come from the review's own checkout (§6.4.6). |
| Comment budget, labels, SARIF, dedupe, class-of-bug grouping [FR-P1–P6, FR-E7, FR-E15, FR-C9] | Ranking and budget with Security/blocking exemptions; one grouped comment when a rule or class matches many places; human-comment and late-CI linter dedupe; Conventional Comments labels; SARIF for Security (§6.10, §6.12). |
| Validated one-click fixes [FR-F1–F3] | Suggestion blocks validated by apply + parse + format checks (§6.11). |
| Structured, Tricorder-style feedback; learned importance priors [FR-B1–B9] | Signed links and slash commands; role-weighted eFP; owner adjudication; human agreement as evidence; automated demotion; path-level importance priors proposed from *Not important here* dismissals (§8.11, §9). |
| Auto-approve behind deterministic gates [FR-A1–A13] | Strictly deterministic Stage 1 over harness-measured coverage and the pre-verification count of `blocking`, `important` and Security candidates, author experience and blast radius from stored history and CODEOWNERS, path denylist and author policy in code, forced bug lane, two-model Stage 2, revocation on push and on base drift; failures attributed to the system (§6.13). |
| PR lifecycle, convergence across pushes [FR-C1–C9] | Incremental re-review, stable finding identity, self-resolving threads, dismissed-stays-dismissed, rebase re-anchoring, stacked-PR bases, a round cap with one summary comment (§6.2). |
| Degraded operation [FR-D1–D6] | Fixed shedding order, fan-out cap, provider failover, explicit coverage, incomplete runs post nothing; queue-driven capacity (§6.15, §2.4). |
| Data handling and eligibility [FR-H1–H8] | Platform denylist, path exclusions, pre-call secret redaction, per-call data logging, pseudonymized mining, Security-owned retention (§13.1). |
| Shift left [FR-S1–S7] | Rule exports (weekly and on promotion/kill, auto-merge for generated-only PRs, drift tracking) via a separate least-privilege GitHub App; pre-PR checks through the CLI (§10.1, §10.3). MCP server deferred (Appendix C). |
| Learning from every source [FR-L1–L6] | Review comments, incidents and reverts, owner-written seeds and merged-change clusters enter one pipeline behind one human gate (§8). |
| Evaluation before rollout [FR-X1–X12] | Eval CI, replay harness, injected and mutation cases, adversarial set on every change, verifier calibration, sampled baseline reviewer, incident set and clean-PR set as the change gate (§8.8–8.9). |
| Cost and latency visible, not optimized for [G5, PRD §13.2] | Recorded per call, run and PR; shown as informational; soft per-run ceiling triggers degradation (§6.14). |
| Per-rule visibility and audit | Grafana on a Postgres replica backed by SQL views; Go web UI in Phase 6 (§12). |
| Deployable from a laptop trial to many teams on one install [PRD personas, R11–R12 in §2.4] | One set of manifests; capacity follows the queue with a job per review; the platform choice and its reasons in §2.4. |

### 1.2 System-wide rules

These hold for every component and are tested in CI where possible.

1. **Config from base.** Rules and repo config are read from the default branch of `exemplar-config` and of the target repo, never from a PR head.
2. **Learning never expands enforcement without a merged PR.** Automation may only *reduce* behavior on its own (demotion, expiry to shadow).
3. **Models never hold write access.** Only `exemplar-poster` and the config writer hold write tokens, and neither sees model input.
4. **Provenance is attached by code.** Rule ids, citations, lanes and severities never come from model output.
5. **Zero coverage is a failure,** never a clean result. Coverage is measured by the harness from the files an agent actually opened, never from anything the model says. "No findings" is claimed only at 100% coverage; degraded runs cannot approve and incomplete runs post nothing.
6. **Verification filters what humans see; it never widens what the bot may approve.** Auto-approve looks at candidate findings before the verifier, so a refuted `blocking`, `important` or Security finding still blocks approval. A `suggestion` candidate does not block [FR-A13]; severity comes from the rule, never from the model (rule 4), so the model cannot downgrade a finding into the non-blocking class.
7. **Nothing reaches a model provider before eligibility, exclusion and secret-redaction checks pass.**
8. **No builds.** No component runs a build tool or depends on build artifacts.
9. **Everything is idempotent on a natural key** so any job can be retried (§7).

---
