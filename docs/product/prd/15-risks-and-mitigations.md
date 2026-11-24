<a id="15-risks-and-mitigations"></a>

## 15. Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Prompt injection leads to a false approval | No write tools for models; deterministic Stage 1; verification by a second model; provenance in code; hardcoded compliance |
| Noisy rules erode trust | Verification, comment budget, effective-FP bar, shadow mode, auto-demotion |
| Generator and verifier share blind spots | Different model families per stage (§8.2); verifier calibration set |
| Bug lane drifts into opinion | Allowlisted bug classes only; separate per-class kill switch and metrics |
| Rules go stale after migrations | `review_by` expiry; re-validation on recent PRs; owner digests |
| Exports conflict with teams' hand-written agent instructions | Generated sections delimited and owned by platform; teams keep their own sections |
| Bot learns to please rather than to be right | Human gate on promotion; acceptance is a metric, not an objective the system optimizes directly |
| Erosion of knowledge transfer and ownership | Counter-metrics (§13.1); owner visibility of auto-approved changes |
| No external baseline means self-graded success | In-house baseline reviewer; replay against human comments; injected-violation evals |
| Repository context is imprecise in the monorepo (name-based, no type resolution) | Scope references by imports; treat results as candidates the verifier must confirm with resolved evidence; benchmark index size, freshness and ctags coverage in Phase 0 |
| Open-source engine licenses (e.g. Semgrep, CodeQL terms) | Legal review before Phase 1; ast-grep as a permissively licensed fallback for Lane 1 |
| Authors game feedback to demote rules they dislike | Role-weighted dismissals (FR-B7); author feedback flags but never demotes alone; owner adjudication |
| Sensitive code or secrets sent to a model provider | Repo denylist and path exclusions (FR-H1–H2); pre-call secret redaction (FR-H3); per-call data logging and hardcoded provider list (FR-H4); fork isolation (FR-H5) |
| Partial review mistaken for a clean review | Explicit coverage (FR-D4); fixed degradation order (FR-D1); degraded runs cannot approve (FR-D5); incomplete runs post nothing (FR-D3) |
| Auto-approve reaches control files through a broad scope | Hardcoded path denylist (FR-A8); bot-author policy (FR-A6); bug lane forced on eligible PRs (FR-A7); approval dismissed on push (FR-A9) |
| Repeat pushes make the bot repetitive or stale | Incremental review and stable finding identity (FR-C1–C2); dismissed stays dismissed (FR-C3); self-resolving threads on rebase (FR-C4) |
| Exports drift from config across hundreds of repos | Weekly cadence, auto-merge for generated-only PRs, drift dashboard (FR-S6) |
