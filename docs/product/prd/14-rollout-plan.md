<a id="14-rollout-plan"></a>

## 14. Rollout Plan

| Phase | Scope | Exit criteria |
|---|---|---|
| **0 — Foundations** | GitHub App, gateway (two providers, failover), tracing, config repo + schema + CI, engine with scope and validation, replay harness, in-house baseline reviewer, adversarial eval set (FR-X10), data-handling review and repo eligibility list with Security (§8.11) | Engine and baseline run end-to-end on the golden set; adversarial set in CI; eligibility and retention signed off |
| **1 — Mine + curate** | Learning plane on pilot repos, compile-to-deterministic attempts, playground, owner workflow, injected-violation evals | ≥ 30 active rules (any kind) with passing evals |
| **2 — Shadow** | Lanes 1–2 + verification in shadow on pilot repos; comment budget; volume estimates; PR lifecycle (§8.9) and degraded-mode behavior (§8.10) exercised in shadow; acceptance baselines re-measured (§13, §16) | *(v0.9)* Precision ≥ 0.7 on an **adjudicated sample** (owners label a random 200 shadow findings as right / wrong / unimportant; human comments are not the ground truth, since a correct finding no reviewer made would count against the bot); injected-violation recall (FR-X6) ≥ 0.7 per active rule; beats baseline |
| **3 — Comment** | Posting with Conventional Comments labels, suggestions, structured feedback, digests, auto-demotion, CLI, SARIF for security | ≥ 50% severity-weighted acceptance for 4 weeks; every active rule ≤ 10% effective FP; incident-set recall measured and rising |
| **4 — Shift left + bug lane** | Rule exports, MCP server, bug lane in shadow → comment on critical paths | Exported-rule violations falling; bug lane ≤ 10% effective FP per class |
| **5 — Auto-approve** | Gates on opted-in repos; path denylist and author policy (FR-A6–A13); red-team exercise extending FR-X10; counter-metric dashboards | Zero red-team approvals; zero approvals from degraded runs in shadow; no counter-metric alarms over 4 weeks; ≥ 65% severity-weighted acceptance and ≥ 50% incident-set recall |
| **6 — Platform** | Engine as judge for other agents; Fixer for human comments; editor integration; web UI | Second internal caller in production |

(Auto-approve moves after shift-left and the bug lane relative to v0.1: approving on "zero findings" is only safe once the lanes that would find problems are proven.)

<a id="141-phased-deliverables"></a>

### 14.1 Phased deliverables

§14 names what each phase contains and when it exits. The exit criteria are mostly measured over calendar weeks of production data (four weeks of acceptance, an adjudicated sample of 200 findings), so a phase is not a deliverable. This section cuts each phase into increments that can be built, demonstrated and handed off one at a time, each tied to the requirements it satisfies. Two consequences follow. Build work runs about one phase ahead of phase exits: the Phase 3 increments are built while Phase 2's adjudicated sample accumulates. And D0.1 with D1.1 gives an end-to-end review with real rules in the first weeks rather than at the end of Phase 1.

Status does not live here. `docs/plans/README.md` says what is built; the GitHub issues carry `phase:0` … `phase:6` labels that tie each one to the phase below.

**Phase 0 — Foundations.** Critical path D0.1 → D0.3 → D0.4; the rest parallelize.

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D0.1 | Walking skeleton: the GitHub App receives PR webhooks, checks out the commit, runs one hardcoded compiled rule with `file_patterns` scoping and deterministic validation, and writes a check run only | A real PR on a pilot repo gets a check run with an anchored finding | FR-E2, FR-T1–T6 |
| D0.2 | Model gateway: two providers, failover, per-call data-class logging, tracing | A stage fails over to the second provider in a test and every call is logged with provider, model, stage and data classes | FR-E10, FR-D3, FR-H4 |
| D0.3 | Config repo: rule schema, layering, 15-minute cache with push invalidation, kill switch, CI that rejects a rule without eval cases | A kill-switch edit takes effect within the TTL; CI blocks a rule with no `eval_cases` | §7, FR-A3, FR-L6.4 |
| D0.4 | Replay harness and golden set | The skeleton runs over recorded PRs offline and diffs its findings against the human comments | FR-X3, FR-X7 |
| D0.5 | In-house baseline reviewer, running in the replay harness | Baseline findings are stored for the same PRs the engine reviewed | FR-X8 |
| D0.6 | Adversarial eval set in CI | Every case in the set fails to reach its goal and any success fails the build; the positive controls reach approval; the set passes on a config with no repository enrolled | FR-X10, FR-X13 |
| D0.7 | Data handling with Security: repo denylist, path exclusions, secret redaction, retention configuration | Security signs off the denylist and the retention periods. A sign-off, not code; it starts in week one | FR-H1–H3, FR-H7 |
| D0.8 | Search container spike: zoekt and ctags on the monorepo, with index size, freshness, p95 query latency and symbol coverage measured | The benchmark decides how much of FR-E12 Phase 2 can rely on | FR-E12, §15 |
| D0.9 | Installable release: images, manifests, setup flow and the first tagged release an operator can install without the authors | A fresh cluster runs the review path from the release manifests alone | §15 (operations) |

