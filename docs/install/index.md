# Install

Exemplar installs into one Kubernetes namespace (`exemplar`) and reviews repositories in one GitHub organization. On the desktop you need only `kubectl` and `openssl` (and `cloudflared` for the default tunnel). There is no install CLI: the first time exemplar-api starts, it serves a setup page that registers the GitHub App for you.

- **[Quickstart](quickstart.md)**: a trial on one node with an in-cluster Postgres. About 20 minutes.
- **[Production install](production.md)**: your own Postgres, sizing and edge rules.
- **[Upgrading](upgrading.md)**, and the [deployment reference](../reference/deployment.md) for every setting, Secret and workload.
