---
title: Exemplar
hide:
  - navigation
  - toc
---

<div class="landing" markdown>

<div class="landing-hero" markdown>

<p class="landing-kicker">A self-correcting AI code reviewer for GitHub</p>

# Give your reviewers their time back. {: .landing-title }

<p class="landing-lead">AI-assisted coding doubled the pull requests your team produces and did nothing for the people who review them. Exemplar reviews every PR with <strong>your organization's own judgment</strong>, approves the low-risk ones with <strong>no human review needed</strong>, and sends the rest to people <strong>with the reasons</strong>.</p>

[Try it: a 20-minute trial install](guides/install/quickstart.md){ .md-button .md-button--primary }
[Why Exemplar](concepts/why.md){ .md-button }

<p class="landing-note">Open source. Runs on your Kubernetes, in one namespace, against one Postgres, as a GitHub App. Code never leaves the paths you allow.</p>

</div>

<div class="landing-problem" markdown>

<div class="landing-stat" markdown>
<span class="landing-stat-n">2×</span>
<span class="landing-stat-l">the pull requests merged by teams with high AI adoption</span>
</div>
<div class="landing-stat" markdown>
<span class="landing-stat-n">2×</span>
<span class="landing-stat-l">the time each of them spends in review</span>
</div>
<div class="landing-stat" markdown>
<span class="landing-stat-n">4 in 5</span>
<span class="landing-stat-l">developers say review takes more of their day since AI tools arrived</span>
</div>

<p class="landing-caption">Human review is the bottleneck now, and under that load it degrades: more approvals, less scrutiny. <a href="concepts/why/#the-problem-review-is-the-bottleneck-now">The numbers and their sources.</a></p>

</div>

## One verdict on every pull request {: .landing-h2 }

<p class="landing-sub">Exemplar does not add comments to a queue that is already too long. It decides who needs to read each PR, and shows its work.</p>

<div class="grid cards landing-verdicts" markdown>

-   :material-check-decagram: **Approved, no human review needed**

    ---

    The PR touched only allowed paths, came from an allowed author, was reviewed to full coverage with no blocking, important or Security finding (suggestions may post beside the approval), and passed a deterministic merge-safety score and a two-model risk check. It merges without waiting on a reviewer.

-   :material-account-arrow-right: **Routed to humans, with the reasons**

    ---

    Everything else goes to your reviewers. The check run says why in plain words: a finding, a denylisted path, a score below the bar. Reviewers open the PR already knowing what to look for.

</div>

## Built on five guarantees {: .landing-h2 }

<div class="grid cards landing-guarantees" markdown>

-   **Your judgment, not the internet's**

    ---

    Rules are mined from your own review history, with citations, and live in a git repository your teams own. Nothing enforces until a rule owner merges it.

-   **Nothing reaches a human unverified**

    ---

    A second model, from a different family, tries to refute every finding. Code checks the anchors, the evidence and the provenance. What survives is budgeted and carries a one-click fix.

-   **Approval is never a model's say-so**

    ---

    Every gate that can approve a PR is deterministic and auditable. The two-model risk check can only withhold an approval, never grant one on its own.

-   **It never blocks**

    ---

    Exemplar approves or stays silent. It never requests changes, and a kill switch at every level is one config merge away.

-   **Violations stop arriving**

    ---

    The same rules export to coding-agent instruction files and a pre-PR check, so agents and IDEs conform before the PR exists.

</div>

## One pass per pull request {: .landing-h2 }

```mermaid
flowchart LR
  PR["Pull request"] --> R["Your rules<br/>one agent per rule"]
  R --> V["Verify<br/>second model refutes"]
  V --> C["Validate<br/>code checks evidence"]
  C --> D{"Every gate<br/>passed?"}
  D -- yes --> A["Approved<br/>no human needed"]
  D -- no --> H["Humans<br/>with the reasons"]
```

<p class="landing-caption">Deterministic first, models only where judgment is needed, a human gate on every rule change. <a href="concepts/how-a-review-works/">How a review works.</a></p>

## What it is built to hit {: .landing-h2 }

<p class="landing-sub">The targets in the product requirements, with counter-metrics that watch for auto-approve eroding shared ownership. Cost and latency are reported on every review and are not targets.</p>

<div class="landing-targets" markdown>
<div class="landing-stat" markdown>
<span class="landing-stat-n">≥ 65%</span>
<span class="landing-stat-l">of findings accepted by authors, at or above human reviewers</span>
</div>
<div class="landing-stat" markdown>
<span class="landing-stat-n">≤ 10%</span>
<span class="landing-stat-l">effective false positives for every active rule</span>
</div>
<div class="landing-stat" markdown>
<span class="landing-stat-n">2×</span>
<span class="landing-stat-l">the precision of a generic AI reviewer on the same PRs</span>
</div>
<div class="landing-stat" markdown>
<span class="landing-stat-n">5% →</span>
<span class="landing-stat-l">of PRs merged with no human review at launch, then growing</span>
</div>
</div>

<div class="landing-close" markdown>

## Put humans only where a human is needed. {: .landing-h2 }

[Quickstart](guides/install/quickstart.md){ .md-button .md-button--primary }
[Your first review](tutorials/first-review.md){ .md-button }
[Why Exemplar](concepts/why.md){ .md-button }

<p class="landing-note">Then <a href="concepts/">Concepts</a> for how it thinks, <a href="guides/">Guides</a> for one task at a time, and the <a href="reference/">Reference</a> for every file, setting and command.</p>

</div>

</div>
