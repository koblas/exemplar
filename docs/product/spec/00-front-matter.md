<!--
The canonical specification is docs/product/spec/, one file per top-level
section (docs/product/README.md maps § numbers to files); edit it here.
It was split on 2026-11-24 from docs/spec/technical-spec.md, imported
on 2026-10-13 from the claude.ai artifact "Exemplar — Technical
Specification" (https://claude.ai/artifact/CoxFmpRouQvgeAXNY8C8jt, version
1791800349-fc89), which is no longer maintained. Section numbers (§) in
code comments, commits and docs/plans refer to these files. Bracketed ids such as [FR-D6] refer to
the PRD ("Exemplar — Self-Correcting AI Code Review" v0.10), in
docs/product/prd/; docs/plans/README.md maps each id family to its
sections in both.
-->

# Exemplar — Technical Specification

**Status:** Draft 1.4 · October 2026 **Implements:** PRD "Exemplar — Self-Correcting AI Code Review" v0.10. Requirement IDs in brackets (e.g. [FR-V1]) refer to the PRD. **Audience:** the engineers building and operating Exemplar, security, and platform leadership.

### What changed in Draft 1.4 (review against PRD v0.9 and v0.10; hosting design of 2026-10-24)

- **PRD v0.7:** verifier quorum for the bug lane (§6.8); round cap and class-of-bug grouping across pushes (§6.2, §6.10); the clean-PR false-alarm set as a change gate beside the incident set (§8.8, §8.9); rounds, human approvals and time waiting on a person as informational metrics (§12.5).
- **PRD v0.8:** author experience and blast radius as Stage 1 gates, with their config and gate-trace fields (§6.13); an auto-approved failure is recorded against the gate trace, never the author (§6.13, §12.5).
- **PRD v0.9:** a clean run is no `blocking`, `important` or Security candidate, so `suggestion` findings post beside an approval (§1.2 rule 6, §6.1, §6.13, §16.1); `same_family` stops gating approval once the verifier calibration has passed for the configured providers, recorded in `calibration_results` (§6.8, §6.13); the approval is bound to the base SHA (§6.13); rules per agent and overflow by expected value (§6.3, §6.5); learned importance priors (§8.11, §9.3); owner-written seeds and merged-change clusters as co-equal sources (§8.1, §8.5, §8.7); `review_by_defaults` per category (§8.10); the goal metric is severity-weighted acceptance plus incident-set recall, and the shadow exit is an adjudicated sample plus injected recall (§12.6, §16).
- **PRD v0.10:** search is optional in constraint 6 and FR-E12, with the install ladder, which is what §6.4.6 already specifies; no section changes.
- **Hosting:** a new §2.4 carries the deployment design: the hosting requirements the code imposes, the eleven changes that make one install elastic (control/runner split, single-use token claims, config snapshots, queue-driven autoscaling), the platform recommendation (Kubernetes on GKE Autopilot, Cloudflare at the edge), the trial paths, and the cost picture. §13 gains the runner boundary; §14, §15, §16 and §17 follow.
- **Search is optional:** §6.4.6 specifies running without `exemplar-search` as an install mode rather than a degraded state, with `find_references` and `path_history` answered from the review's own checkout on single-repository installs, and what the check run says. The install ladder is shadow without search, posting with search, auto-approve.
- **Testing:** §16.1 adds metamorphic relations over replay, a seeded simulation of the worker's state machines that carries the chaos and base-drift checks, a configuration matrix, structural guardrails and hot-path benchmarks, and says which change needs which technique.
- **As built:** §2.2 records that the orchestrator, poster and feedback roles run as River jobs inside `exemplar-worker` with distinct token flavors, that the worker's checkout is a blob-less depth-1 clone (§6.4.2), and that the default way in is a Cloudflare Tunnel (§13). The services table is the binaries that exist.

### What changed in Draft 1.3

- **Network boundary diagram and edge table** added at the top of §13: every inbound channel and every outbound dependency, with protocol, authentication, data crossing and NetworkPolicy enforcement.
- **MCP server moved out of the initial scope** to Appendix C (deferred). Coding agents use exported instruction files and the CLI for pre-PR checks (§10.3); Phase 4 no longer includes MCP; the stack, layout, security table, dashboards and config lose their MCP entries, with the `mcp` caller value kept reserved.

### What changed in Draft 1.2 (review against PRD v0.6)

- **Coverage is computed by the harness from tool calls, never from model output** (§6.5); `files_examined` removed from the model-facing schema.
- **Auto-approve requires zero *candidate* findings**, before verification, from every lane (§6.13); the same-family check compares the actual families used on both sides of each finding (§6.8).
- Authorization added for `/exemplar try`, slash commands, feedback links and code-scanning dismissals (§6.12, §8.7, §9.1).
- Exports: `export:` blocks need platform approval; the exports app uses native auto-merge, not a ruleset bypass (§10.2).
- Fingerprints carry an occurrence index; findings are unique per review, not per PR (§4.3, §6.2); re-run attempts counted on `reviews`.
- Monorepo concurrency, snapshot-pinned `exemplar-search`, incremental indexing, per-rule Lane 1 timeouts, clamped-range fix handling, GraphQL for sweeper and reaction polls (§5, §6.4, §6.6, §6.9, §6.15).
- Feedback: 👎 is a secondary signal with weight 0.5 and no eFP role; event precedence defined; demotion uses unweighted counts for the Wilson bound; roles derived from PR participation (§9).
- Learning: community detection instead of connected components; doc- and incident-link boosts restored in ranking (§8.4, §8.5).
- Verifier and Stage 2 calibration gate CI (§6.8, §8.9); worker egress allowlist corrected (§13); `gh_events` retention; secrets allowlist; terminology aligned (§13.1, §15).

---
