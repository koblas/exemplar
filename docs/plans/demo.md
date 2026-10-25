# End-to-end demo

The design for a live demonstration of Exemplar on a sandbox GitHub org: what the audience sees, what produces each part, the data it needs and how it is triggered. It is the first time the whole system runs against real GitHub and a real model, so it doubles as the Phase 0 live runs (T-0.1, T-0.2; spec §16). Status is tracked in beads under the demo epic; this page holds the design and does not track progress.

The install workflow is used as it stands (setup page over a port-forward, show-and-apply Secret). Refining it is the `exemplar-ffj` epic and stays out of scope here.

<a id="1-what-the-demo-must-show"></a>

## 1. What the demo must show

The case in [`docs/concepts/why.md`](../concepts/why.md), live, in four beats. Each must happen on screen; none is narrated over a slide.

| Beat | The audience sees | Produced by | Needs |
|---|---|---|---|
| **A. Org-specific findings** | A PR that breaks two seeded org rules gets one review: inline comments with the rule id, Conventional Comments label, citation and a one-click suggestion; the check run lists coverage and says why a human is needed. A file in the same PR baits a plausible but wrong finding; the check run's held-back counts and the lanes board's refutation-by-rule panel show the verifier refuting it, so it was never posted (`/ui/` lists posted findings only). | webhook → worker (lanes 1–2, bug lane, verifier, poster) | Active rules in `exemplar-config` with eval cases; the PR set (§3) |
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

[`examples/demo/`](../../examples/demo/README.md) holds the kit and its runbook. A driver (`kit/`, a Go program, not part of the product) plays the three people through the GitHub API with their tokens, one phase at a time, each safe to re-run:

- **repo:** the sandbox repository's base tree, a small Go service with a CODEOWNERS file naming the rule owner and an `internal/auth/` path for `sensitive_paths`.
- **config:** the demo's exemplar-config, rendered from `examples/exemplar-config` (left as it is, since the repository's tests read it) with the repo file, the noisy demo rule `demo-todo-comments` and its eval case, and `global.yaml` with auto-approve on, the categorizer on two families, the cross-family verifier first, every head sampled for the baseline, and `verifier.importance_min` lowered to 0.3 so the deliberately noisy rule's findings post for people to reject (a real install keeps 0.5).
- **seed:** the history for beat C, merged before ingest first runs, since ingest reads merged or closed PRs and the first comment of each review thread, and its cursor does not revisit a PR. Two conventions, each stated by both reviewers on three PRs on `.go` files; the driver's tests run every comment through ingest's own filter.
- **prs:** beats A and B: the two-violation PR with its refutation bait, the README typo and the sensitive-path PR.
- **feedback:** beat D's volume. PRs with three TODOs each are opened until 30 of the demo rule's findings are marked wrong, by both reviewers in turn after each has reviewed the PR, so the feedback counts at reviewer weight.
- **teach:** the owner's *not important here* under `internal/legacy`.

The runbook gives the run order, starts each learning job on demand with `kubectl create job --from=cronjob/exemplar-learn-<job>` (extract twice: one pass submits the batch, the next collects it) and names the cluster context and namespace for every command.

<a id="4-code-the-demo-needs"></a>

## 4. Code the demo needs

What tracing each beat found missing from the code, each built as a beads issue the demo's live run depends on.

- **Embed job** (spec §8.3): nothing writes `extractions.embedding`, and `cluster` reads only rows that have one, so mining stops after extract. A Voyage route in the gateway and an embed job in `learning/` fill it.
- **Second-family provider** (spec §6.13, PRD FR-E10): `gates.ReviewConfidence` is 0 when both categorizers share a family, and every route in `internal/llm` reports `anthropic`, so nothing can be approved. An adapter for a second family behind the gateway, configured as `providers.categorizer` side B, unblocks Stage 2 and the cross-family verifier (`exemplar-bnt.7`).
- **Live baseline runs** (spec §6.3, item 10; T-0.2): `Engine.Baseline` is called only from `internal/replay`, and `baseline.sample_rate` is parsed and never read, so the baseline board has no live data. The worker samples production PRs and stores a `caller=baseline` review.
- **UI eFP flag** (spec §9.4): `/ui/` marks a rule over the bar at a hardcoded 0.30 while demotion uses `feedback.efp_bar` (0.10), so a rule can be demoted without the UI flagging it.
- **Duplicate check runs** (`exemplar-s83.5`): a retried review leaves an extra check run, and a killed attempt's stays `in_progress`, which a live demo shows on the PR.
