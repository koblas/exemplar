# Exemplar

AI-assisted coding multiplied the pull requests a team produces and did nothing for the people who review them. Exemplar is a self-correcting AI code reviewer for GitHub built for that bottleneck: it holds the review standard, and it decides which PRs still need a human. It reviews with your organization's own judgment, not the internet's. It mines your review history into rules your teams own in a git repository (`exemplar-config`), enforces them on every pull request with one agent per rule, verifies each finding with a second model before anyone sees it, and demotes rules people say are wrong. It never blocks a PR; it approves low-risk changes with no human review once deterministic gates pass, routes the rest to humans with the reasons, and ships the same rules to coding agents before a PR exists. It runs on Kubernetes against one Postgres database.

**Documentation: <https://koblas.github.io/exemplar/>**

- [Why Exemplar](https://koblas.github.io/exemplar/concepts/why/): the problem, the approach, and what it won't do.
- [Quickstart](https://koblas.github.io/exemplar/guides/install/quickstart/): a trial on one node, about 20 minutes, then [your first review](https://koblas.github.io/exemplar/tutorials/first-review/) and [your first rule](https://koblas.github.io/exemplar/tutorials/first-rule/). Production: [how to install](https://koblas.github.io/exemplar/guides/install/production/).
- [Guides](https://koblas.github.io/exemplar/guides/) for installers, operators, rule owners, PR authors and contributors; the [reference](https://koblas.github.io/exemplar/reference/) for every config file, setting, Secret, workload, CLI command and web UI page.
- [Architecture](https://koblas.github.io/exemplar/concepts/architecture/) and [how a review works](https://koblas.github.io/exemplar/concepts/how-a-review-works/); contributions follow [CONTRIBUTING.md](CONTRIBUTING.md).

The pages' sources are in [`docs/`](docs/) (`make docs-serve` to preview; `make docs` builds and checks them). The design documents stay in the repository only: the [technical spec](docs/spec/technical-spec.md) (`§` references), the [PRD](docs/spec/prd.md) (`FR-…` ids), and the [roadmap and plans](docs/plans/README.md).
