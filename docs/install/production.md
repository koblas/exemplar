# Production install

The same steps, with these differences.

**Prerequisites.**

| You need | Notes |
|---|---|
| Kubernetes **1.29+** | Native sidecars (git-sync) need 1.29. The manifests are written against 1.33. The namespace runs at the `restricted` Pod Security level. |
| A way in, with TLS | One public host for people and webhooks (or two, with `EXEMPLAR_WEBHOOK_URL`). **Default:** a Cloudflare Tunnel; the cluster then needs no public ingress. **Or** an ingress controller (`deploy/k8s/direct`, which assumes `ingress-nginx`), or both. |
| A storage class for local SSD | `exemplar-search` asks for 500Gi per replica on `local-ssd`, two replicas on different nodes. Set both in the overlay. |
| **Postgres 16** with **pgvector** | Reachable from the cluster on 5432. Use a read replica for Grafana if you add it. |
| A model provider | An Anthropic API key, or AWS Bedrock through workload identity (see `global.yaml` `providers`). |
| GitHub org owner rights | To create and install the GitHub App. GitHub Enterprise Server works too. |

**`exemplar-config`.** As in the quickstart, and also review `platform/eligibility.yaml` (repos listed there are never sent to a model) and `CODEOWNERS` (rule owners and the platform team review their files). Validate with `exemplar validate path/to/exemplar-config` ([the CLI](../cli.md)).

**Postgres.** Create a database and role. The services apply their migrations at startup, one pod at a time:

```sql
CREATE ROLE exemplar LOGIN PASSWORD '<password>';
CREATE DATABASE exemplar OWNER exemplar;
\c exemplar
CREATE EXTENSION IF NOT EXISTS vector;  -- as a superuser, if the exemplar role cannot
```

Put its URL in `exemplar-database` (`url`), for example `postgres://exemplar:<password>@db.internal:5432/exemplar?sslmode=require`.

**Secrets.** Create them from your secret manager; none are in this repository or the rendered manifests. Before applying: `exemplar-database`, `exemplar-root-key`, `exemplar-providers` (unless Bedrock only) and, with the tunnel, `exemplar-cloudflared`. The setup page produces `exemplar-github-app` (and `exemplar-exports-app`). `exemplar-oidc` is only for OIDC sign-in. The full table is in the [deployment reference](../reference/deployment.md).

**The overlay.** Copy [`examples/k8s/acme`](https://github.com/koblas/exemplar/tree/main/examples/k8s/acme):

| File | Sets |
|---|---|
| `kustomization.yaml` | The build (the release's `install-tunnel.yaml` or `install-direct.yaml`, or the repo's `deploy/k8s/base?ref=<version>` with both image digests pinned), and the search volume's storage class and size. The Ingress and in-cluster Postgres pieces are there, commented out. |
| `settings.yaml` | `EXEMPLAR_CONFIG_REPO` and `EXEMPLAR_API_URL`. For GitHub Enterprise Server, also `EXEMPLAR_GH_WEB_URL` (`https://<host>/`) and `EXEMPLAR_GH_API_URL` (`https://<host>/api/v3/`). |

Startup refuses any value left from the examples (`example-org`, `Iv1.example`, `*.example.invalid`, `<…>`), naming each one.

**The edge.** The tunnel routes by path: `/webhook` to the webhook, everything else to the API, on whatever host names you route to it. Cloudflare's controls replace the Ingress's (spec §13 IN-1, IN-2), so a tunnel install isn't done without them:
- an **Access application** with your org's IdP on the host, with a **bypass** policy for the `/webhook` path (GitHub can't sign in), unless OIDC sign-in alone is enough for you;
- a **WAF custom rule** that blocks `/webhook` from any source outside GitHub's `hooks` ranges (`https://api.github.com/meta`). The webhook still checks every delivery's signature.

Cloudflare ends TLS at its edge and sees requests in clear text, webhook payloads included. If that's not acceptable, use `deploy/k8s/direct`: set the host, TLS secret and class on both Ingresses (the webhook's matches `/webhook` only), and, if your controller doesn't run in `ingress-nginx`, the namespace in the two NetworkPolicy patches. Both are commented out in the example.

**Render, check and apply.** Name the context explicitly:

```bash
kubectl kustomize overlays/acme > rendered.yaml
```

```bash
go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0 -strict -summary -kubernetes-version 1.33.0 rendered.yaml
```

```bash
kubectl --context <your-cluster> apply -f rendered.yaml
```

Then register the App from the setup page ([quickstart](quickstart.md) step 6) and work through `/ui/setup` (step 7). From this repository, `go test ./deploy` also checks every build against the code: ports, probes, shutdown deadlines, every environment variable, security contexts and network paths.
