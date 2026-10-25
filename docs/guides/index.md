# Guides

Each guide solves one task for one kind of reader. They assume you know what Exemplar is ([Concepts](../concepts/index.md)) and have an installation ([Quickstart](install/quickstart.md)); they point at the [Reference](../reference/index.md) for every field and flag rather than repeating it.

## Where things are

| Area | What it covers | Start with |
|---|---|---|
| Installing | Kubernetes manifests, the setup page that registers the GitHub App, Postgres, inbound through a tunnel or an Ingress, sign-in | [Quickstart](install/quickstart.md), then [How to install for production](install/production.md) |
| Operating | Kill switches, incidents, upgrades, key rotation, Grafana, telemetry, the optional CronJobs | [How to respond to an incident](operate/runbook.md), [How to troubleshoot](operate/troubleshoot.md) |
| Rules | The `exemplar-config` repository, rule files, eval cases, config CI, the playground, mined candidates, adjudication | [Your first rule](../tutorials/first-rule.md), [exemplar-config reference](../reference/config/index.md) |
| Reviewing | What a posted finding looks like, feedback commands and links, reactions, SARIF alerts | [How to respond to a finding](authors/respond-to-a-finding.md) |
| Who reviews | The verdict on every PR: approved with no human, or routed to humans with reasons; the gates, the audit page, the counter-metrics | [How a review works](../concepts/how-a-review-works.md#who-reviews-this-pr), [How to turn on auto-approve](operate/auto-approve.md) |
| Before the PR | `exemplar login` and `exemplar review`, coding agents, exported instruction files | [How to review before pushing](authors/review-before-pushing.md) |
| Configuration | Every `exemplar-config` file, environment variable, Secret and ConfigMap | [Reference](../reference/index.md) |
| How it works | Components, the review pipeline, trust boundaries, data handling | [Architecture](../concepts/architecture.md), [How a review works](../concepts/how-a-review-works.md) |
| Developing | The devenv environment, checks, manifests, this site | [How to set up the development environment](develop/dev-environment.md), [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md) |

## Install

For the platform engineer standing Exemplar up.

- [Quickstart](install/quickstart.md): a trial install on one node with an in-cluster Postgres, about 20 minutes. Production builds on the same steps.
- [How to install for production](install/production.md): a managed Postgres, two replicas, the edge rules.
- [How to set up sign-in](install/sign-in.md): GitHub OAuth, OpenID Connect, or both.
- [How to run Postgres in the cluster](install/in-cluster-postgres.md): the optional component for evaluation.
- [How to build your own images](install/build-images.md): unpublished changes or a private registry.
- [How to upgrade](install/upgrade.md) and [How to rotate keys](install/rotate-keys.md).

## Operate

For whoever carries the pager.

- [How to troubleshoot an installation](operate/troubleshoot.md): where to look when nothing arrives.
- [How to respond to an incident](operate/runbook.md): the kill switches, provider outages, backlogs, bad approvals.
- [How to turn on auto-approve](operate/auto-approve.md): the branch rules and config each repository needs.
- [How to set up Grafana](operate/grafana.md), [How to send telemetry](operate/telemetry.md).
- [How to enable rule exports](operate/rule-exports.md), [How to enable the compile CronJob](operate/compile-cronjob.md).

## Rule owners

For the team that owns rules in `exemplar-config`.

- [How to write a rule](rules/write-a-rule.md): exemplar and compiled rules, severities, exports.
- [How to write eval cases](rules/eval-cases.md): historical and synthetic cases, negatives, mutations.
- [How to test a rule before merging](rules/test-a-rule.md): `exemplar try`, the playground, local evals.
- [How to set up config CI](rules/config-ci.md): the variables and secrets each workflow job needs.
- [How to review mined candidates](rules/review-candidates.md): the candidates page and proposed PRs.
- [How to seed a rule by hand](rules/seed-a-rule.md): a rule you write, through the same candidate path.
- [How to adjudicate feedback](rules/adjudicate.md): uphold, overturn, teach-the-bot proposals, the digest.

## PR authors

For anyone whose pull request Exemplar reviews, and the coding agents working for them.

- [How to respond to a finding](authors/respond-to-a-finding.md): the commands, the links, the reactions, what each does.
- [How to review before pushing](authors/review-before-pushing.md): `exemplar login` and `exemplar review`.
- [How to run Exemplar from a coding agent](authors/coding-agents.md): instruction files and the pre-PR check.

## Develop

For contributors to Exemplar itself.

- [How to set up the development environment](develop/dev-environment.md).
- [How to check manifests locally](develop/check-manifests.md).
- [How to write documentation](develop/docs.md).
