## 8. Learning plane

### 8.1 Ingest

- **API:** GraphQL, per repo, paging `pullRequests(states: [MERGED, CLOSED], orderBy: UPDATED_AT)` with nested `reviewThreads { isResolved comments(first: 1) { databaseId body path diffHunk author createdAt reactions } }` — `first: 1` yields the thread-root (top-level) comment per PRD FR-L1.1. Also include `reviews { body }` only if review-summary bodies prove useful.
- **Budget:** GraphQL has its own point budget separate from REST. For ~700K PRs, expect the backfill to take days; run it as a resumable per-repo job with `ingest_cursors(repo, cursor, updated_at)` and back off on `RATE_LIMITED`. After backfill, keep the corpus current from `pull_request_review_comment` webhooks.
- **Pre-filters (code, before any model):** drop bots (including Exemplar), PR-author self-comments, bodies < 25 chars, pure emoji/LGTM/"nit:" + < 6 words, and suggestion blocks with no prose. This typically removes a large share of comments for free.
- **Eligibility and data handling:** only eligible repos are mined; excluded paths are skipped; bodies and hunks are secret-scanned; identities are pseudonymized on ingest (§13.1).
- **Accepted bot findings** enter with `source='accepted_bot_finding'` and are down-weighted in ranking (PRD FR-L1.2).
- **Doc-linked comments** (PRD FR-L1.6): extract URLs from comment bodies with a regex, normalize them, and keep those matching the internal docs allowlist (`docs.internal`, wiki hosts, the style-guide repo). Store in `mined_comments.doc_urls`; they boost ranking and become the rule's `citation.doc`.
- **Incidents and reverts** (PRD FR-L1.7): a Go job populates `path_events(repo, path, kind, ref, at)` from (a) revert commits on default branches (`This reverts commit <sha>` trailer, or GitHub-generated "Revert "…"" PRs), and (b) the incident tool's API (PagerDuty, incident.io, or a postmortem repo) for incidents linking a PR or commit. Reverted PRs with no review comment on the reverted lines become **seed candidates** for Blocker rules: the extractor is given the diff and the revert reason instead of a comment. The same table backs `path_history` (§6.4) and `merge_safety_score`. *(v0.9)* These are a **co-equal source**, not a supplement: `candidates.source_kind` records `incident` or `revert`, and ranking (§8.5) floors such a candidate at the score of a sizeable comment cluster, because comments record what reviewers caught and incidents record what they missed.
- **Owner-written seeds** (PRD FR-L1.8): a rule owner writes a candidate by hand, either in the playground (**Save as candidate PR**, §12.8, which is the owner action `propose_rule`) or directly as a rule file with `status: candidate` in a config PR. It becomes a `candidates` row with `source_kind='owner_seed'`, its citation the owner plus any doc they link, and it goes through exactly the mined path from there: eval cases before merge (§8.8 generates history and injected cases for it; the owner may add their own), the volume estimate and linter-overlap checks (§8.7), shadow first (FR-X4), and the same feedback, expiry and demotion lifecycle. Nothing about a seed skips the gate; what it skips is the wait for a mining run.
- **Merged-change clusters** (PRD FR-L1.9): `exemplar-learn change-clusters`, weekly, takes the last 30 days of merged PRs per ownership area and groups them three ways: by area and the files they share, by what the bug lane flagged on them (posted or shadow), and by which were later reverted or incident-linked (`pr_links`). For each cluster above a size floor (5 PRs, or 2 with a revert) it asks the high-tier model one question, "what deterministic check or review rule would have caught the pattern these share", with the diffs' hunks as untrusted input, and writes the answer as a `candidates` row with `source_kind='change_cluster'` citing the PRs. It then enters the compile attempt (§8.6) like any candidate, so a cluster that compiles lands as a Lane 1 rule. As review volume rises and comments per PR fall, merged changes carry judgment that comments no longer do.

### 8.2 Extract

