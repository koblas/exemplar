<!--
The canonical PRD is docs/product/prd/, one file per top-level section
(docs/product/README.md maps section numbers to files); edit it here. It
was split on 2026-11-24 from docs/spec/prd.md, imported on 2026-10-13 from the claude.ai artifact "PRD: Exemplar — Self-Correcting
AI Code Review (v0.6)" (https://claude.ai/artifact/EZgtgLKk7v47FJatTTdcyp,
version 1791798313-8eef), which is no longer maintained, and converted
from its rendered HTML to Markdown with the text unchanged. The §17
mockups are rendered as quoted Markdown (diffs as diff blocks, without
their line numbers), and heading anchors keep the page's ids. Requirement
ids such as FR-D6 in code, commits and docs refer to these files;
docs/plans/README.md maps each family to its PRD and spec sections. The
PRD's "Companion: Implementation Plan & Architecture" is
docs/product/spec/.
-->

<a id="prd-exemplar-self-correcting-ai-code-review"></a>

# PRD: Exemplar — Self-Correcting AI Code Review

**Status:** Draft v0.12 · November 2026 · **Build-only** (no third-party AI reviewer will be purchased) **Source:** Redpoint Alchemy talk "AI broke code review" (Andrew Swerdlow, Roblox, Sep 10 2026) and session notes, extended with patterns borrowed from mature industry systems (Appendix B) **Working name:** Exemplar (placeholder) **Companion:** Implementation Plan & Architecture (Go services, Postgres, GitHub App)

Contents

