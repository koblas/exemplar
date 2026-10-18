# Exemplar

Exemplar is a self-correcting AI code reviewer for GitHub. It reviews pull requests against rules your teams own in a git repository (`exemplar-config`), learns new rules from your reviewers' comments, and demotes rules people say are wrong. It can auto-approve low-risk changes once deterministic gates pass. It runs on Kubernetes against one Postgres database.

**Documentation: <https://koblas.github.io/exemplar/>**

- [Quickstart](https://koblas.github.io/exemplar/install/quickstart/): a trial on one node, about 20 minutes. Production: [install](https://koblas.github.io/exemplar/install/production/), then [going live](https://koblas.github.io/exemplar/operations/going-live/).
- [Deployment reference](https://koblas.github.io/exemplar/reference/deployment/): every setting, Secret and workload.
- [Architecture](https://koblas.github.io/exemplar/architecture/) and [developing](https://koblas.github.io/exemplar/development/); contributions follow [CONTRIBUTING.md](CONTRIBUTING.md).

The pages' sources are in [`docs/`](docs/) (`make docs-serve` to preview). The design documents stay in the repository only: the [technical spec](docs/spec/technical-spec.md) (`§` references), the [PRD](docs/spec/prd.md) (`FR-…` ids), and the [roadmap and plans](docs/plans/README.md).