**Phase 1 — Mine and curate.**

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D1.1 | Owner-written seeds and the config-PR promotion path. Pulled first so a handful of real rules exist before the mining pipeline works, which unblocks Phase 2 early | An owner writes a rule in the playground or the config repo and it reaches `active` through a merged config PR | FR-L1.8, FR-L6.2 |
| D1.2 | Comment ingest and backfill with pseudonymized identity | Pilot repos' review history is queryable at the FR-L1.5 scale | FR-L1.1–L1.5, FR-H6 |
| D1.3 | Extract and embed, idempotent by content hash | A re-run over the same comments makes no model calls and no duplicate candidates; the extractor accepts 10–15% of comments | FR-L2 |
| D1.4 | Cluster, merge, rank and route | A ranked candidate list with citations to every contributing comment lands in front of the owners CODEOWNERS names | FR-L3, FR-L4.1–L4.2 |
| D1.5 | Playground and owner workflow: edit, test, promote, kill with a reason | A kill is recorded with its reason and a promotion opens a config PR | FR-L6.1, FR-L6.3 |
| D1.6 | Eval cases and injected-violation evals in CI | Every active rule has positive, negative and injected cases and CI reports precision, recall and F1 per rule | FR-X1, FR-X2, FR-X6 |
| D1.7 | Compile-to-deterministic. A parallel track; the exemplar lane works without it | A candidate meeting the FR-L5.2 bar is proposed as a compiled rule | FR-L5 |
| D1.8 | Incident and revert mining, with suppression of candidates a linter already covers. Can slip into Phase 2 without blocking it | An incident-linked diff yields a candidate labelled `eval: synthetic` | FR-L1.7, FR-L4.3, FR-X11 |
| D1.9 | Rule layering: shipped presets, `extends` and `rule_settings` in the config repository, config-repository presets, then pinned repository presets, then the project's `.exemplar/config.yaml`; the starter config extends `exemplar:recommended` and enrols nothing | A fresh copy of the starter passes config CI with no repository enrolled; a project opts out of a rule in its own repository and cannot opt out of a `required` one | FR-E19, FR-E20 |

**Phase 2 — Shadow.** Built while Phase 1's rule count climbs toward 30; nothing posts to authors.

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D2.1 | Exemplar lane: one agent per rule, at most three concurrent, a read-only harness, results to the store and the check run only | A pilot PR's shadow review lists every in-scope rule and its findings | FR-E1–E5 |
| D2.2 | Verification: refutation, importance, evidence that resolves in the checkout, outcomes stored per finding | Per-rule refutation rate is a queryable metric | FR-V1–V4, FR-V6 |
| D2.3 | Context providers: the search container, ticket text, ownership and history | An agent finds a definition and its references across the monorepo without a build | FR-E12–E14 |
| D2.4 | Comment budget, ranking, overflow by expected value and the pre-launch volume estimate, computed but not posted | A config PR shows expected comments per PR for the rule it promotes | FR-P1, FR-P2, FR-P6, FR-E16, FR-D2 |
| D2.5 | PR lifecycle: incremental re-review, stable finding keys, rebase reconciliation, drafts and stacks | A push re-reviews only the changed files and no finding is recorded twice | FR-C1–C6 |
| D2.6 | Degraded operation: shedding order, explicit coverage, incomplete runs, per-repo queueing | A provider outage yields "review did not complete", never "no findings" | FR-D1–D6 |
| D2.7 | Adjudication tool and the baseline comparison board. This is what produces the exit sample, so it ships before the sample can start | Owners can label a random shadow finding right, wrong or unimportant, and the board shows precision against the baseline | §14 exit, FR-X8 |

