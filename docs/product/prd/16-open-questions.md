<a id="16-open-questions"></a>

## 16. Open Questions

1. Human acceptance baseline: the slide shows ~55%, session notes say ~40%. Adopt the §9 definitions and re-measure.
2. Clustering thresholds 0.65 vs 0.35 — validate empirically.
3. Rule ownership: platform-owned global rules plus team-owned repo rules (recommended), or one central corpus? *(v0.11: answered in §4.1 and FR-E17: one config repository with three scopes — global, selected by `applies_to`, and repository-local in the target repository — each owned through CODEOWNERS. v0.12: presets shipped with the binary are a fourth layer beneath them, and the target repository's `.exemplar/config.yaml` may opt out of rules the organization has not marked `required` (§7, FR-E19, FR-E20).)*
4. What must be true to merge without a human reviewer, and is security the gate on agent autonomy?
5. Definition of `merge_safety_score` (now a deterministic Stage 2 input, §12): which history signals, what weights, and who owns the threshold. `review_confidence` is the two-model agreement score from Stage 2 and needs a calibration set like FR-X9.
6. Which second model provider is approved for the verifier (§8.2)?
7. Which bug classes enter the bug lane first, and on which paths?
8. Which coding-agent tools are in use across the org, so exports target the right instruction formats?
9. Semgrep vs. ast-grep vs. CodeQL for Lane 1, subject to license review.
10. Monorepo size, main languages and include/module roots, to size the search container and write import patterns for the most important languages first.
11. Which repos go on the eligibility denylist (FR-H1), and what retention periods Security wants for traces, findings and model logs (FR-H7 defaults are placeholders).
12. Which bot and agent authors, if any, should ever be auto-approvable and within what scope (FR-A6).
13. Languages in scope for v1 Lane 1 and the symbol index (FR-H8).
14. G4's "other agent" callers (branch validation, test generation) have no requirements until Phase 6 — should one be specified now to shape the API, or deferred?
15. Whether GitHub Enterprise Server is in scope alongside GitHub.com; several features (SARIF, rulesets, suggestion blocks) differ by version.
16. *(v0.7)* Should every finding have to be answered? Anthropic requires each finding to be fixed or resolved with a reason, and blocks the merge until then; that is what makes their noise measurement trustworthy. Exemplar never blocks (§3.2) and counts an untouched finding as *ignored* (§13). The middle ground is to make "unanswered at merge" a first-class feedback state surfaced in the owner digest and the check-run summary, without gating. Decide whether that is enough signal, or whether a repo may opt into requiring an answer.
17. *(v0.7)* An automatic circuit breaker for auto-approve: when a §13.1 counter-metric alarms (revert rate of auto-approved PRs, incidents on PRs the bot was silent on), should auto-approve suspend itself for that repo until an owner re-enables it, rather than waiting for the YAML kill switch (FR-A3)? Anthropic lists this as wanted but not built. The alarms exist; the question is whether the system may act on them alone.

18. *(v0.11)* Rule selection (FR-E17): is `applies_to` with declared tags the right shape, and in what order do tag sources arrive — declared in the repository file first, GitHub topics mirrored as `topic:*` next, detection from the code (`lang:*`, `has:*`) only if a fleet needs it? *v0.12:* and do `org:*` presets a project extends (FR-E20) make tags redundant, or do both stay? The platform owner decides; the data model's rule scope gains a third value.

19. *(v0.12)* Rule settings beyond narrowing (FR-E19): should the config repository be able to set `applies_to` on a preset rule (the month-1 team that finds a starter rule wrong for its services, without disabling it everywhere), or change a preset rule's severity? Today `rule_settings` only narrows (`status: shadow`, `exclude_paths`, `required`), since a severity or description change would invalidate the rule's shipped eval results.