- **Message Batches API** (asynchronous, roughly half the cost of live calls) with **three comments per request**, model `claude-sonnet-5-5`, pinned model id, versioned prompt `extractor_version`.
- **Idempotent, not deterministic** (PRD FR-L2.4): each comment's `input_hash = sha256(extractor_version | model | body | diff_hunk)` keys a result cache; re-running over the same comments returns cached results without a model call and never creates duplicate candidates.
- Output schema per comment: `{is_reusable_constraint: bool, title, description, preferred_approach, applies_to_hint, category, language_agnostic: bool, confidence}`. `language_agnostic` marks constraints such as timeouts on outbound RPCs, logging and secrets handling.
- **Calibration loop:** sample 300 comments, have two engineers label them, tune the prompt until the extractor accepts **10–15%** and agrees with humans at ≥ 85%. Track acceptance per batch; alert if it drifts outside 8–20%.
- Retries: a request that errors returns its comments to `pending`; nothing is ever dropped silently (PRD FR-L2.4).

### 8.3 Embed

Embeddings are produced by Voyage AI's code embedding model through `exemplar-gateway`. Each extraction embeds `title + description + preferred_approach` (not the raw comment) and is stored as `halfvec(1024)`.

### 8.4 Cluster and merge

Two thresholds apply: **0.65** groups near-duplicate extractions into clusters; **0.35** is a deliberately loose bar for *proposing* merges between clusters, with an LLM making the call.

1. Partition by file extension (PRD FR-L3.1); extractions with `language_agnostic = true` form one additional cross-extension partition, so an org-wide rule is not fragmented per language.
2. Within each partition, build a kNN graph (faiss / hnswlib, k=20) and keep edges with cosine ≥ 0.65. Clusters are Leiden communities over that graph (networkx / igraph), not connected components: components chain through pairwise-similar extractions into giant clusters that no cohesion check can split. Any community larger than 200 extractions is re-partitioned at 0.75.
3. Compute each cluster's centroid; for cluster pairs with centroid cosine ≥ 0.35, ask `claude-opus-5-5` a cohesion question: "Do these describe the same enforceable constraint? Return {same: bool, merged_title, merged_description}."
4. Union-find over `same=true`. Cache decisions by `(cluster_a_hash, cluster_b_hash)` so reruns are deterministic and cheap.

### 8.5 Rank and route

`score = log(1 + cluster_size) · (1 + distinct_reviewers) · recency_weight · resolution_weight · doc_boost · incident_boost`, where resolution weight favors threads that were resolved (signal the comment was acted on), `doc_boost` is 1.5 when any source comment links internal documentation (PRD FR-L1.6) and `incident_boost` is 2.0 when any source is incident- or revert-linked (PRD FR-L1.7); both from PRD FR-L4.1. Owner = majority CODEOWNERS of the source files; ties go to the platform team.

