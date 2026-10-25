# End-to-end demo

The design for a live demonstration of Exemplar on a sandbox GitHub org: what the audience sees, what produces each part, the data it needs and how it is triggered. It is the first time the whole system runs against real GitHub and a real model, so it doubles as the Phase 0 live runs (T-0.1, T-0.2; spec §16). Status is tracked in beads under the demo epic; this page holds the design and does not track progress.

The install workflow is used as it stands (setup page over a port-forward, show-and-apply Secret). Refining it is the `exemplar-ffj` epic and stays out of scope here.

<a id="1-what-the-demo-must-show"></a>

## 1. What the demo must show

The case in [`docs/concepts/why.md`](../concepts/why.md), live, in four beats. Each must happen on screen; none is narrated over a slide.

| Beat | The audience sees | Produced by | Needs |
|---|---|---|---|
| **A. Org-specific findings** | A PR that breaks two seeded org rules gets one review: inline comments with the rule id, Conventional Comments label, citation and a one-click suggestion; the check run lists coverage and says why a human is needed. A second PR baits a plausible but wrong finding, and the finding page in `/ui/` shows the verifier refuting it, so it was never posted. | webhook → worker (lanes 1–2, bug lane, verifier, poster) | Active rules in `exemplar-config` with eval cases; the PR set (§3) |
| **B. Auto-approve vs route** | A README typo PR from an allowed author is approved by the App with no human. A PR touching `sensitive_paths` gets a check run naming the Stage 1 reasons it was routed to humans. | worker gates (spec §6.13), Stage 2 categorizer on two providers | `auto_approve` on globally and for the repo, CODEOWNERS covering the allowed paths, branch protection that lets a native approve count, **two model families** (§4) |
| **C. Mining with citations** | Seeded review history becomes a candidate rule PR in `exemplar-config` whose description cites the review comments it came from, routed to the CODEOWNERS owner. | `exemplar-learn` ingest → extract → **embed** → cluster → rank → propose | Seed history (§3) merged before ingest first runs; an embedding provider (§4) |
| **D. Feedback loop and boards** | A reviewer's `/exemplar not-important` reply shows on the finding page within a minute; the teach job opens a draft rule-edit PR narrowing `file_patterns`; the demote job drops a rule over the eFP bar to shadow by an auto-merged config PR; the boards show per-rule precision and Exemplar against the in-house baseline on the same PRs. | worker feedback commands; `exemplar-learn teach`, `demote`; materialized roll-ups; Grafana | One owner signal for teach; **30 judged findings** on one rule for demotion (spec §9.4 fixes the window); live baseline runs (§4) |

<a id="2-prerequisites"></a>

## 2. Prerequisites from outside the repository

- A sandbox GitHub org with the review App and the config-writer App installed (the manifest flow on `/setup` creates them). Branch protection on `exemplar-config` must let the config-writer App merge its own demotion PRs.
- **Three human accounts** in the org: an author and two reviewers, one of whom is the rule owner. Ingest drops bot comments and the PR author's own comments; demotion needs two distinct non-author participants; a reviewer only gets reviewer weight on a PR they took part in.
- An Anthropic key (generator, verifier, extract via Message Batches), a second-family provider key for the Stage 2 categorizer's B side (§4), and a Voyage key for embeddings (spec §8.3).
- A cluster: OrbStack's Kubernetes, with the in-cluster Postgres component and no `exemplar-search` (spec §6.4.6). Inbound through the Cloudflare tunnel (`deploy/k8s/base`), which needs a Cloudflare account and a hostname.
- Grafana is bring-your-own ([`docs/reference/dashboards.md`](../reference/dashboards.md)); the runbook installs it next to the release rather than adding it to the manifests.

<a id="3-the-demo-kit"></a>

## 3. The demo kit

Kept under `examples/demo/`, driven by `gh` with the three accounts' tokens, and safe to re-run against a fresh org:

- **Sandbox repository content:** a small Go service with a CODEOWNERS file, an `internal/auth/` path for `sensitive_paths`, and the rules of `examples/exemplar-config` applying to it.
- **Seed history** for beat C, pushed and merged before ingest first runs: ingest reads merged or closed PRs only, the first comment of each review thread, and its cursor does not revisit a PR, so comments added after a PR is mined are never read. At least two PRs and two reviewers per pattern, each comment 25 characters or more, stating the same constraint in close paraphrase on files of the same extension (the cluster job joins at cosine ≥ 0.65).
- **The PR set** for beats A and B: the two-violation PR, the refutation bait (modelled on `evals/_verifier/wrong-*`), the README typo, and the sensitive-path PR.
- **Feedback volume** for beat D: PRs that produce 30 judged findings on one demo rule, judged by the two reviewers with `/exemplar` commands so the weighted eFP clears `feedback.efp_bar` and the Wilson bound falls below `demotion_wilson_floor`.
- **A runbook:** the order of the run, and each learning job started on demand with `kubectl create job --from=cronjob/exemplar-learn-<job>` (extract twice: one pass submits the batch, the next collects it). It also names the cluster context and namespace for every command.

<a id="4-code-the-demo-needs"></a>

## 4. Code the demo needs

What the code does not do yet, found by tracing each beat. Each is a beads issue under the demo epic.

- **Embed job** (spec §8.3): nothing writes `extractions.embedding`, and `cluster` reads only rows that have one, so mining stops after extract. A Voyage route in the gateway and an embed job in `learning/` fill it.
- **Second-family provider** (spec §6.13, PRD FR-E10): `gates.ReviewConfidence` is 0 when both categorizers share a family, and every route in `internal/llm` reports `anthropic`, so nothing can be approved. An adapter for a second family behind the gateway, configured as `providers.categorizer` side B, unblocks Stage 2 and the cross-family verifier (`exemplar-bnt.7`).
- **Live baseline runs** (spec §6.3, item 10; T-0.2): `Engine.Baseline` is called only from `internal/replay`, and `baseline.sample_rate` is parsed and never read, so the baseline board has no live data. The worker samples production PRs and stores a `caller=baseline` review.
- **UI eFP flag** (spec §9.4): `/ui/` marks a rule over the bar at a hardcoded 0.30 while demotion uses `feedback.efp_bar` (0.10), so a rule can be demoted without the UI flagging it.
- **Duplicate check runs** (`exemplar-s83.5`): a retried review leaves an extra check run, and a killed attempt's stays `in_progress`, which a live demo shows on the PR.
