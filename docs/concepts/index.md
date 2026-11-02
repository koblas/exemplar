# Concepts

These pages explain how Exemplar thinks and why it is built the way it is. They are written for someone technical who is new to AI code review and wants to understand the system, not for someone who already knows it. They give background, not instructions: for steps see the [Guides](../guides/index.md), and for exact fields and flags see the [Reference](../reference/index.md).

If you are new, read them in this order. [Why Exemplar](why.md) explains the problem. [How a review works](how-a-review-works.md) explains what happens to a pull request. The rest fill in the details.

| Page | What it explains |
|---|---|
| [Why Exemplar](why.md) | Why code review has become the bottleneck, the two jobs Exemplar does about it (hold the standard, absorb the volume), its principles, what it won't do, and how it knows it is working. |
| [How a review works](how-a-review-works.md) | What happens between a GitHub webhook and a posted comment: the three lanes, the verifier, validation, budgets, and how the verdict (no human needed, or routed to humans) is reached. |
| [Architecture](architecture.md) | The processes, how they talk to each other, and the paths work takes through them, as built. |
| [Rule lifecycle](rule-lifecycle.md) | How a rule is mined, proposed, shadowed, activated, demoted, extended and killed, and why a human sits at every gate. |
| [Trust and data handling](trust-and-data.md) | Where the trust boundaries are, what the model is allowed to say, and what leaves the cluster. |
| [Glossary](glossary.md) | The words the rest of the site uses. |
