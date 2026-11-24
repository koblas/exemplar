<a id="1-summary"></a>

## 1. Summary

AI code generation has multiplied the volume of code engineers produce, but time-to-production has barely moved. The bottleneck migrated from writing code to reviewing it. Senior engineers now spend their time reading rather than designing, review load scales linearly with headcount while generation scales with tokens, and every codegen tool is capped at the throughput of the humans supervising it.

Generic AI reviewers do not fix this. They know the public internet, not the company: repo conventions, security patterns, infra assumptions, migration scars, incident (SEV) lessons, and ownership graphs. They produce correct-but-irrelevant comments, engineers learn to ignore them within a week, and that trust is expensive to win back.

Exemplar **mines the organization's own review history into a curated, version-controlled set of review rules ("exemplars")**, then **enforces them on every pull request with one focused agent per rule**, alongside deterministic checks and a narrow bug-hunting lane. Every candidate finding passes a **verification stage** and **deterministic validation in code** before it reaches a human. A single human gate sits between offline learning and online enforcement, and the same rules are pushed **upstream to the coding agents and IDEs** that write the code.

The core insight from the source: this is **not a prompt problem, it is a systems problem**, and the durable asset is the encoded organizational judgment, not the model that reads it. The core insight from the industry: **precision is the product** — every mature system that survived (Google Tricorder and AutoCommenter, Meta's diff-time analysis, Uber uReview, Anthropic Code Review) is organized around keeping false positives low and measured.
