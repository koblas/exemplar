# Exemplar

Exemplar is a self-correcting AI code reviewer for GitHub. It reviews pull requests against rules your teams own in a git repository (`exemplar-config`). It learns new rules from your reviewers' comments, and demotes rules people say are wrong. It can auto-approve low-risk changes once a set of deterministic gates pass.

## Components

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

Everything runs on Kubernetes against one Postgres database. Work is queued with River, in Postgres.


## Where to start

- **Trying it:** [Quickstart](install/quickstart.md), about 20 minutes on one node.
- **Running it:** [Production install](install/production.md), then [Going live](operations/going-live.md).
- **Every setting, Secret and workload:** [Deployment reference](reference/deployment.md).
- **How it works:** [Architecture](architecture.md).
- **Changing it:** [Developing](development.md) and [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md).
