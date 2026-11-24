<a id="3-goals-and-non-goals"></a>

## 3. Goals and Non-Goals

<a id="31-goals"></a>

### 3.1 Goals

- **G1 — High-signal findings.** Raise the **severity-weighted** accepted-finding rate (`blocking` 3, `important` 2, `suggestion` 1; definitions in §13) from a generic-bot baseline (~21%, unweighted) to **≥ 65%**, at or above the human reviewer acceptance rate, with an **effective false-positive rate ≤ 10%** per rule (Tricorder's bar; see §9), **and** catch at least **50%** of the incident set (FR-X11: bugs that caused a revert or incident, replayed against the current config). *(v0.9)* Acceptance alone rewards findings that are easy to fix, not findings that matter; weighting by severity and pairing it with incident recall keeps the corpus from drifting toward the mechanical. Raw acceptance stays informational (§13.2).
- **G2 — Encode organizational judgment.** Convert historical review comments into a curated, owned, testable rule corpus with citations back to source comments.
- **G3 — Safe autonomy.** Auto-approve a defined class of low-risk PRs without a human reviewer, with deterministic guardrails and an instant kill switch.
- **G4 — One evaluator, many callers.** A single alignment engine serves PR review, a rule-authoring playground, a local pre-PR CLI, IDEs and coding agents (via MCP), and other agents (branch validation, test generation).
- **G5 — Cost and latency are visible, not optimized for.** Every review reports what it cost and how long it took (§13.2, §17.4). These are informational: precision and encoded judgment come first, and no phase exits on a cost or latency number. A soft per-run ceiling (default $10, configurable) exists only to stop runaway reviews, and exceeding it triggers graceful degradation (§8.10) rather than a goal miss.
- **G6 — Shift left.** The same rules reach coding agents and IDEs before a PR exists, so the review bot's findings per PR fall over time for reasons other than noise suppression.
- **G7 — Fix, don't just flag.** A meaningful share of findings ship with a one-click fix.

<a id="32-non-goals"></a>

### 3.2 Non-Goals

- Purchasing or depending on a third-party AI code reviewer. (Open-source *libraries and engines* are allowed, subject to license review — see §15.)
- Training or fine-tuning a custom model. Models are swappable components behind a gateway.
- Nits and trivia (naming bikesheds, formatting). Linters and formatters own these, and Exemplar suppresses findings that duplicate them.
- Ever blocking a PR. The bot never posts `REQUEST_CHANGES`.
- Self-modifying behavior. Learning never changes enforcement without a merged, human-approved config change. (Unlike some commercial tools that auto-create rules from reactions, this is a deliberate choice: optimizing directly for acceptance invites the bot to learn to say what people like to hear.)
- Broad "review everything" commentary (PR walkthroughs, style opinions). Out of scope for v1; considered in Phase 5.
