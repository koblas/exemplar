# Why Exemplar

Exemplar has one goal: **give code reviewers their time back without lowering the bar.** AI made code cheap to write. It did nothing to make code cheaper to review. Pull requests (PRs) now show up faster than the people who have to read them. A team has two bad ways to cope: review less carefully, or merge less often. Exemplar adds a third: keep the review standard where it is, and spend human attention only on the PRs that need a human. This page makes the case for the product. The [product requirements](https://github.com/koblas/exemplar/blob/main/docs/spec/prd.md) are the long version.

## The problem: review is the bottleneck now

Writing code with AI scales with how many tokens you can buy. Reviewing code scales with how many people you have. Since coding assistants and agents became normal, several groups have measured the gap between the two:

- Teams that use AI heavily merge about twice as many PRs, and spend about twice as long reviewing each one. Faros AI measured more than 10,000 developers and found 98% more PRs merged, 91% more review time, and PRs about half again as large.[^faros]
- Four in five developers say they spend more time in code review since their team adopted AI tools. More than half say reviewing AI-generated code is the biggest source of friction in their work (Harness, 700 practitioners, 2026).[^harness]
- DORA's 2025 research finds that AI adoption now raises how much teams ship, but still raises instability too: more changes fail, more work gets redone.[^dora]
- Under that load, review itself gets worse. A 2026 study of humans reviewing agent-written PRs found approvals going up while scrutiny went down: PRs waited longer in the queue and got less inspection.[^habituation] Telemetry shows a growing share of PRs merged with no review at all.[^faros]
- Open source hit the wall first. In 2026 GitHub shipped controls that let maintainers close or cap incoming PRs, because agent-generated submissions were eating the review time projects had.[^github]

!!! note "About these numbers"
    Most come from vendors of engineering-metrics or review tooling, and from surveys. Treat the sizes as rough. The direction is the same in every source, and it matches what Exemplar's own counter-metrics watch for.

The result is a review bottleneck with two faces:

1. **Standards slip.** Reviewers who cannot read everything start approving on trust. The judgment that made review valuable stops being applied: years of comments about *this* codebase's conventions, security patterns, painful migrations and lessons from incidents. Coding agents never had that judgment in the first place, so the same mistakes get generated, reviewed and fixed over and over.
2. **Capacity runs out.** Every coding tool is capped by how fast the humans supervising it can read. Senior engineers spend their days reading instead of designing, and the queue still grows.

Generic AI reviewers do not fix either problem. They know the public internet, not your organization. They leave comments that are correct but irrelevant. Engineers learn to ignore them within a week, and that trust is expensive to win back. The talk that inspired Exemplar measured a generic bot at roughly one in five findings accepted. That is worse than no bot at all, because it trains people to skip the bot's comments.

## Two jobs: hold the standard, absorb the volume

Exemplar is an AI code reviewer that **learns your organization's own review judgment**, applies it to every PR, and **decides which PRs still need a human**. It also corrects itself when people tell it a rule is wrong.

**Hold the standard.**

1. **It mines your review history.** A set of background jobs (the *learning plane*) reads years of human review comments, plus postmortems and reverts. It pulls out the comments that express a reusable constraint, groups similar ones together, and proposes candidate rules. Each candidate cites the comments it came from.
2. **A human decides what becomes a rule.** Candidates arrive as PRs to a config repository your teams own, called `exemplar-config`. Rule owners edit, test and merge them, or reject them. Nothing is enforced until a human has merged it, and every rule ships with test cases.
3. **It enforces with one focused agent per rule.** Rules that can be written as a code pattern run as plain pattern matches, with no model involved. The rest each get a small AI agent with read-only tools and context from across the repository. A narrow "bug lane" looks for logic errors no rule describes.
4. **Nothing reaches a human unverified.** Every candidate finding goes to a second model, from a different model family where possible. That model's job is to disprove the finding and judge whether it matters here. Code then checks the evidence, the line numbers and where the finding came from. What survives is ranked, trimmed to a budget and posted, with a one-click fix where the change is small and local.

**Absorb the volume.**

5. **Every PR gets a verdict: approved, or routed to humans with reasons.** A PR is approved with no human review when it touched only allowed paths, came from an allowed author, was reviewed completely with no blocking, important or Security finding, passed a merge-safety score computed by code, and was rated low-risk by two models. Everything else goes to human review, and the record says which gate it failed. Reviewers open the PR already knowing why it needs them. The gates are deterministic (the same input always gives the same answer), auditable, and off until you turn them on per repository.
6. **It learns from what people say back.** Structured dismissals ("wrong", "not here", "already covered") feed each rule's precision score. A rule people keep rejecting is demoted. A correction in a reply opens a proposal to edit the rule, which the owner reviews through the same gate.
7. **The same rules reach the code before the PR.** Rules are exported into the instruction files that coding agents read, and into a local check you can run before opening a PR. Agents and IDEs follow the rules before review, so those violations never show up as review load at all.

The lasting asset is the collection of rules: owned, versioned, cited and testable. The models that read it are swappable parts behind a gateway.

## How it differs

| A generic AI reviewer | Exemplar |
|---|---|
| Knows the public internet | Knows what your reviewers have said, with citations |
| One large prompt reviews everything | One agent per rule, plain pattern matching where possible, plus a narrow bug lane |
| Posts what the model says | Posts only what a second model failed to refute and code could validate |
| Adds comments to every PR; the human queue stays the same length | Approves low-risk PRs without a human, behind deterministic gates, and tells reviewers why the rest need them |
| Learns from thumbs up and down, directly into its behavior | Learns through a config PR that a human merges; it never changes its own enforcement |
| Comments on everything it notices | Has a comment budget per PR and per file; small style nits belong to linters and are deduplicated |
| Can block merges | Never requests changes. It approves, or it says nothing |
| Works at review time only | The same rules ship to coding agents, IDEs and a pre-PR command-line tool |

There is a tempting shortcut that Exemplar is built to replace. When a team is overwhelmed, engineers start reviewing PRs with their own coding assistant: paste the diff in, post what comes back. It looks like help. In practice the assistant leaves many comments, some valid and some not. Nobody can tell which ones the human reviewer actually agrees with. Authors spend their time disproving them, and soon authors use an assistant to answer the assistant. Review turns into an exchange between two tools that spreads no knowledge and needs no teammate. The alternative is one central reviewer whose findings someone owns, whose precision is measured per rule, and whose approval is a plain, auditable decision. That is what owned rules, verification, structured feedback and deterministic gates are for.

## Principles

- **Precision is the product.** A noisy rule is worse than no rule. Every design choice traces back to reducing one of two kinds of false positive: findings that are wrong, and findings that are right but don't matter here.
- **Human attention is the scarce resource.** The point of approving a PR without a human is not speed. It is that the reviewer's hour goes to the PR that needs it.
- **Learn offline, enforce online, one human gate between them.** Cheap vector math filters first, a model decides, a human merges.
- **Deterministic first, expensive only if everything passes.** A check that can be a pattern is a pattern. An approval is decided by gates that code can audit, never by a model's word.
- **The model never vouches for itself.** A different model tries to disprove each finding, and code checks the evidence. Everything the model reads, including the diff and the PR description, is treated as untrusted.
- **A fix is worth more than a comment.**
- **Best practices expire.** Every rule carries a review-by date and drops back to shadow mode unless someone re-confirms it.
- **Review is also knowledge transfer.** Counter-metrics watch for auto-approve eroding shared ownership of the code.
- **An auto-approved failure is a system failure.** When a PR Exemplar approved causes an incident or a revert, the question is which gate, rule or check should have refused it, never which author to blame. The fix lands in config, a rule, a test case or the code.
- **Cost and latency are visible, not optimized for.** Every review reports what it cost. Precision and encoded judgment come first.

## What it won't do

- Replace human review of design, or block a PR. The bot never posts "request changes".
- Approve anything a model alone judged safe. Approval requires every deterministic gate to pass first. The two-model risk check can only say no.
- Comment on naming, formatting or style trivia. Linters own those, and findings that duplicate a linter are dropped.
- Train or fine-tune a model, or depend on a third-party AI reviewer.
- Change its own behavior without a merged, human-approved config change. A bot that optimizes directly for what people react well to learns to say what people like to hear.
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

These are the targets Exemplar is built against, from the product requirements:

| Metric | Target |
|---|---|
| Findings accepted by authors | at least 65%, at or above human reviewers |
| Effective false-positive rate per rule | at most 10% for every active rule |
| Precision against an in-house generic baseline reviewer | at least double |
| Accepted findings that ship with a fix the author applied | at least a quarter |
| Pull requests merged fully AI-reviewed, with no human review needed | 5% at launch, then growing |
| Violations per PR for exported rules | falling quarter over quarter |

Alongside them, counter-metrics watch for harm: the revert rate of auto-approved PRs compared with human-approved ones, incidents on PRs where the bot was silent, code-owner paths changed without that team seeing the PR, and human design comments falling while acceptance stays flat. Cost and latency are reported on every review but are not targets.

## Where to go next

- [How a review works](how-a-review-works.md): what happens to a PR, and how the verdict is reached.
- [Architecture](architecture.md): how the pieces fit, as built.
- [Quickstart](../guides/install/quickstart.md): a trial in about 20 minutes, then [Your first review](../tutorials/first-review.md): from shadow mode to posted reviews.
- [How to turn on auto-approve](../guides/operate/auto-approve.md): letting Exemplar approve low-risk PRs.

[^faros]: Faros AI, [How AI-Generated Code Is Increasing Code Review Burden](https://www.faros.ai/blog/ai-code-quality-senior-engineer-review-burden), telemetry from more than 10,000 developers across 1,255 teams.
[^harness]: Harness, *State of Engineering Excellence 2026*, reported in [The Review Tax](https://dev.to/harsh2644/the-review-tax-why-81-of-developers-are-buried-in-ai-code-review-9k6) and [Computer Weekly](https://www.computerweekly.com/news/366643082/Software-developers-shift-to-AI-code-reviewers).
[^dora]: DORA, [State of AI-assisted Software Development 2025](https://dora.dev/insights/balancing-ai-tensions/).
[^habituation]: [Habituation at the Gate: Rising Approval and Declining Scrutiny in Human Review of AI Agent Code](https://arxiv.org/pdf/2606.22721), 2026.
[^github]: [The Register](https://www.theregister.com/software/2026/02/03/github-ponders-kill-switch-for-pull-requests-to-stop-ai-slop/4334869) and [The New Stack](https://thenewstack.io/ai-generated-code-crisis/) on GitHub's 2026 pull-request controls.
