# Exemplar

Exemplar is a self-correcting AI code reviewer for GitHub that **reviews with your organization's own judgment, not the internet's**. It mines your review history into rules your teams own in git, enforces them on every pull request with one focused agent per rule, verifies every finding with a second model before a human sees it, and learns from what reviewers say back, through a human gate. The same rules reach coding agents and IDEs before a PR exists.

It is built on one conviction: precision is the product. A reviewer people learn to ignore is worse than none. [Why Exemplar](concepts/why.md) makes the case; the rest of this site is how to run it.

## Start here

<div class="grid cards" markdown>

-   :material-rocket-launch: **Quickstart**

    ---

    A trial on one node with an in-cluster Postgres, through a Cloudflare Tunnel or an Ingress. About 20 minutes.

    [:octicons-arrow-right-24: Quickstart](tutorials/quickstart.md)

-   :material-source-pull: **Your first review**

    ---

    Watch a shadow review, turn posting on, and give the bot its first feedback.

    [:octicons-arrow-right-24: Your first review](tutorials/first-review.md)

-   :material-file-document-edit: **Your first rule**

    ---

    Write a rule, give it eval cases, try it on a real PR, and merge it.

    [:octicons-arrow-right-24: Your first rule](tutorials/first-rule.md)

-   :material-lightbulb: **Concepts**

    ---

    How a review works, how a rule lives and dies, what leaves the cluster and what never does.

    [:octicons-arrow-right-24: Concepts](concepts/index.md)

</div>

## The lifecycle

1. **Install** Exemplar into one Kubernetes namespace as a GitHub App, with `exemplar-config` as the repository your teams own ([Quickstart](tutorials/quickstart.md), [production install](guides/install/production.md)).
2. **Review in shadow.** Every lane runs and the results are stored; nothing is posted until you say so ([Your first review](tutorials/first-review.md)).
3. **Go live.** Findings arrive as one review per PR head, labelled, budgeted, with a one-click fix where the change is local. Exemplar approves or stays silent; it never blocks.
4. **Give feedback.** Authors reply `/exemplar wrong <why>` or click a link; owners uphold or overturn ([feedback commands](reference/feedback-commands.md)).
5. **Grow the corpus.** The learning plane mines review history into candidate rules with citations, proposes them as config PRs, and demotes rules people reject ([rule lifecycle](concepts/rule-lifecycle.md)).
6. **Shift left.** The same rules export to coding-agent instruction files and run from the CLI before a PR exists ([review before pushing](guides/authors/review-before-pushing.md)).

## What's where

| Area | What it covers | Start with |
|---|---|---|
| Installing | Kubernetes manifests, the setup page that registers the GitHub App, Postgres, inbound through a tunnel or an Ingress, sign-in | [Quickstart](tutorials/quickstart.md), then [How to install for production](guides/install/production.md) |
| Operating | Kill switches, incidents, upgrades, key rotation, Grafana, telemetry, the optional CronJobs | [How to respond to an incident](guides/operate/runbook.md), [How to troubleshoot](guides/operate/troubleshoot.md) |
| Rules | The `exemplar-config` repository, rule files, eval cases, config CI, the playground, mined candidates, adjudication | [Your first rule](tutorials/first-rule.md), [exemplar-config reference](reference/config/index.md) |
| Reviewing | What a posted finding looks like, feedback commands and links, reactions, SARIF alerts | [How to respond to a finding](guides/authors/respond-to-a-finding.md) |
| Before the PR | `exemplar login` and `exemplar review`, coding agents, exported instruction files | [How to review before pushing](guides/authors/review-before-pushing.md) |
| Configuration | Every `exemplar-config` file, environment variable, Secret and ConfigMap | [Reference](reference/index.md) |
| How it works | Components, the review pipeline, trust boundaries, data handling | [Architecture](concepts/architecture.md), [How a review works](concepts/how-a-review-works.md) |
| Developing | The devenv environment, checks, manifests, this site | [How to set up the development environment](guides/develop/dev-environment.md), [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md) |

## What's in the box

Everything runs on Kubernetes in one namespace against one Postgres database, as a GitHub App.

| Binary | What it does |
|---|---|
| `exemplar-webhook` | Receives GitHub webhooks and stores each delivery once. |
| `exemplar-worker` | Routes events and reviews pull requests. Also runs `exemplar review` evaluations, playground runs, web UI owner actions, and the periodic jobs (reconcile, sweep, retention, stats). |
| `exemplar-gateway` | The only holder of model-provider keys. Routes, fails over, redacts, and records cost. |
| `exemplar-search` | A git mirror and code index for review context (`search_text`, `find_definition`, `find_references`, `dependents`). |
| `exemplar-api` | Sign-in (GitHub OAuth and/or OIDC), signed feedback links, the `/ui/` web UI, and `/v1/evaluate` for the CLI and services. |
| `exemplar-learn` | The learning-plane CronJobs. Ingest, extract, evals, compile, propose, demote, expiry, digest, teach and export. |
| `learning/` (Python) | The `cluster` and `rank` CronJobs. |
| `exemplar` | The CLI. Covers config validation, evals, `exemplar login`/`review`, `try`, replay, mutations, and calibration. |