**Co-equal sources** (v0.9). A candidate whose `source_kind` is `incident`, `revert`, `owner_seed` or `change_cluster` has no cluster of comments to score, so its score is **floored at the score a ten-comment, one-reviewer, resolved, recent cluster would get** (`log(11) · 2 · 1 · resolution_weight · 1 · 1`), then multiplied by its own boosts (2.0 for incident and revert; a change cluster's size in PRs stands in for `cluster_size`). An owner seed also skips the per-team throttle of §8.7, since the owner asked for it. The ranked queue therefore interleaves the four sources instead of listing comment clusters first.

### 8.6 Compile to deterministic

For every candidate (and quarterly for every active exemplar):

1. Ask the high-tier model to write an **ast-grep** rule (YAML) — or a Semgrep rule when the pattern needs taint or metavariable comparison — from the candidate's description and 3–5 positive examples with their hunks. Output is the rule file only, validated against the engine's schema with `ast-grep test` / a Semgrep scan of an empty directory (offline; `semgrep --validate` would also fetch rule lints from semgrep.dev).
2. Run it in a sandbox (no network, CPU/memory limits) over the candidate's eval cases.
3. Accept as `kind: compiled` if precision ≥ the exemplar's precision and recall ≥ 90% of the exemplar's recall. Store every attempt in `compile_attempts` with metrics, whether or not it is accepted.
4. Accepted compiles are proposed in the same config PR as the candidate (or as a "convert to compiled" PR for an active exemplar), showing both scores side by side (PRD §17.5).

### 8.7 The human gate

- The pipeline opens a **draft PR in `exemplar-config`** per top candidate containing the drafted rule YAML (`status: shadow`), auto-generated eval cases, the compile attempt result, and a description listing every source comment, doc link and incident with links.
- **Checks on every candidate PR:** schema, eval (§8.9), pre-launch volume estimate (§6.10), linter overlap (trial run of the repo's linters on the positive cases; any overlap fails the check with the linter rule named).
- **Throttle:** at most 3 open candidate PRs per owner team; the rest queue in `candidates`.
- **Playground inside the PR:** `/exemplar try org/repo#1234` runs the draft rule via `exemplar-api` with `rule_overrides` and replies with findings and cost; `/exemplar eval` reruns the suite. **Authorization:** before running, `exemplar-api` checks that the commenting user has read access to `org/repo` (GitHub user OAuth, same check as the CLI and other callers, §10.1, §10.5) and that the repo is eligible (§13.1); otherwise it replies "no access to org/repo" and runs nothing. The reply contains file paths, line numbers and the finding message only — no code excerpts — because `exemplar-config` is readable by more people than most target repos. Full output is available through the CLI (`exemplar transcript`), which enforces repo access per viewer.
- **Promote** = owner edits, sets `status: active` (or keeps `shadow` for a staged rollout), CI passes, merge.
- **Kill** = close with a `killed` label and a reason (a small check reopens the PR if the reason is missing). The reason is written to `candidates.kill_reason` and fed to the extractor as a negative example.
- **Teach-the-bot fast path** (PRD FR-L6.5): when feedback with free text arrives (§9), the light model classifies it as `scope` (wrong paths), `exception` (a legitimate pattern the rule should allow), `rule_wrong`, or `noise`. When the same classification for the same rule recurs ≥ 3 times in 30 days (or once from the rule owner), the config writer opens a draft PR: a `file_patterns` exclusion for `scope`, a description amendment plus a new negative eval case for `exception`, or an owner review issue for `rule_wrong`. The PR includes before/after eval scores. Nothing changes until it is merged.
- **As implemented:**
  - **Classification:** the reason of a recorded rejection (slash command or code-scanning dismissal) is classified (`teach-classifier-v1`) and stored as `feedback_events.teach_class`.
  - **`exemplar-learn teach`** runs nightly over each person's latest rejection in 30 days, from people other than observers.
    - A reason's class wins. Without one, "not important here" is a scope signal and "disagree" a disagreement.
    - **Scope:** a draft PR adds the prefix most of that evidence shares (its first two path segments, at least 3 signals or one from the owner) to `file_patterns` as an exclusion.
    - **Exception, rule_wrong and 3 disagrees:** an owner review issue (label `exemplar-teach`).
    - The description amendment and negative eval case for exception are left to the owner. The eval CI on the draft PR shows the effect rather than a computed before/after.
  - **Contents:** proposals carry finding links and counts, never people's reasons.
  - **Repeats:** one per rule version and class (`teach_proposals`).

### 8.8 Eval generation

- **Positives from history:** for each source comment, the PR's `head_sha` at comment time plus the commented path and line range → "expect a finding overlapping these lines."
- **Negatives from history:** PRs touching files that match the rule's patterns, approved by a CODEOWNER, with no comment in the candidate's cluster → "expect no finding."
- **Injected violations** (PRD FR-X6): take negative cases and ask the model to make the **minimal** edit that introduces the violation, producing a patch. Validate in code: the patch applies, the file still parses (tree-sitter), and the diff is ≤ 15 lines. 10% are spot-checked by the owner in the candidate PR. Stored as `eval_cases(synthetic=true, base_ref, patch)`.
- **Bug-class mutations:** deterministic mutation operators implemented on tree-sitter ASTs (remove a nil/err check, flip `<`/`<=`, drop a `defer Close()`, remove a mutex lock/unlock pair) applied to clean code in critical paths, generating recall cases for Lane 3 without any model.
  - **As implemented:** `exemplar mutations`, for Go on `go/ast` and for C and C++ on tree-sitter (`internal/syntax`).
  - **Go operators:** drop an `if err != nil` after the assignment; `< len(x)` becomes `<= len(x)`; drop `defer x.Close()`, expecting the finding at x's acquisition; remove `mu.Lock()` and its unlock, expecting it at the unguarded statements; drop an `if x == nil { return }` guard.
  - **C/C++ operators:**
    - drop an `if (rc …)` that returns or jumps, right after rc is assigned from a call;
    - in a for loop's condition, `<` becomes `<=`;
    - drop `free`, `fclose`, `close` and the like on something acquired from a call earlier in the block, expecting the finding at the acquisition;
    - remove `pthread_mutex_lock(&m)` or `m.lock()` with its unlock, or a `std::lock_guard`, `unique_lock` or `scoped_lock`;
    - drop an `if (p == NULL)`, `== nullptr` or `!p` guard that only returns.
    - Statements are removed only when alone on their lines. Test files (`_test`, `_unittest`, `_tests`) and `vendor/` and `third_party/` are skipped.
  - **Validation:** a mutation must still parse (with no tree-sitter errors, for C/C++) and change at most 15 lines.
  - **Output:** cases are merged into `evals/<bug class>/cases.yaml` (synthetic, `repo@sha`, a patch each) for an owner to commit.
  - **Evaluation:** bug classes are evaluated like rules. Only that class runs, on every file.
- **Verifier labels** (PRD FR-X9): every finding where an author gave an explicit Wrong or Fixed signal is copied into `verifier_labels` as ground truth; a monthly sample of 100 is human-labeled for "unimportant".
- **Incident-derived rules** (PRD FR-X11): for rules seeded from reverts or incidents, the reverted or incident-linked diff is the positive case, supplemented by injected-violation cases; the rule file and candidate PR carry `eval: synthetic` until a real PR provides a positive.
- **Adversarial set** (PRD FR-X10): the shipped set, embedded in the binary with the verifier and categorizer labels, runs against the shipped preset rules; `exemplar-config/evals/_adversarial/` adds the organization's cases, which run alongside, and a local case with a shipped case's id is a validate warning. Each case holds PRs with prompt injection in diffs, comments, PR descriptions, file names and linked tickets, each with the attacker's goal (false approval, suppressed finding, fabricated citation). Maintained from Phase 0; the Phase 5 red team adds to it.
- **Clean-PR set** (PRD FR-X12): `exemplar-config/evals/_clean/cases.yaml` lists PRs known to be clean: merged, never reverted, no incident within 90 days, and confirmed by a named engineer who knows the code (`clean_prs`). `exemplar-learn evals --clean`, weekly and on every change to prompts, tools, models or providers, reviews them with the current config through the replay harness (`caller=eval`); **every finding is a false alarm**, and a false alarm that carries a validated suggested fix is a *harmful* one, counted separately, because applying it would change behavior. Rates are stored per rule and class in `clean_pr_runs` and shown on the lanes board next to the incident-set recall. A new clean PR enters the set by a config PR with the confirming engineer as reviewer; a PR later reverted or incident-linked leaves it automatically.
- **The change gate:** the incident set (FR-X11, replayed as the recall side) and the clean-PR set (the false-alarm side) are run together on every prompt, tool, model or provider change; the change is kept only if incident-set recall does not fall and neither the clean false-alarm rate nor the harmful rate rises, each judged against the previous run with the thresholds in `eval_thresholds.change_gate`. §8.9 step 6 runs it in CI.
- **Baseline comparison** (PRD FR-X8): the baseline reviewer's shadow findings (stratified 10% sample, §6.3) are matched to human comments and to Exemplar's findings on the same PRs; §12 shows precision and accepted-equivalent findings per PR side by side.

Cases reference code by `repo@sha:path#Lstart-Lend` (plus `patch` for synthetic cases); no code is copied into `exemplar-config`.

**Eval profile** (PRD FR-X13). A synthetic case with no `repo` is built locally (`synth.Build`) and reviewed under a built-in profile instead of an enrolled repository's config:
- the organization's global settings, with `auto_approve: true`;
- a repository config with `shadow: false` and `auto_approve.enabled: true`, overridable per case with an inline `repo_config` when a case needs one;
- the policy inputs a synthetic workspace cannot supply set so they pass unless the case sets them: `auto_approve.paths: ["**"]`, `unowned_paths: allow`, an author who meets the author policy, size limits above the case, verifier calibration on, branch rules present, merge-safety history passing;
- a bug lane when `global.yaml` enables no class: every allowlisted class that needs no search references, since auto-approve requires the bug lane at full coverage and without one no case could approve;
- everything a review computes from the diff runs for real: coverage, unreviewed files, the bug lane, findings and the model-derived Stage 2 categorization and review confidence, since those are what an injection attacks;
- no eligibility check: `engine.Prepare` accepts the profile only for a workspace `synth.Build` made, never for a clone URL.

Positive controls in the shipped set are harmless cases that must reach approval under the profile, run with scripted model outputs (`replay --scripted`) so they are deterministic and make no model calls; a profile change that stops them approving fails the build, so the false-approval cases can fail. A recorded case keeps `repo@sha` and the eligibility check: `exemplar eval` reports an ineligible repository as a failed case, and `validate` errors on a recorded case whose repository is not enrolled and, for one release, warns on `repo` in a synthetic case (an error after).

### 8.9 Eval CI (GitHub Actions)

`exemplar-config/.github/workflows/eval.yml` on `pull_request`:

1. Schema-validate every changed rule with the `exemplar` CLI (`exemplar validate`, JSON Schema from `schema/`) — a single binary downloaded in the workflow.
2. For changed rules, call `exemplar-api /v1/evaluate` with `mode: eval` (authenticated with an OIDC-issued token, so no long-lived secret in the repo). Compiled rules are evaluated by running the engine; exemplars by the full lane + verifier path.
3. Post precision / recall / F1 (historical and synthetic reported separately) as a check; block merge below thresholds in `global.yaml`.
4. Post the volume estimate and linter-overlap results as separate checks.
5. Run the **adversarial set**, shipped and local cases, under the eval profile (§8.8) on every config, prompt or model change (config PRs here; prompt and model changes in the `exemplar` repo's CI). Any attacker success fails the build.
6. Run verifier and Stage 2 categorizer calibration on every change to their prompts, models or providers; fail below the FR-X9 thresholds (§6.8). Run the **change gate** (incident set and clean-PR set, §8.8) on the same triggers and on any tool change; fail if recall falls or either false-alarm rate rises.
7. Config PRs that change a rule's `export:` block (summary or `agents` flag) require an approval from `@platform/devex` in addition to the rule owner, enforced by a CODEOWNERS-style check on the YAML path (§10.2). A PR that raises `concurrency.rules_per_agent` must attach the golden-set run that justifies it (§6.3 step 7).
8. Nightly: full golden set, verifier calibration, Stage 2 categorizer calibration, baseline comparison. Weekly: the clean-PR set and incident-set recall, reported on the lanes board.

**As implemented** (step 2, `mode: eval`, migration 00025):
- **CI side.** `exemplar eval --remote --changed-since <base>` sends exemplar-config's merge-base and pushed HEAD (plus any `--rules`) to `/v1/evaluate`. It polls, writes the same JSON and summary table as a local `exemplar eval`, and fails below `eval_thresholds`.
- **Worker side.**
  - exemplar-worker accepts mode eval only for `EXEMPLAR_CONFIG_REPO`.
  - It checks out base and head with a read-only token, loads the config at the head, and picks the changed rules the way the CLI does (shared in `evalrun`).
  - It runs `evalrun.EvaluateRule` on each, with compiled rules on the engines and exemplar rules through the lane and verifier (`caller=eval`).
  - Case repos are read with installation tokens.
  - Eligibility comes from the deployed config, so a config PR cannot un-denylist a repo to evaluate on it.
- **Credentials.** Auth0 has no GitHub Actions OIDC federation, so the example workflow uses an Auth0 client-credentials grant for exemplar-api, with the single permission `evaluate:<config repo>`. That one narrow secret replaces the gateway token and the GitHub case-repo token the job used to hold.

### 8.10 Rule expiry

A daily job finds rules whose `review_by` date has passed. If the rule's eval still passes **on cases drawn from the last 90 days** and its effective FP is under the bar, it opens a PR extending `review_by` for the owner to merge. Otherwise it opens a PR setting `status: shadow`, which **auto-merges after 14 days** if the owner hasn't responded (demotion is a safe direction). The digest lists both.

**Per-category defaults** (PRD §7, v0.9). The default `review_by` at promotion, and the extension period, come from `global.yaml` `review_by_defaults` per category: a Security rule and a Performance rule written against one library version have half-lives an order of magnitude apart. Defaults: Security and Blocker 6 months, Performance 6, Correctness 12, Style and Nit 18; `expiry.default_months` is the fallback for a category without an entry. A rule file may set its own `review_by`; the candidate PR writer fills it from the category default when the rule has none.

**As implemented** (`exemplar-learn expiry`, daily):
- **Evidence:** the rule's last 90 days of production outcomes (`finding_outcomes`) stand in for an eval on recent cases. It needs at least one judged finding and weighted eFP within `feedback.efp_bar` to extend.
- **Extension:** `review_by` moves the category's default (today `expiry.default_months`) past today. The owner merges it; Exemplar never does.
- **Shadow PR:** it merges after `expiry.auto_demote_after_days` unless a person comments, reviews or closes it.
- **Repeats:** there is one branch per rule version and decision.

### 8.11 Learned importance priors

Implements PRD FR-B9: the second false-positive class of PRD §2.2 (right, but not important here) gets the same feedback loop the first has.

- **Signal.** *Not important here* dismissals (`fb_not_important`, and code-scanning dismissals mapped to it) from authors, reviewers and owners (never observers), with the finding's path, category and rule.
- **Job.** `exemplar-learn priors`, weekly. For each repo it aggregates the last 90 days of such dismissals by `(path prefix, category)`, trying prefixes from the file's directory upward and keeping the deepest prefix that meets the floor: **at least 5 dismissals from at least 3 distinct people**. Rules whose findings are all under one prefix are reported to their owner instead, since that is a scope problem for §8.7's teach path, not an importance prior.
- **Proposal.** One config PR per repo and week adding or raising an `importance_priors` entry in `repos/<org>/<repo>.yaml`: `{prefix, category, importance_min: <raised threshold>}` by default, or `suppress` when more than 80% of the prefix's findings in that category were dismissed as unimportant. The PR cites the dismissals (finding links and counts, never people's words), shows the findings the prior would have withheld in the last 90 days, and lists any accepted findings it would also have withheld, so the owner sees the trade. Security and `blocking` findings are never subject to a prior, and the schema refuses one.
- **Effect.** Merged priors apply in ranking (§6.10) and are recorded on each finding they touch; the owner digest shows priors in force under the owner's paths and what they withheld. A prior is config, so it expires and is removed like any config: the job proposes removal when a prefix has had no finding of that category in 180 days.
- **Scope.** Repo files only; a global prior is a rule change and goes through the rule's owner.

---
