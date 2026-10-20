# Why Exemplar

Exemplar exists for one reason: **to give your reviewers their time back without lowering the bar.** AI made code cheap to write and did nothing for the cost of reviewing it. Pull requests now arrive faster than the people who must read them, and the only ways a team has to cope are to review less carefully or to merge less often. Exemplar gives it a third option: hold the review standard, and put human attention only where a human is needed. This page is the case for the product; the [product requirements](https://github.com/koblas/exemplar/blob/main/docs/spec/prd.md) are the long form.

## The problem: review is the bottleneck now

Code generation scales with tokens. Review scales with headcount. Since coding assistants and agents became routine, the gap between the two has been measured from several directions:

- Teams with high AI adoption merge about twice as many pull requests, and spend about twice as long in review per PR. Faros AI's telemetry across more than 10,000 developers put the figures at 98% more PRs merged and 91% more review time, with PRs half again as large.[^faros]
- Four in five developers say they spend more time in code review since their team adopted AI tools, and more than half name reviewing AI-generated code as the biggest source of friction in their work (Harness, 700 practitioners, 2026).[^harness]
- DORA's 2025 research finds AI adoption now raises delivery throughput, and still raises instability: more change failures, more rework.[^dora]
- Under that load, review itself degrades. A 2026 study of human review of agent-authored PRs found approvals rising while scrutiny fell: more time in the queue, less time inspecting.[^habituation] Telemetry shows a growing share of PRs merged with no review at all.[^faros]
- Open source hit the wall first. In 2026 GitHub shipped controls that let maintainers close or cap incoming pull requests, because agent-generated submissions were consuming the review time projects had.[^github]

!!! note "About these numbers"
    Most come from vendors of engineering-metrics or review tooling, and from surveys. Treat the magnitudes as indicative. The direction is the same in every source, and it matches what Exemplar's own counter-metrics are built to watch.

The result is a review bottleneck with two faces:

1. **Standards slip.** Reviewers who cannot read everything start approving on trust, and the judgment that made review valuable, years of comments about *this* codebase's conventions, security patterns, migration scars and incident lessons, stops being applied. Coding agents never had it, so the same violations are generated, reviewed and fixed over and over.
2. **Capacity runs out.** Every coding tool is capped at the throughput of the humans supervising it. Senior engineers spend their days reading rather than designing, and the queue still grows.

Generic AI reviewers do not fix either. They know the public internet, not your organization. They produce correct-but-irrelevant comments, engineers learn to ignore them within a week, and that trust is expensive to win back. The talk that motivated Exemplar measured a generic bot at roughly a fifth of its findings accepted, which is worse than no bot, because it trains people to skip the bot's comments.

## Two jobs: hold the standard, absorb the volume

Exemplar is a self-correcting AI code reviewer that **encodes your organization's own review judgment**, enforces it on every pull request, and **decides which pull requests still need a human**.

**Hold the standard.**

1. **It mines your review history.** The learning plane reads years of human review comments (and postmortems and reverts), extracts the ones that express a reusable constraint, clusters them, and proposes candidate rules with citations back to the comments they came from.
2. **A human decides what becomes a rule.** Candidates arrive as pull requests to a config repository your teams own (`exemplar-config`). Rule owners edit, test and merge them, or kill them. Nothing enforces until a human merged it, and every rule ships with evaluation cases.
3. **It enforces with one focused agent per rule.** Rules that can be expressed as patterns run deterministically. The rest each get a small agent with read-only tools and repository-wide context. A narrow bug lane looks for the logic errors no rule describes.
4. **Nothing reaches a human unverified.** Every candidate finding goes to a second model, from a different family where possible, whose job is to disprove it and to judge whether it matters here. Code then checks the evidence, the anchors and the provenance. What survives is ranked, budgeted and posted, with a one-click fix where the change is local.

**Absorb the volume.**

5. **Every PR gets a verdict: approved, or routed to humans with reasons.** A pull request that touched only allowed paths, came from an allowed author, was reviewed to full coverage with zero findings, and passed a deterministic merge-safety score and a two-model risk categorisation is approved without a human. Everything else goes to human review, and the record says which gate it failed, so reviewers open the PR knowing why it needs them. The gates are deterministic, auditable, and off until you turn them on per repository.
6. **It learns from what people say back.** Structured dismissals ("wrong", "not here", "already covered") feed per-rule precision. A rule people keep rejecting is demoted. A correction in a reply opens a rule-edit proposal for the owner, through the same gate.
7. **The same rules reach the code before the PR.** Rules export to coding-agent instruction files and a local pre-PR check, so agents and IDEs conform before review, and the violations never arrive as review load at all.

The durable asset is the rule corpus: owned, versioned, cited and testable. The models that read it are swappable components behind a gateway.

## How it differs

| A generic AI reviewer | Exemplar |
|---|---|
| Knows the public internet | Knows what your reviewers have said, with citations |
| One large prompt reviews everything | One agent per rule, deterministic where possible, plus a narrow bug lane |
| Posts what the model says | Posts only what a second model failed to refute and code could validate |
| Adds comments to every PR; the human queue stays the same length | Approves low-risk PRs without a human behind deterministic gates, and tells reviewers why the rest need them |
| Learns from thumbs up and down, directly into behavior | Learns through a config PR a human merges; it never changes its own enforcement |
| Comments on everything it notices | Comment budget per PR and per file; nits belong to linters and are deduplicated |
| Can block merges | Never requests changes. It approves, or it says nothing |
| Review-time only | The same rules ship to coding agents, IDEs and a pre-PR CLI |

## Principles

- **Precision is the product.** A noisy rule is worse than no rule. Every design choice traces to reducing one of two false positives: findings that are wrong, and findings that are right but don't matter here.
- **Human attention is the scarce resource.** The point of approving a PR without a human is not speed; it is that the reviewer's hour goes to the PR that needs it.
- **Learn offline, enforce online, one human gate between them.** Cheap vectors prefilter, a model decides, a human merges.
- **Deterministic first, expensive only if it all passes.** A check that can be a pattern is a pattern. An approval is decided by gates code can audit, never by a model's say-so.
- **The model never vouches for itself.** A different model tries to disprove each finding; code checks the evidence. Everything the model reads, including the diff and the PR description, is untrusted.
- **A fix is worth more than a comment.**
- **Best practices expire.** Rules carry a review-by date and drop back to shadow unless re-confirmed.
- **Review is also knowledge transfer.** Counter-metrics watch for auto-approve eroding shared ownership.
- **Cost and latency are visible, not optimized for.** Every review reports what it cost; precision and encoded judgment come first.

## What it won't do

- Replace human review of design, or block a PR. The bot never posts "request changes".
- Approve anything a model alone judged safe. Approval requires every deterministic gate to pass first; the two-model categorisation can only say no.
- Comment on naming, formatting or style trivia. Linters own those, and findings that duplicate them are dropped.
- Train or fine-tune a model, or depend on a third-party AI reviewer.
- Change its own behavior without a merged, human-approved config change. Optimizing directly for what people react well to invites a bot that learns to say what people like to hear.
- Send code it has been told not to send. Repositories and paths can be excluded from ever reaching a model provider, and the gateway is the only process holding provider keys.

## Who it's for

| Persona | What they get |
|---|---|
| **PR authors** | Relevant, cited findings with fixes; low-risk changes merged without waiting on a reviewer; the same checks locally and in their coding agent before pushing |
| **Human reviewers** | A shorter queue, each PR in it labelled with why it needs a human, and time for design instead of repeat-pattern review |
| **Rule owners** | Mined candidates with citations, a playground to test them, per-rule precision and dismissal reasons, a weekly digest |
| **Platform teams** | One Kubernetes install, config as code, cost by lane and caller, kill switches at every level |
| **Security and compliance** | Approval policies the bot can't be prompt-injected past, provenance on every finding, control over which repos and paths leave the org |
| **Coding agents** | Path-scoped rules in their instruction files and a pre-PR check they can call |

## How it knows it's working

The targets Exemplar is built against, from the product requirements:

| Metric | Target |
|---|---|
| Findings accepted by authors | at least 65%, at or above human reviewers |
| Effective false-positive rate per rule | at most 10% for every active rule |
| Precision against an in-house generic baseline reviewer | at least double |
| Accepted findings that ship with a fix the author applied | at least a quarter |
| Pull requests merged fully AI-reviewed, with no human review needed | 5% at launch, then growing |
| Violations per PR for exported rules | falling quarter over quarter |

Alongside them, counter-metrics watch for harm: the revert rate of auto-approved PRs against human-approved ones, incidents on PRs where the bot was silent, code-owner paths changed without that team seeing the PR, and human design comments falling while acceptance stays flat. Cost and latency are reported on every review but are not targets.

## Where to go next

- [How a review works](how-a-review-works.md): what happens to a PR, and how the verdict is reached.
- [Architecture](architecture.md): how the pieces fit, as built.
- [Quickstart](../tutorials/quickstart.md): a trial in about 20 minutes, then [Your first review](../tutorials/first-review.md): from shadow mode to posted reviews.
- [How to turn on auto-approve](../guides/operate/auto-approve.md): letting Exemplar approve low-risk PRs.

[^faros]: Faros AI, [How AI-Generated Code Is Increasing Code Review Burden](https://www.faros.ai/blog/ai-code-quality-senior-engineer-review-burden), telemetry from more than 10,000 developers across 1,255 teams.
[^harness]: Harness, *State of Engineering Excellence 2026*, reported in [The Review Tax](https://dev.to/harsh2644/the-review-tax-why-81-of-developers-are-buried-in-ai-code-review-9k6) and [Computer Weekly](https://www.computerweekly.com/news/366643082/Software-developers-shift-to-AI-code-reviewers).
[^dora]: DORA, [State of AI-assisted Software Development 2025](https://dora.dev/insights/balancing-ai-tensions/).
[^habituation]: [Habituation at the Gate: Rising Approval and Declining Scrutiny in Human Review of AI Agent Code](https://arxiv.org/pdf/2606.22721), 2026.
[^github]: [The Register](https://www.theregister.com/software/2026/02/03/github-ponders-kill-switch-for-pull-requests-to-stop-ai-slop/4334869) and [The New Stack](https://thenewstack.io/ai-generated-code-crisis/) on GitHub's 2026 pull-request controls.