- [PRD: Exemplar — Self-Correcting AI Code Review](#prd-exemplar-self-correcting-ai-code-review)
  - [1. Summary](01-summary.md#1-summary)
  - [2. Problem Statement](02-problem-statement.md#2-problem-statement)
  - [3. Goals and Non-Goals](03-goals-and-non-goals.md#3-goals-and-non-goals)
  - [4. Users and Personas](04-users-and-personas.md#4-users-and-personas)
    - [4.1 From install to day-to-day: the adoption journey](04-users-and-personas.md#41-from-install-to-day-to-day-the-adoption-journey)
  - [5. System Overview](05-system-overview.md#5-system-overview)
  - [6. Functional Requirements — Learning Plane (offline)](06-functional-requirements-learning-plane-offline.md#6-functional-requirements-learning-plane-offline)
  - [7. Data Model — Rules](07-data-model-rules.md#7-data-model-rules)
  - [8. Functional Requirements — Enforcement Plane (online)](08-functional-requirements-enforcement-plane-online.md#8-functional-requirements-enforcement-plane-online)
  - [9. Feedback — structured, Tricorder-style — revised](09-feedback-structured-tricorder-style-revised.md#9-feedback-structured-tricorder-style-revised)
  - [10. Authoring-time delivery (shift left) — new](10-authoring-time-delivery-shift-left-new.md#10-authoring-time-delivery-shift-left-new)
  - [11. Evaluation — revised](11-evaluation-revised.md#11-evaluation-revised)
  - [12. Auto-Approve Gates](12-auto-approve-gates.md#12-auto-approve-gates)
  - [13. Success Metrics](13-success-metrics.md#13-success-metrics)
  - [14. Rollout Plan](14-rollout-plan.md#14-rollout-plan)
    - [14.1 Phased deliverables](14-rollout-plan.md#141-phased-deliverables)
  - [15. Risks and Mitigations](15-risks-and-mitigations.md#15-risks-and-mitigations)
  - [16. Open Questions](16-open-questions.md#16-open-questions)
  - [17. Sample interactions](17-sample-interactions.md#17-sample-interactions)
  - [Appendix A — Design Principles](appendix-a-design-principles.md#appendix-a-design-principles)
  - [Appendix B — Prior art borrowed, and what we deliberately did not take](appendix-b-prior-art-borrowed-and-what-we.md#appendix-b-prior-art-borrowed-and-what-we-deliberately-did-not-take)

<a id="what-changed-in-v02v04"></a>

### What changed in v0.2–v0.11

- **v0.12 (rule layering and eval independence):** rules arrive in layers: presets shipped with the binary, the config repository, and the target repository. The config repository names presets with `extends` and adjusts their rules with `disabled_rules` and `rule_settings`, so an installation references the starter rules instead of copying them; a preset rule goes active only when the organization accepts the release that brought it, and Security and `blocking` preset rules are `required` unless the organization relaxes them (FR-E19). A project's `.exemplar/config.yaml`, read from the base branch, may extend presets, disable rules the organization has not marked `required`, and narrow a rule's paths (FR-E20). Exemplar's own defence evals (adversarial, verifier, categorizer) ship with the binary and run against the shipped rules, and synthetic eval cases run under a built-in profile instead of a borrowed enrolled repository, so config CI passes with no repository enrolled and a false-approval case can actually fail (FR-X10, FR-X13). The starter config enrols nothing. FR-E17 and open question 3 are amended to the four layers; question 19 is new.
- **v0.11 (the adoption journey):** §4.1 describes how an installation grows from a trial on one repository to many teams sharing one install, and what each stage needs. It surfaces two gaps the stages expose: a rule has no way to apply to a set of repositories short of all of them (FR-E17), and nothing lists what runs on one repository and why (FR-E18). Both are proposed, pending the design decision in §16 question 18; open question 3 (rule ownership) is answered by the model there. No existing requirement changed.
- **v0.10 (search is optional):** the search container is an optional part of an installation, not a prerequisite (constraint 6, FR-E12). Without it, references and history come from the review's own checkout, the check run says that other repositories were not searched, and a review of a repository with no `context.extra_repos` is complete, not degraded, so it may auto-approve. The install ladder is shadow without search, posting with search, then auto-approve. Search remains how a monorepo or a multi-repository org gets cross-repository context.
- **2026-10-25 (§14.1):** the rollout plan is cut into phased deliverables, D0.1 … D6.5, each with a done-when and the requirements it satisfies; issues in beads carry matching `phase:N` labels. No requirement changed.
- **v0.9 (assumptions review):** eight assumptions re-examined against what the review space has learned. Auto-approve no longer requires zero findings of any severity: a clean run is no `blocking`, `important` or Security finding (FR-A13, §12). The goal metric is severity-weighted acceptance plus incident-set recall; raw acceptance is informational (G1, §13, §14). Verifier independence is quorum and context, with a second model family a preference, and `same_family` stops gating approval once the quorum is calibrated (FR-E10). Approval is bound to the base SHA as well as the head (FR-A9). Rules per agent is a configuration evaluated on the golden set, and overflow is ranked by expected value, not precision alone (FR-E3, FR-E16). Incidents, reverts, merged-change clusters and owner-written seeds are co-equal sources with review comments (constraint 1, FR-L1.7 to FR-L1.9; the backlog's cluster-review item is adopted). The shadow exit is an adjudicated sample plus injected recall, not precision against human comments (§14). Path-level importance priors learn from *Not important here* dismissals (FR-B9). `review_by` defaults per category (§7).
- **v0.8 (after Will Larson's "Speeding up code review with `small-change`", October 2026):** two deterministic Stage 1 gates, author experience in the touched paths (FR-A10) and blast radius as a count of ownership areas with shared paths excluded (FR-A11); the accountability norm that an auto-approved failure is a system failure (FR-A12); a caution on domain-heavy code in §12. The post's `review-pr-cluster` idea is in [`docs/plans/backlog.md`](../../plans/backlog.md).
- **v0.7 (after Anthropic's "How the Claude Code team does code review", claude.dev, October 2026):** verifier quorum for the bug lane (FR-V7); convergence controls across pushes: a round cap and class-of-bug findings (FR-C8, FR-C9); a clean-PR false-alarm rate (FR-X12); review rounds, human approvals and time waiting on a person as informational metrics (§13.2); two open questions on unanswered findings and an automatic auto-approve circuit breaker (§16). The further ideas from that post are in [`docs/plans/backlog.md`](../../plans/backlog.md), not here, until one is adopted. Where the post disagrees with this PRD (its reviewer blocks merges on open findings and never approves; Exemplar never blocks and may approve within FR-A2) the PRD stands.
- **v0.6 (gap review):** PR lifecycle (§8.9), degraded operation (§8.10), data handling and repo eligibility (§8.11); importance check extended to compiled rules (FR-V6); auto-approve Stage 1 made strictly deterministic with path denylist, bot-author rules and a "bug lane actually ran" gate (§12); role-weighted feedback and human-comment dedupe (§9, FR-E15); CI-timing rule for linter dedupe (FR-E7); budget exemption for security and must-fix findings (FR-P6); adversarial evals and baseline sampling (§11); export operations and MCP access control (§10); cross-language clustering (FR-L3.1). **Cost and latency moved to informational metrics (§13.2)** — they are tracked and reported but are not goals or exit criteria.
- **v0.5:** repository context is fully self-contained and never builds code: one in-cluster search container embedding the open-source zoekt engine, with import-based scoping instead of a build graph (constraint 6, FR-E12).
- **v0.4:** no external code search tool will be available; repository context is built in-house.
- **v0.3:** added §17, eight sample interactions with mockups of what authors, rule owners, coding agents and approvers see.
- **Build-only constraint** made explicit. Every capability a commercial reviewer would provide must be built; mature systems are treated as design references, not options.
- **Three review lanes** instead of one: exemplar rules (the original design), compiled deterministic rules, and a narrow general-bug lane.
- **Verification stage** that tries to refute every finding and filters "valid but unimportant" ones before anything is posted.
- **Structured feedback** (Tricorder-style dismissal reasons) replaces 👍/👎 as the primary signal.
- **Fixes** delivered as one-click GitHub suggestions where the change is local.
- **Authoring-time delivery**: rules exported to coding-agent instruction files and served over MCP, so AI-written code conforms before it becomes a PR.
- **Comment budgets, rule expiry, deeper context, an in-house baseline reviewer, and counter-metrics** for what auto-approve takes away.

---
