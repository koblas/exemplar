# Exemplar

Exemplar is a self-correcting AI code reviewer for GitHub that **reviews with your organization's own judgment, not the internet's**. It mines your review history into rules your teams own in git, enforces them on every pull request with one focused agent per rule, verifies every finding with a second model before a human sees it, and learns from what reviewers say back, through a human gate. The same rules reach coding agents and IDEs before a PR exists.

It is built on one conviction: precision is the product. A reviewer people learn to ignore is worse than none. [Why Exemplar](why.md) makes the case; the rest of this site is how to run it.

## What it does

- **Learns rules from review comments**, with citations, and proposes them as pull requests to `exemplar-config`. Nothing enforces until a rule owner merges it.
- **Reviews every PR** against those rules: deterministic patterns where possible, one agent per rule where judgment is needed, and a narrow bug-hunting lane.
- **Verifies before posting.** A second model tries to refute each finding and judges whether it matters here; code checks the evidence. Comments are budgeted, labelled, and carry a one-click fix where they can.
- **Never blocks.** It approves or stays silent, and can auto-approve a narrow, gated class of low-risk PRs with a kill switch.
- **Demotes rules people reject**, and turns a correction in a reply into a rule-edit proposal.
- **Ships the rules upstream** to coding-agent instruction files, an MCP server and a pre-PR CLI.

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

## Where to start

- **Why it exists:** [Why Exemplar](why.md).
- **Trying it:** [Quickstart](install/quickstart.md), about 20 minutes on one node.
- **Running it:** [Production install](install/production.md), then [Going live](operations/going-live.md).
- **Every setting, Secret and workload:** [Deployment reference](reference/deployment.md).
- **How it works:** [Architecture](architecture.md).
- **Changing it:** [Developing](development.md) and [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md).
