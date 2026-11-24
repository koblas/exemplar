<a id="6-functional-requirements-learning-plane-offline"></a>

## 6. Functional Requirements — Learning Plane (offline)

<a id="61-mine-fr-l1"></a>

### 6.1 Mine (FR-L1)

| ID | Requirement |
|---|---|
| FR-L1.1 | Ingest top-level human review comments over a configurable lookback (default **750 days** ). |
| FR-L1.2 | Also ingest the system's own findings that authors **accepted** , down-weighted relative to human comments. |
| FR-L1.3 | Store each comment with repo, path, extension, diff hunk, author, reviewer, timestamp, thread resolution, reactions, and **any URLs it cites** (style guides, docs, incident reports). |
| FR-L1.4 | Exclude the system's own seeded reactions from all feedback signals. |
| FR-L1.5 | Support backfill at the scale of ~700K PRs / ~1.7M comments over 3 years. |
| FR-L1.6 | **Doc-linked comments are high-value seeds.** Comments that link to internal documentation are ranked higher and the link becomes the rule's primary citation. *(Borrowed: Google AutoCommenter trained on comments containing best-practice URLs.)* |
| FR-L1.7 | Also mine **incident postmortems and revert commits** : PRs later reverted or linked to a SEV are a source of candidate Blocker rules even when no review comment caught them. *(v0.9)* These are a **co-equal source**, not a supplement: a candidate from one incident ranks with a comment cluster of ten (FR-L4.1), because comments record what reviewers caught and incidents record what they missed. |
| FR-L1.8 | **Owner-written seeds** — *v0.9*. A rule owner may write a candidate by hand (a one-line constraint and the paths it applies to) in the playground or the config repo. It enters the same pipeline as a mined candidate: eval cases before merge (FR-L6.4), volume estimate (FR-P2), shadow first (FR-X4), and the same feedback and demotion lifecycle. Its citation is the owner, with any doc they link. |
| FR-L1.9 (not built: #275) | **Merged-change clusters** — *v0.9*. A periodic job clusters recently merged changes (by area, by what the bug lane flagged, by what later reverted) and asks what deterministic check or rule would have caught the shared pattern, opening candidates the way FR-L6 does. As review volume rises and comments per PR fall, changes carry judgment that comments no longer do. *(Borrowed: Larson's `review-pr-cluster`, which reviews batches of PRs for themes and new verifications.)* |

<a id="62-extract-and-embed-fr-l2"></a>

### 6.2 Extract and embed (FR-L2)

| ID | Requirement |
|---|---|
| FR-L2.1 | An LLM processes comments in **batches of three** , deciding whether each expresses a reusable review constraint versus praise, typos, or one-off remarks. |
| FR-L2.2 | Tune the extractor to accept roughly **10–15%** of comments. |
| FR-L2.3 | For each accepted comment, emit a draft `{id, description, body}` and embed it. |
| FR-L2.4 | Extraction is **idempotent** : results are cached by content hash so re-running over the same comments does not re-call the model or produce duplicate candidates. (LLM output is not deterministic; the pipeline is.) A failed batch is retried and never silently drops a candidate. |

<a id="63-cluster-and-merge-fr-l3"></a>

### 6.3 Cluster and merge (FR-L3)

| ID | Requirement |
|---|---|
| FR-L3.1 | Compare candidates sharing the same file extension by default. The extractor tags candidates that are **language-agnostic** (e.g. "every outbound RPC sets a timeout", logging, secrets handling); those cluster across extensions so one org-wide rule is not fragmented into one rule per language. |
| FR-L3.2 | Cosine similarity is a cheap prefilter (≥ 0.65 for clustering, ≥ 0.35 to propose merges; both configurable). |
| FR-L3.3 | An LLM cohesion check makes the final merge decision. |
| FR-L3.4 | Merged candidates retain citations to every contributing source comment. |

<a id="64-rank-and-route-fr-l4"></a>

### 6.4 Rank and route (FR-L4)

| ID | Requirement |
|---|---|
| FR-L4.1 | Rank by cluster size, then **distinct reviewers** , with boosts for doc-linked, incident-linked, and resolved-thread sources. |
| FR-L4.2 | Route candidates to the likely owner using CODEOWNERS of the source files. |
| FR-L4.3 | **Suppress candidates already covered** by an existing linter, formatter or static-analysis rule (match by description and a trial run on the source PRs). |

<a id="65-compile-to-deterministic-where-possible-fr-l5-new"></a>

### 6.5 Compile to deterministic where possible (FR-L5) — *new*

*(Borrowed: Google found ~two thirds of learned best practices were beyond traditional static analysis — which means roughly a third were within reach of it.)*

| ID | Requirement |
|---|---|
| FR-L5.1 | For every candidate, attempt to synthesize a deterministic rule (Semgrep or ast-grep pattern; CodeQL query for security dataflow cases) with an LLM, then evaluate it on the candidate's eval set. |
| FR-L5.2 | If the compiled rule meets the LLM rule's precision and ≥ 90% of its recall on the eval set, the candidate is proposed as a **compiled rule** (Lane 1). Otherwise it remains an exemplar (Lane 2). |
| FR-L5.3 | Compiled rules carry the same metadata, citations, ownership, feedback and demotion lifecycle as exemplars. |
| FR-L5.4 | Active exemplars are periodically re-attempted for compilation as patterns stabilize. |

<a id="66-human-gate-fr-l6"></a>

### 6.6 Human gate (FR-L6)

| ID | Requirement |
|---|---|
| FR-L6.1 | Owners review ranked candidates and can **edit, test in the playground, promote, or kill** . |
| FR-L6.2 | Promotion opens a **config PR** . Nothing enforces until it merges. |
| FR-L6.3 | A kill is recorded with a reason and becomes negative signal for future extraction. |
| FR-L6.4 | Every rule ships with eval cases (§11) before merge; CI blocks merges without them. |
| FR-L6.5 | **"Teach the bot" fast path.** When an engineer replies to a finding with a correction ("@exemplar that's wrong for us because…"), the system immediately opens either a suppression proposal or a rule-edit PR for the owner, rather than waiting for the next mining run. *(Borrowed from the reply-to-learn interaction in commercial reviewers, kept behind our human gate.)* |
