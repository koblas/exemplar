# How to install for production

The [Quickstart](quickstart.md) steps, with these differences: your own Postgres, two replicas of each service, pinned images, and the rules at the edge.

## Prerequisites

| You need | Notes |
|---|---|
| Kubernetes **1.29+** | Native sidecars (git-sync) need 1.29. The manifests are written against 1.33. The namespace runs at the `restricted` Pod Security level. |
| A way in, with TLS | One public host for people and webhooks (or two, with `EXEMPLAR_WEBHOOK_URL`). **Default:** a Cloudflare Tunnel; the cluster then needs no public ingress. **Or** an ingress controller (`deploy/k8s/direct`, which assumes `ingress-nginx`), or both. |
| A storage class for local SSD | `exemplar-search` asks for 500Gi per replica on `local-ssd`, two replicas on different nodes. Set both in the overlay. |
| **Postgres 16** with **pgvector** | Reachable from the cluster on 5432. Use a read replica for Grafana if you add it. |
| A model provider | An Anthropic API key, or AWS Bedrock through workload identity (see `global.yaml` `providers`). The gateway builds every provider `global.yaml` routes to, skips one whose credentials are missing with a warning, and fails only when a stage is left with none. |
| GitHub org owner rights | To create and install the GitHub App (a personal account works too). GitHub Enterprise Server works too. |

## 1. Prepare `exemplar-config`

As in the quickstart, and also review `platform/eligibility.yaml` (repos listed there are never sent to a model) and `CODEOWNERS` (rule owners and the platform team review their files). Validate with `exemplar validate path/to/exemplar-config` ([The CLI](../../reference/cli.md)). Its `eval.yml` jobs beyond `validate` run only once their repository variables are set ([How to set up config CI](../rules/config-ci.md)).

## 2. Create the database

Create a database and role. The services apply their migrations at startup, one pod at a time:

```sql
CREATE ROLE exemplar LOGIN PASSWORD '<password>';
CREATE DATABASE exemplar OWNER exemplar;
\c exemplar
CREATE EXTENSION IF NOT EXISTS vector;  -- as a superuser, if the exemplar role cannot
```

Put its URL in `exemplar-database` (`url`), for example `postgres://exemplar:<password>@db.internal:5432/exemplar?sslmode=require`. For an evaluation without a managed database, see [How to run Postgres in the cluster](in-cluster-postgres.md).

## 3. Create the Secrets

Create them from your secret manager; none are in this repository or the rendered manifests. Before applying: `exemplar-database`, `exemplar-root-key`, `exemplar-providers` (unless Bedrock only) and, with the tunnel, `exemplar-cloudflared`. The setup page produces `exemplar-github-app` (and `exemplar-exports-app`). `exemplar-oidc` is only for OIDC sign-in. Every key is listed in [Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md).

For Bedrock, annotate the `exemplar-gateway` service account for the cloud's identity federation. Don't add AWS keys to a Secret.

## 4. Write the overlay

Copy [`examples/k8s/acme`](https://github.com/koblas/exemplar/tree/main/examples/k8s/acme):

| File | Sets |
|---|---|
| `kustomization.yaml` | The build (the release's `install-tunnel.yaml` or `install-direct.yaml`, or the repo's `deploy/k8s/base?ref=<version>` with both image digests pinned), and the search volume's storage class and size. The Ingress and in-cluster Postgres pieces are there, commented out. |
| `settings.yaml` | `EXEMPLAR_CONFIG_REPO` and `EXEMPLAR_API_URL`. For GitHub Enterprise Server, also `EXEMPLAR_GH_WEB_URL` (`https://<host>/`) and `EXEMPLAR_GH_API_URL` (`https://<host>/api/v3/`). |

Startup refuses any value left from the examples (`example-org`, `Iv1.example`, `*.example.invalid`, `<…>`), naming each one. How to pin images is on [Releases and images](../../reference/releases.md).

=== "Cloudflare Tunnel"

    The overlay keeps the default build. The tunnel routes by path: `/webhook` to the webhook, everything else to the API, on whatever host names you route to it. Cloudflare's controls replace what an Ingress would do, so a tunnel install isn't done without them:

    - an **Access application** with your org's IdP on the host, with a **bypass** policy for the `/webhook` path (GitHub can't sign in), unless OIDC sign-in alone is enough for you;
    - a **WAF custom rule** that blocks `/webhook` from any source outside GitHub's `hooks` ranges (`https://api.github.com/meta`). The webhook still checks every delivery's signature.

    With a separate webhook host (`EXEMPLAR_WEBHOOK_URL`), each rule goes on its own host instead.

    Cloudflare ends TLS at its edge and sees requests in clear text, webhook payloads included. If that's not acceptable, use the Ingress build.

