# Install

Exemplar installs into one Kubernetes namespace (`exemplar`) and reviews repositories owned by one GitHub organization or account. On the desktop you need only `kubectl` and `openssl` (and `cloudflared` if you choose the tunnel). There is no install CLI: the first time exemplar-api starts, it serves a setup page that registers the GitHub App for you. GitHub reaches it through a Cloudflare Tunnel or through your own Ingress controller; both are first-class.

- **[Quickstart](quickstart.md)**: a trial on one node with an in-cluster Postgres, through a tunnel or an Ingress. About 20 minutes.
- **[Production install](production.md)**: your own Postgres, sizing and edge rules.
- **[Upgrading](upgrading.md)**, and the [deployment reference](../reference/deployment.md) for every setting, Secret and workload.