**Phase 3 — Comment.**

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D3.1 | Posting: Conventional Comments labels, rule and doc links, threads that resolve themselves on push | A finding posts with its label and link and resolves with "Addressed in <sha>" when the lines change | FR-P3, FR-P4, FR-C2 |
| D3.2 | Structured feedback, implicit signals, role weighting and human-agreement credit | Each dismissal reason routes as FR-B2 says and an author's dismissal alone cannot demote | FR-B1–B3, FR-B7, FR-B8 |
| D3.3 | One-click suggestions with patch validation | A suggestion that does not apply cleanly is dropped and the finding posts without it | FR-F1–F3 |
| D3.4 | Linter dedupe with the CI wait window, and human-comment dedupe | An overlapping CI finding minimizes the bot's comment and counts against no one | FR-E7, FR-E15 |
| D3.5 | Weekly effective-FP computation, owner digests, auto-demotion and `review_by` expiry | A rule over the demotion threshold drops to shadow by an auto-merged config PR | FR-B4, FR-B5, §7 |
| D3.6 | Convergence and learning: round cap, class-of-bug grouping, teach-the-bot, learned importance priors | A PR past the round cap gets one summary comment; a correction opens a proposal for the owner | FR-C8, FR-C9, FR-L6.5, FR-B9 |
| D3.7 | CLI on the working tree, and SARIF for Security findings | `exemplar review` evaluates uncommitted changes; a Security finding appears in code scanning | FR-S4, FR-P5 |

**Phase 4 — Shift left and bug lane.** Two independent tracks that run concurrently.

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D4a.1 | Export generator and export PRs, with the drift board | Every opted-in repo has an export PR and the board shows which config SHA it reflects | FR-S1, FR-S2, FR-S6 |
| D4a.2 | Pre-PR checks for coding agents through the CLI and `/v1/evaluate`, with caller authentication and attribution. The MCP server stays deferred (technical spec, Appendix C) | A coding agent runs the engine on its own diff before opening a PR and its usage is attributed | FR-S3 (via CLI), FR-S7 |
| D4a.3 | Upstream-effectiveness tracking | Per rule, the violation rate in agent-authored PRs with the export versus without | FR-S5 |
| D4b.1 | Bug lane in shadow: allowlisted classes on PRs above the risk threshold | Each class has its own precision and kill switch | FR-E6 |
| D4b.2 | Verifier quorum for the bug lane | A bug finding posts only when k of n verifiers confirm it | FR-V7 |
| D4b.3 | Clean-PR false-alarm set and the change gate, then bug findings on critical paths | A prompt, tool or model change is kept only if the false-alarm rate does not rise | FR-X12 |

**Phase 5 — Auto-approve.**

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D5.1 | Stage 1 gates in shadow, emitting a would-have-approved trace. Runs for weeks before anything approves | Every eligible PR has a full gate trace and no approval is posted | FR-A4, FR-A6–A8, FR-A10, FR-A11, FR-A13, FR-D5 |
| D5.2 | Stage 2: `merge_safety_score` defined and `review_confidence` calibrated (closing the two §16 questions), with verifier calibration | The calibration set meets the FR-X9 targets with the configured providers | §12, FR-X9, FR-E10 |
| D5.3 | Approval mechanics: `APPROVE`, dismissal on push, base-drift re-run, the compliance constant, owner digest entries | An approval is dismissed on the next push and re-run when the base moves | FR-A2, FR-A5, FR-A9 |
| D5.4 | Red team extending FR-X10, counter-metric boards with alerts, and the accountability loop | Zero red-team approvals; every incident on an auto-approved PR is recorded against its gate trace | FR-A12, §13.1 |
| D5.5 | First opted-in repo, narrow paths, `max_owner_areas` of 1 | The §14 Phase 5 exit, measured over four weeks | §14 |

**Phase 6 — Platform.**

| # | Deliverable | Done when | Requirements |
|---|---|---|---|
| D6.1 | A second internal caller judged through `/v1/evaluate` | The caller is in production | G4 |
| D6.2 | Fixer for human review comments | A reviewer's comment gets a validated suggestion from the bot | FR-F4 |
| D6.3 | Editor integration | Findings appear in the editor on the working tree | FR-S4 |
| D6.4 | Web UI beyond read-only: owner actions, playground, transcripts | Owners run the Phase 1 workflow from the UI | FR-L6.1 |
| D6.5 | Merged-change clustering | A periodic job opens candidates from clusters of merged changes | FR-L1.9 |
