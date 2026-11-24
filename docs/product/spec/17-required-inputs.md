## 17. Required inputs

These inputs configure the system and are needed before the end of Phase 0.

| # | Input | Configures |
|---|---|---|
| 1 | GitHub plan (Enterprise Cloud or not) | Rate-limit budgets (§5) |
| 2 | Result of acceptance test T-0.3 per repo ruleset | Approval mechanism per repo (§6.13) |
| 3 | Whether any public/fork PRs are in scope | Fork handling, visibility tiers |
| 4 | Second model provider and its data-processing terms | Gateway provider B, verifier, Stage 2 cross-check |
| 5 | Model access path (direct API or a cloud marketplace) | Gateway endpoints |
| 6 | Compute platform (recommended: GKE Autopilot; see §2.4.3 for when to choose otherwise) and the answers to the §2.4.3 verification list | The production overlay: compute class for search, ephemeral volumes, FQDN policies, runner quota |
| 7 | Rule ownership map (platform-owned global rules, team-owned `.exemplar/` rules) | CODEOWNERS of `exemplar-config` and target repos |
| 8 | Compliance repo list | `internal/gates/compliance.go` |
| 9 | Grafana instance and Postgres replica access | §12 |
| 10 | Repos where transcripts may not be stored | Transcript retention policy |
| 11 | Monorepo size, file count, main languages, include/module roots | `exemplar-search` sizing; import patterns; `context` config |
| 12 | Feedback-link domain and SSO integration | `exemplar-api` |
| 13 | Coding-agent tools in use | Export targets (§10.2); whether any agent in use cannot invoke the CLI, which would trigger Appendix C |
| 14 | Incident source (PagerDuty, incident.io, postmortem repo) | `path_events` ingestion |
| 15 | Embedding provider approval (default: Voyage AI via the gateway) | §8.3 |
| 16 | Eligibility denylist of repos never sent to a model provider (Security) | `platform/eligibility.yaml` (§13.1) |
| 17 | Retention periods for gate traces, findings/feedback and model logs (Security) | `platform/retention.yaml` (§13.1) |
| 18 | Bot/agent identities that may ever be auto-approved, and their scopes | `auto_approve.bot_authors` (§6.13) |
| 19 | Confirmation of the v1 language list against the monorepo | §6.4.5 |
| 20 | Whether GitHub Enterprise Server is in scope alongside GitHub.com | GitHub feature matrix (SARIF, rulesets, suggestions) |
| 21 | `merge_safety_score` weights and threshold owner | `merge_safety.weights` (§6.13) |
| 22 | Whether a hosted trial is offered at all, and on what data-handling terms | §2.4.4; tenancy, retention and shadow enforcement for trial tenants |
| 23 | Security's answer on running compile and the playground in Cloudflare Sandboxes (repository files leaving the cluster, R6) | §2.4 C10; the rule-sandbox choice |
| 24 | Whether search is run from day one, or added at the posting step of the install ladder | §6.4.6; the overlay, storage class and size |
| 25 | The engineer(s) who confirm clean PRs, and the first set | `evals/_clean/` (§8.8) |

---
