# Why Exemplar

Exemplar exists because AI made code cheap to write and did nothing for the cost of reviewing it. This page is the case for the product: the problem it takes on, what it is and isn't, the principles it is built on, and how it knows whether it is working. The [product requirements document](https://github.com/koblas/exemplar/blob/main/docs/spec/prd.md) in the repository is the long form.

## The problem

Code generation has multiplied what each engineer produces, but time-to-production has barely moved. The bottleneck migrated from writing code to reviewing it:

- Senior engineers spend their days reading rather than designing.
- Review capacity grows with headcount; generation grows with tokens. Every coding tool is capped at the throughput of the humans supervising it.
- The judgment that makes a review good lives in years of past review comments, not in any style guide, and style guides go stale within weeks.
- Coding agents write code without that judgment, so the same violations are generated, reviewed and fixed over and over.

Generic AI reviewers don't fix this. They know the public internet, not your organization: its conventions, security patterns, infrastructure assumptions, migration scars, incident lessons and ownership. They produce correct-but-irrelevant comments, engineers learn to ignore them within a week, and that trust is expensive to win back. The talk that motivated Exemplar measured a generic bot at roughly a fifth of its findings accepted, which is worse than no bot, because it trains people to skip the bot's comments.

## What Exemplar is

Exemplar is a self-correcting AI code reviewer that **encodes your organization's own review judgment** and enforces it on every pull request.

1. **It mines your review history.** The learning plane reads years of human review comments (and postmortems and reverts), extracts the ones that express a reusable constraint, clusters them, and proposes candidate rules with citations back to the comments they came from.
2. **A human decides what becomes a rule.** Candidates arrive as pull requests to a config repository your teams own (`exemplar-config`). Rule owners edit, test and merge them, or kill them. Nothing enforces until a human merged it, and every rule ships with evaluation cases.
3. **It enforces with one focused agent per rule.** Rules that can be expressed as patterns run deterministically. The rest each get a small agent with read-only tools and repository-wide context. A narrow bug lane looks for the logic errors no rule describes.
4. **Nothing reaches a human unverified.** Every candidate finding goes to a second model, from a different family where possible, whose job is to disprove it and to judge whether it matters here. Code then checks the evidence, the anchors and the provenance. What survives is ranked, budgeted and posted, with a one-click fix where the change is local.
5. **It learns from what people say back.** Structured dismissals ("wrong", "not here", "already covered") feed per-rule precision. A rule people keep rejecting is demoted. A correction in a reply opens a rule-edit proposal for the owner, through the same gate.
6. **The same rules reach the code before the PR.** Rules export to coding-agent instruction files and a local pre-PR check, so agents and IDEs conform before review, and findings per PR fall for a reason other than suppression.

The durable asset is the rule corpus: owned, versioned, cited and testable. The models that read it are swappable components behind a gateway.

## How it differs

| A generic AI reviewer | Exemplar |
|---|---|
| Knows the public internet | Knows what your reviewers have said, with citations |
| One large prompt reviews everything | One agent per rule, deterministic where possible, plus a narrow bug lane |
| Posts what the model says | Posts only what a second model failed to refute and code could validate |
| Learns from thumbs up and down, directly into behavior | Learns through a config PR a human merges; it never changes its own enforcement |
| Comments on everything it notices | Comment budget per PR and per file; nits belong to linters and are deduplicated |
| Can block merges | Never requests changes. It approves, or it says nothing, and it can auto-approve a narrow, gated class of low-risk PRs |
| Review-time only | The same rules ship to coding agents, IDEs and a pre-PR CLI |

## Principles

- **Precision is the product.** A noisy rule is worse than no rule. Every design choice traces to reducing one of two false positives: findings that are wrong, and findings that are right but don't matter here.
- **Learn offline, enforce online, one human gate between them.** Cheap vectors prefilter, a model decides, a human merges.
- **Deterministic first, expensive only if it all passes.** A check that can be a pattern is a pattern.
- **The model never vouches for itself.** A different model tries to disprove each finding; code checks the evidence. Everything the model reads, including the diff and the PR description, is untrusted.
- **A fix is worth more than a comment.**
- **Best practices expire.** Rules carry a review-by date and drop back to shadow unless re-confirmed.
- **Review is also knowledge transfer.** Counter-metrics watch for auto-approve eroding shared ownership.
- **Cost and latency are visible, not optimized for.** Every review reports what it cost; precision and encoded judgment come first.

## What it won't do

- Replace human review of design, or block a PR. The bot never posts "request changes".
- Comment on naming, formatting or style trivia. Linters own those, and findings that duplicate them are dropped.
- Train or fine-tune a model, or depend on a third-party AI reviewer.
- Change its own behavior without a merged, human-approved config change. Optimizing directly for what people react well to invites a bot that learns to say what people like to hear.
- Send code it has been told not to send. Repositories and paths can be excluded from ever reaching a model provider, and the gateway is the only process holding provider keys.

## Who it's for

| Persona | What they get |
|---|---|
| **PR authors** | Relevant, cited findings with fixes; fast merges for low-risk changes; the same checks locally and in their coding agent before pushing |
| **Human reviewers** | Less mechanical and repeat-pattern review; time for design |
| **Rule owners** | Mined candidates with citations, a playground to test them, per-rule precision and dismissal reasons, a weekly digest |
| **Platform teams** | One Kubernetes install, config as code, cost by lane and caller, kill switches at every level |
| **Security and compliance** | Approval policies the bot can't be prompt-injected past, provenance on every finding, control over which repos and paths leave the org |
| **Coding agents** | Path-scoped rules in their instruction files and a pre-PR check they can call |

## How it knows it's working

The targets Exemplar is built against:

| Metric | Target |
|---|---|
| Findings accepted by authors | at least 65%, at or above human reviewers |
| Effective false-positive rate per rule | at most 10% for every active rule |
| Precision against an in-house generic baseline reviewer | at least double |
| Accepted findings that ship with a fix the author applied | at least a quarter |
| Pull requests merged fully AI-reviewed | 5% at launch, then growing |
| Violations per PR for exported rules | falling quarter over quarter |

Alongside them, counter-metrics watch for harm: the revert rate of auto-approved PRs, incidents on PRs where the bot was silent, code-owner paths changed without that team seeing the PR, and human design comments falling while acceptance stays flat. Cost and latency are reported on every review but are not targets.

## Where to go next

- [Architecture](architecture.md): how the pieces fit, as built.
- [Quickstart](../tutorials/quickstart.md): a trial in about 20 minutes.
- [Your first review](../tutorials/first-review.md): from shadow mode to posted reviews.