=== "Ingress"

    Use `deploy/k8s/direct` (or the release's `install-direct.yaml`). Set the host, TLS secret and class on both Ingresses (the webhook's matches `/webhook` only), and, if your controller doesn't run in `ingress-nginx`, the namespace in the two NetworkPolicy patches. Both are commented out in the example. Put org SSO in front of everything but `/webhook` (an IdP-aware proxy or ingress auth annotations).

## 5. Render, check and apply

Name the context explicitly:

```bash
kubectl kustomize overlays/acme > rendered.yaml
```

```bash
go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0 -strict -summary -kubernetes-version 1.33.0 rendered.yaml
```

```bash
kubectl --context <your-cluster> apply -f rendered.yaml
```

## 6. Register the App and check

Register the App from the setup page ([Quickstart](quickstart.md#6-register-the-github-app)) and work through `/ui/setup` (step 7 there). Then set up [sign-in](sign-in.md) for your organisation, and go through [Your first review](../../tutorials/first-review.md) on a shadow repository before posting anywhere.

From a clone of this repository, `go test ./deploy` also checks every build against the code: ports, probes, shutdown deadlines, every environment variable, security contexts and network paths ([How to check manifests locally](../develop/check-manifests.md)).

## Sizing

The core manifests run each review as a Kubernetes Job of its own (`--launcher=kubernetes`, [Runners](../../reference/workloads.md#runners)): the runner template's requests are one review's, and `--runner-quota` (8 in the core manifests) caps the Jobs in flight across every worker pod. Raise it to let a burst of pushes fan out; each runner in flight holds Postgres connections, so size `max_connections` (or a pooler) with it. The worker pod's own requests are still sized for reviews in process and can come down. Worker, runner, search and storage requests are starting points. Re-size from your own monorepo benchmark: shallow sparse clone time, zoekt full-index time and size, query latency, ctags coverage of the main languages.

## What the manifests cannot do

- **Egress by host name.** NetworkPolicy matches addresses, so "HTTPS out" is any public address on 443 (and 22 for git-sync). Per-host lists (GitHub only for search, providers only for the gateway) need an FQDN-aware policy from the CNI, such as Cilium `toFQDNs`, on top.
- **The tunnel's egress** is any public address on 7844. Cloudflare's edge ranges (or an FQDN policy for `region1.v2.argotunnel.com` and `region2.v2.argotunnel.com`) would narrow it.
- **The tunnel's edge rules are outside the repo.** Without the WAF rule, anyone can post to `/webhook`, though deliveries are still HMAC-checked. Without Access, the API's own sign-in is the only gate. Nothing in the cluster can check them.
- **The Kubernetes API's address.** The worker's NetworkPolicy reaches the API server on 443 and 6443 at any private address. Narrow it to the address and port of `kubectl get endpoints kubernetes -n default` in an overlay.
- **The ingress namespace.** In Ingress builds, `exemplar-webhook-from-ingress` and `exemplar-api-from-ingress` admit the `ingress-nginx` namespace; change it to match the cluster's controller.
- **Debian packages** float within the pinned base snapshot. Rebuild on a new base digest to update them.
