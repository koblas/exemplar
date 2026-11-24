<a id="8-functional-requirements-enforcement-plane-online"></a>

## 8. Functional Requirements — Enforcement Plane (online)

<a id="81-lanes"></a>

### 8.1 Lanes

| Lane | What it catches | How it runs | Cost |
|---|---|---|---|
| **1. Compiled rules** | Org rules expressible as patterns | Semgrep / ast-grep on changed files | ~$0 |
| **2. Exemplar agents** | Org rules needing judgment | One agent per in-scope rule, ≤ 3 concurrent | Most of the budget |
| **3. Bug lane** — *new* | Novel logic errors no rule describes: null/undefined paths, off-by-one, broken error handling, race conditions, resource leaks, contract mismatches with callers | A small set of specialized agents (e.g. correctness, concurrency, error handling), restricted to *bugs* , never style or preference | Bounded share of budget, scaled by PR risk and size |

*(Borrowed: Anthropic's Code Review keeps false positives low by restricting itself to logic errors and actual bugs — things an engineer would almost always fix. The bug lane adopts the same restriction so it cannot erode trust in the exemplar lane.)*

| ID | Requirement |
|---|---|
| FR-E1 | **Inputs:** diff, a checkout of the commit, existing review threads, effective rules, and **context providers** (§8.3). |
| FR-E2 | **Deterministic scope** by `file_patterns` before any model call; zero-match rules never run. |
| FR-E3 | **One agent per exemplar** by default. *(v0.9)* `concurrency.rules_per_agent` (default 1, at most 5) may group rules of the same category with overlapping `file_patterns` into one agent, so related rules share one read of the code. Any value above 1 is adopted only after the golden set (FR-X5) shows no loss of precision or recall against 1; the one-giant-prompt failure mode (constraint 4) is what the cap and the eval guard against. |
| FR-E4 | ≤ 3 concurrent agents per review (configurable). |
| FR-E16 (not built: #262) | **Overflow by expected value** — *v0.9*. When more rules are in scope than `max_exemplar_rules` allows, the ones skipped are the lowest in **expected value for this diff**: the rule's precision × its severity weight × its historical hit rate on the changed paths, not precision alone. Skipped rules are listed in the check run as before ("not run on this review"). |
| FR-E5 | Purpose-built harness, read-only tools, high-capability models by default. |
| FR-E6 | **Bug lane** runs only on PRs above a risk/size threshold or on paths flagged critical, and only emits findings in an allowlisted set of bug classes. It has its own per-class precision tracking and kill switch. |
| FR-E7 | **Linter dedupe:** findings that overlap a finding from the repo's existing linters or CI static analysis on the same lines are dropped. **Timing:** CI results usually arrive after the bot's findings are ready. The bot waits for linter/static-analysis check runs up to a configurable window (default 2 minutes after findings are ready); if they have not reported, it posts, and when CI later reports an overlapping finding the bot minimizes its own comment as outdated and records it as a linter duplicate. Overlap counts against neither the rule's precision nor the author's feedback. |
| FR-E15 | **Human-comment dedupe** — *new* : existing review threads (FR-E1) are used to avoid repeating a human. If a human reviewer has already commented on the same lines and a light-model check judges the intent equivalent, the finding is not posted; it is recorded as `human_duplicate` and counts as agreement with the reviewer, not as a false positive. The bot never replies to or grades human comments outside the Phase 5 Fixer (FR-F4). |
| FR-E8 | Verdict is only ever `APPROVE` or no-op. **Never `REQUEST_CHANGES`.** |
| FR-E9 | Identical behavior across callers (production, playground, CLI, MCP, agents). |

<a id="82-model-diversity-new"></a>

### 8.2 Model diversity — *new*

*(Borrowed: Uber found the best configuration paired a generator from one model family with a grader from another.)*

| ID | Requirement |
|---|---|
| FR-E10 | The gateway supports **at least two model providers** . The verification stage defaults to a different model (ideally a different family) than the generator for the same finding, so their blind spots are less correlated. *(v0.9)* **Independence is the requirement; a second family is a preference.** A verifier is independent when it runs in a fresh context with only the finding, the rule and the code (never the generator's reasoning), and for the bug lane when the quorum (FR-V7) agrees. `same_family` is still recorded on every finding, but it stops gating auto-approve (§12 Stage 2) once the verifier calibration (FR-X9) passes with the configured providers. Until then a single provider blocks approval, which is the situation at the time of writing. |
| FR-E11 | Model assignment per stage and per rule is configuration, evaluated on the golden set before change. |

<a id="83-context-providers-new"></a>

### 8.3 Context providers — *new*

*(Borrowed: full-codebase indexing and ticket context in commercial reviewers; Meta and Google review tools rely on monorepo-wide code search — which we must build ourselves.)*

| ID | Requirement |
|---|---|
| FR-E12 | **Self-contained repository context:** agents can find definitions, find references, find the packages that depend on a changed file, and search text across the whole monorepo — at the default-branch snapshot, overlaid with the PR's own changes — without running any build. Served by one in-cluster container embedding an open-source code search engine (zoekt) with symbol extraction; references are scoped to the defining package and its importers, discovered by searching import statements. Read-only, rate-limited, results capped; degrades to plain git search if the index is unavailable. See implementation plan §5.5. *(v0.10)* **The search container is optional.** An installation without it answers references and history from the review's own checkout, with the same caps, and the check run states that other repositories were not searched. That is an install mode, not a degraded run: on a repository with no `context.extra_repos` every caller is in the checkout, the bug lane runs in full and the review may auto-approve (§12). On a repository that names other repositories, checks that need their callers are skipped, the run is degraded and it cannot auto-approve (FR-D5). The **install ladder** is shadow without search; posting with search once cross-repository context is worth running it; auto-approve once the Phase 5 criteria hold (§14). |
| FR-E13 | **Ticket context:** when a PR links an issue (GitHub issue or Jira key in title/body/branch), its title and description are fetched and supplied as untrusted context. |
| FR-E14 | **Ownership and history context:** CODEOWNERS, recent incidents touching the paths, and recent reverts are available to the bug lane and to gates. |

<a id="84-verification-stage-new"></a>

### 8.4 Verification stage — *new*

*(Borrowed: Anthropic Code Review's verification step that attempts to disprove each finding; Uber uReview's separate grader; Google Tricorder's insistence that analyzers prove usefulness.)*

Every candidate finding from Lanes 2 and 3 is sent to a verifier before validation; Lane 1 findings receive the importance check only (FR-V6):

| ID | Requirement |
|---|---|
| FR-V1 | **Refutation.** The verifier receives the finding, the rule (for Lane 2), the relevant code, and read-only tools, and is instructed to **find evidence that the finding is wrong** . Output: `{verdict: confirmed\\|refuted\\|uncertain, evidence: [path:line…], reason}` . Only `confirmed` passes. |
| FR-V2 | **Importance.** The verifier also scores whether the issue matters *in this context* (e.g., is this path hot, user-facing, security-relevant). Findings below the rule's or lane's importance threshold are dropped (class 2 false positives, §2.2). |
| FR-V3 | **Evidence is checked in code.** Cited evidence lines must exist in the checkout; a verdict whose evidence doesn't resolve is treated as `uncertain` . |
| FR-V4 | Verifier outcomes are stored per finding, so per-rule refutation rate is a first-class metric (a rule refuted > 50% of the time is a bad prompt). |
| FR-V5 | Verification runs only on candidate findings, which are few. Its cost is reported with the review (§13.2) and is subject to the same soft ceiling and degradation order as the lanes (§8.10). |
| FR-V6 | **Importance check for compiled rules** — *new* . Lane 1 findings skip refutation (a pattern match is not a hallucination) but *not* the importance check: a lightweight model scores importance in context (FR-V2) for compiled rules in the Performance, Style and Nit categories, and for any compiled rule that opts in. Security and Correctness compiled findings post without it. This closes the gap where a Semgrep performance pattern in an offline tool would post exactly the class-2 false positive §2.2 promises to defend against. |
| FR-V7 | **Verifier quorum for the bug lane** — *v0.7*. A Lane 3 finding is sent to `verifier.quorum.n` verifiers (default 3, each a separate call; different models or families when more than one provider is configured) and posts only when at least `verifier.quorum.k` (default 2) return `confirmed`. Lane 2 findings keep the single verifier of FR-V1 by default; a rule may opt into the quorum with `verify: quorum`. The agreeing share is stored with the finding and is a ranking input for the comment budget (FR-P1), after severity. Quorum calls count toward the soft ceiling and are shed before the lanes in §8.10. *(Borrowed: Anthropic's reviewer posts a bug-hunting finding only when enough separate verifiers vote that it is real.)* |

<a id="85-deterministic-validation-in-code"></a>

### 8.5 Deterministic validation (in code)

| ID | Requirement |
|---|---|
| FR-T1 | **Attach provenance:** rule id and citation come from the rule, never from model output. |
| FR-T2 | **Validate anchors** against visible diff hunks; drop the rest. |
| FR-T3 | **Zero coverage = failure** , never "zero violations." |
| FR-T4 | **Exclude own reactions** from feedback. |
| FR-T5 | Schema-invalid output is discarded and logged, never repaired by another model call. |
| FR-T6 | The model has no write tools; the orchestrator posts. |

<a id="86-ranking-comment-budget-and-presentation-new"></a>

### 8.6 Ranking, comment budget and presentation — *new*

*(Borrowed: AutoCommenter calibrated thresholds by replaying full historical reviews to estimate comment volume before launch; Conventional Comments for legible severity.)*

| ID | Requirement |
|---|---|
| FR-P1 | **Comment budget:** at most N findings posted per PR (default 7) and M per file (default 3), ordered by severity, then verifier confidence, then rule precision. Overflow is summarized in the check run, not posted inline. |
| FR-P2 | **Pre-launch volume estimate:** before a rule goes `active` , replay it over the last 30 days of PRs in its scope and report expected comments per PR; owners see this in the config PR. |
| FR-P3 | **Conventional Comments labels:** every comment starts with a label derived from severity and category, e.g. `issue (blocking):` , `suggestion (performance):` , `question:` . |
| FR-P4 | Every comment links to the rule file at its SHA and the rule's doc citation, and carries the structured feedback controls (§9). |
| FR-P5 | **Security-category findings are also emitted as SARIF** to GitHub code scanning, so security teams triage them in the tool they already use and its dismissal reasons feed our feedback loop. |
| FR-P6 | **Budget exemptions** — *new* : Security-category and `blocking` -severity findings are never dropped by the comment budget. If they alone exceed N, they are posted as one grouped comment per file listing every occurrence, and the check run is marked `neutral` with a visible summary. Only `important` and `suggestion` findings compete for the remaining budget. |

<a id="87-fixes-new"></a>

### 8.7 Fixes — *new*

*(Borrowed: Google's ML-suggested edits for resolving review comments; Uber's Fixer; GitHub suggested changes.)*

| ID | Requirement |
|---|---|
| FR-F1 | For rules with `fix_policy: suggest` , when the fix is local to the anchored hunk, the finding includes a GitHub `suggestion` block the author can apply in one click. |
| FR-F2 | Suggested fixes are validated in code: the patch must apply cleanly, stay within the anchored range, and (where a fast check exists, e.g. `gofmt` , a parser) must parse. Fixes failing validation are dropped; the finding is posted without a fix. |
| FR-F3 | Applying a suggestion is the strongest acceptance signal and is tracked separately. |
| FR-F4 | **Phase 5:** a Fixer that proposes edits in response to *human* review comments (reviewer leaves a comment → bot offers a suggestion), reusing the same harness and validation. |

<a id="88-public-api-sketch"></a>

### 8.8 Public API (sketch)

```
POST /v1/evaluate
{
  "repo": "org/game-engine", "commit": "a1b2c3d", "base": "f9e8d7c",
  "caller": "production | playground | cli | mcp | agent | eval",
  "lanes": ["compiled", "exemplar", "bug"],
  "rules": ["*"] | ["blocking-fetch-in-loop"],
  "rule_overrides": [ ...rule... ],          // playground only
  "mode": "review | eval"
}

200 OK
{
  "findings": [{
    "rule_id": "blocking-fetch-in-loop",     // attached by code
    "lane": "exemplar", "severity": "important",
    "path": "src/net/fetch.cpp", "line_start": 142, "line_end": 147,
    "message": "...", "suggestion": "...",   // optional, validated
    "verification": {"verdict": "confirmed", "importance": 0.82, "model": "..."}
  }],
  "suppressed": {"budget": 3, "refuted": 5, "linter_duplicate": 2},
  "verdict": "APPROVE | NO_OP",
  "metrics": { ... }, "eval": null
}
```

<a id="89-pr-lifecycle-new"></a>

### 8.9 PR lifecycle — *new*

Everything above describes one review of one commit. A PR is a sequence of pushes, rebases and conversations, and the bot's behavior across that sequence is what authors actually experience.

| ID | Requirement |
|---|---|
| FR-C1 | **Incremental re-review on push.** A new push re-reviews only the files whose hunks changed since the last reviewed commit, plus any file a previously posted finding anchors to. Unchanged files keep their prior results. Rules run again only where their scope intersects the changed set. |
| FR-C2 | **Finding identity across pushes.** Each finding has a stable key (rule id + file + normalized anchor content). A finding with the same key is never posted twice. A finding whose anchored lines were changed is re-verified: if it no longer holds, the bot resolves its own thread with a one-line note ("Addressed in `abc123` "); if it still holds, the existing thread is left in place, not re-posted. |
| FR-C3 | **Dismissed stays dismissed.** A finding the author dismissed with a structured reason (§9) is not re-posted on later pushes to the same PR, even if the model would find it again. |
| FR-C4 | **Rebase and force-push.** When the base changes or history is rewritten, prior comments are re-anchored by content; findings that no longer anchor to a visible hunk are resolved as outdated (FR-T2), not re-posted at a guessed location. The review is reconciled, not restarted. |
| FR-C5 | **Draft PRs** are not reviewed until marked ready, unless the repo opts in to draft review. **Stacked PRs** are reviewed against their declared stack base (the parent branch), not the trunk, so findings are not duplicated down the stack. |
| FR-C6 | **Config drift within a PR.** Each review records the config SHA it ran under (§17.4). If config changes mid-PR, findings from rules that were killed or demoted are resolved as outdated with a note; new rules apply only to the next push. |
| FR-C7 | **Lifetime accounting is informational.** Cost and time are recorded per run and summed per PR for §13.2, but repeat pushes do not consume a shared budget. The soft ceiling (§8.10) applies per run; incremental review (FR-C1) is what keeps lifetime cost proportionate. |
| FR-C8 | **Round cap** — *v0.7*. A review round is one run that posts at least one new finding. After `rounds.max` rounds on one PR (default 3), the bot posts no further inline findings; it posts one summary comment listing what is still open, says that the change may need a different approach, and leaves the check run `neutral`. Later pushes are still reviewed (FR-C1) and still resolve addressed threads (FR-C2), and Security and `blocking` findings are still posted (FR-P6). The cap resets when a human reviewer posts or the PR is marked ready after being a draft. Rounds per PR are recorded for §13.2. *(Borrowed: one of Anthropic's PRs went through more than fifty review rounds overnight before they added a cap and diff-only re-review.)* |
| FR-C9 | **Class-of-bug findings** — *v0.7*. When one rule or one bug class matches more than `posting.class_threshold` places in one review (default 3), the review posts one grouped comment that names the pattern and lists every occurrence, instead of one anchor now and the rest on later rounds. `posting_mode: grouped` already does this per rule; this applies it per review for `per_occurrence` rules and for the bug lane. The group has one finding key (FR-C2) and one feedback thread. |

<a id="810-degraded-operation-new"></a>

### 8.10 Degraded operation — *new*

Latency and cost are not goals (§3.1, §13.2), but a review that quietly does less than it claims is a precision failure. This section defines what the bot does when it cannot do everything, and how it says so.

| ID | Requirement |
|---|---|
| FR-D1 (not built: #263) | **Degradation order.** When a run approaches its soft ceiling or a dependency is slow, work is shed in a fixed order: (1) bug lane, (2) exemplar rules with the lowest recent precision, (3) verification importance scoring (findings default to posting), never (4) refutation or deterministic validation, which always run for anything posted. |
| FR-D2 | **Bounded fan-out.** At most K exemplar rules run per review (default 40, configurable). On a PR where more are in scope, the lowest-precision rules are deferred and listed in the check run as "not run on this review"; this is reported as partial coverage, not as a clean pass. |
| FR-D3 | **Provider failover.** The gateway (FR-E10) fails over to the second provider for any stage. If both are unavailable, the run is marked `incomplete` ; the check run says "review did not complete" with the reason and a retry link. No comments and no approval are posted from an incomplete run. |
| FR-D4 | **Coverage is explicit.** The check run (§17.4) reports coverage as the share of in-scope rules that actually ran to completion, and lists what was shed and why. "Zero findings" is only claimed at 100% coverage (FR-T3); otherwise the summary reads "no findings from the rules that ran". |
| FR-D5 | **Degraded runs cannot auto-approve.** Any shedding under FR-D1/D2 or any incomplete stage disqualifies the PR from auto-approve for that run (§12). |
| FR-D6 | **Backpressure.** Reviews are queued per repo; a burst (mass rebase, release branch cut) is served in order with incremental review (FR-C1), and the queue depth is visible to the platform team. Latency is reported (§13.2) but has no SLO in v1. |

<a id="811-data-handling-privacy-and-repo-eligibility-new"></a>

### 8.11 Data handling, privacy and repo eligibility — *new*

The system sends diffs, repository context, ticket text and three years of review comments to external model providers, and stores identity-bearing records about who said what in review. That needs rules.

| ID | Requirement |
|---|---|
| FR-H1 | **Repo eligibility is explicit.** Repos are opted in individually. A platform-level denylist excludes repos that must never be sent to a model provider (secrets, compliance-scoped, legal hold); no config in the repo itself can override it. Eligibility applies to review, mining and context providers alike. |
| FR-H2 | **Path exclusions.** Vendored and generated code ( `.gitattributes` `linguist-generated` / `linguist-vendored` ), lockfiles, binaries, minified assets and files above a size threshold are excluded from review, mining and context by default. Repos can extend this with `.exemplarignore` . Excluded paths are reported as "not reviewed", not as clean. |
| FR-H3 | **Secrets never leave.** Diffs and context snippets are secret-scanned before any model call; matches are redacted in place and the finding "possible secret in diff" is posted deterministically (no model involved). A PR whose diff cannot be redacted is marked incomplete (FR-D3). |
| FR-H4 | **What leaves, to whom, is documented and logged.** Each model call records provider, model, stage, repo and the classes of data sent (diff, context, ticket, comments). Providers are used under enterprise agreements with no training on inputs and defined retention; the approved provider list is a hardcoded constant alongside FR-A2. |
| FR-H5 | **Fork PRs.** PRs from forks are reviewed read-only with no repository secrets available to the harness, never auto-approved, and only if the repo opts in to fork review. The GitHub App's write token is never used in a fork context. |
| FR-H6 | **Identity in mined data.** The learning plane stores reviewer and author identity pseudonymized; rule citations point at comment URLs, not names. Rankings use distinct-reviewer counts, not reviewer identity. Raw comment text is retained only as long as the rule corpus cites it. |
| FR-H7 | **Retention and audit.** `approval_decisions` and full gate traces (FR-A4) are retained for the period Security/Compliance sets (default 7 years); review findings and feedback events for 2 years; model call logs for 90 days. Retention periods are configuration owned by Security, not the platform team. |
| FR-H8 | **v1 language scope.** Lane 1 engines and the symbol extraction / import patterns in the search container (FR-E12) are language-specific; the languages supported in v1 are listed in the implementation plan, and exemplar rules for other languages run with text-only context and are labelled as such in the config PR. |
