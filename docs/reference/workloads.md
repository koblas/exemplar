# Workloads, ports and probes

The Kubernetes manifests in [`deploy/`](https://github.com/koblas/exemplar/tree/main/deploy), as plain Kustomize. `go test ./deploy` checks them against the code ([How to check manifests locally](../guides/develop/check-manifests.md)).

## Layout

| Path | What |
|---|---|
| `docker/Dockerfile` | The Go services image: every `cmd/` binary, plus git, ripgrep, universal-ctags, ast-grep 0.45.1, Semgrep 1.172.0, unshare and prlimit. Base images are pinned by digest, ast-grep by checksum. Built for linux/amd64 only. |
| `docker/learning.Dockerfile` | The Python learning image (`learning/`: cluster, rank), with dependencies from `uv.lock`. |
| `k8s/core/` | Plain manifests for everything but inbound, with a `kustomization.yaml` (and the image pins). Not built alone. |
| `k8s/components/cloudflare-tunnel/` | Inbound through a Cloudflare Tunnel: the `exemplar-cloudflared` Deployment, its routes, and the NetworkPolicies that let it reach the webhook and the API. |
| `k8s/components/ingress/` | Inbound through an Ingress controller: the two Ingresses and the NetworkPolicies that admit `ingress-nginx`. |
| `k8s/components/postgres/` | Optional in-cluster Postgres 16 with pgvector ([How to run Postgres in the cluster](../guides/install/in-cluster-postgres.md)). In no shipped build. |
| `k8s/base/` | The default build: core plus the tunnel. `kubectl kustomize deploy/k8s/base`. |
| `k8s/direct/` | Core plus the Ingresses, no tunnel. `kubectl kustomize deploy/k8s/direct`. |
| `manifests_test.go` | The checks. |
| `render-release.sh` | Renders a release's `install-tunnel.yaml` and `install-direct.yaml` with both digests pinned. |
| `../examples/k8s/acme/` | An example installation overlay: the default build, Docker Hub images, settings, sign-in, tunnel routes and search storage; the Ingress and Postgres pieces commented out. CI renders it. |
| `../examples/k8s/quickstart/`, `quickstart-direct/` | The trial overlays (one replica, in-cluster Postgres, small requests) through the tunnel and through an Ingress. CI renders both. |

## Builds and inbound

GitHub and people reach exemplar-webhook and exemplar-api one of three ways. Core's NetworkPolicies admit nothing to either; each component adds its own allow (`exemplar-webhook-from-tunnel`, `exemplar-webhook-from-ingress`, and the same for the API), so a build admits exactly the sources it ships.

| Build | How | Admits | Resources |
|---|---|---|---|
| `base` (default) | `exemplar-cloudflared` dials out to Cloudflare on 7844 (QUIC, falling back to HTTP/2 over TCP). Cloudflare balances between its 2 replicas. | Only `exemplar-cloudflared` pods | 53 |
| `direct` | Ingresses on the cluster's controller | Only the `ingress-nginx` namespace | 50 |
| `base` + `components/ingress` | Both | Both | 57 |
| `base` + `components/postgres` | As `base`, with the in-cluster database | As `base` | 57 |

The tunnel is locally managed: routing is in the `exemplar-cloudflared` ConfigMap, reviewed with the rest of the manifests rather than set in the dashboard. The rules match by path, not host name: only `^/webhook$` reaches the webhook, as the Ingress's `Exact` path does, and everything else reaches the API. Only the host names an installation routes to the tunnel (`cloudflared tunnel route dns`) reach it at all. cloudflared runs as its own Deployment, never a sidecar, so the tunnel credentials stay out of the webhook and API pods and the webhook's egress stays Postgres only. Cloudflare ends TLS at its edge and sees requests in clear text; the webhook still checks each delivery's HMAC, and its rejection log's `remote` is a cloudflared pod's address, not GitHub's. The edge rules an installation adds are on [How to install for production](../guides/install/production.md#4-write-the-overlay).

## Workloads

| Binary | Kind | Replicas | Port | Probes | Shutdown → grace | Notes |
|---|---|---|---|---|---|---|
| `exemplar-webhook` | Deployment + Service + (Ingress) | 2 | 8080 `http`, Service 80 | `/healthz`, `/readyz` | 15 s → 30 s | Public for GitHub at `/webhook`; egress to Postgres only |
| `exemplar-api` | Deployment + Service + (Ingress) | 2 | 8080 `http`, Service 80; 8081 `setup` (loopback, no Service) | `/healthz`, `/readyz` | 15 s → 30 s | Public for people, behind org SSO at the edge; egress to Postgres and GitHub OAuth |
| `exemplar-gateway` | Deployment + Service | 2 | 8080 `http`, Service 80 | `/healthz`, `/readyz` | 25 s → 40 s | The only holder of provider keys; marks itself unready and starts shutting down at the same moment, so a request routed in that window is retried by the caller |
| `exemplar-worker` | Deployment | 2 (×4 reviews each) | 8080 `http` (probes only) | `/healthz`, `/readyz` | 80 s → 120 s | Runs River's periodic jobs on its leader |
| `exemplar-search` | StatefulSet + 2 Services | 2, on different nodes | 8080 `http`, Services 8080 and 80 | `/healthz`, `/readyz` | 25 s → 45 s | Local SSD per replica (`local-ssd`, 500Gi placeholder). Optional: an overlay may delete it and empty `EXEMPLAR_SEARCH_URL` ([How to run without exemplar-search](../guides/install/without-search.md)) |
| `exemplar-learn` | 11 CronJobs | | | | | ingest, path-events, extract, evals, compile (suspended), propose, demote, expiry, teach, digest, export |
| `exemplar-learning` | 2 CronJobs | | | | | cluster, rank (Python) |
| `exemplar-postgres` | StatefulSet + Service | 1 | 5432 | `pg_isready` over TCP; startup probe allows 10 min; preStop `pg_ctl stop --mode=fast --timeout=30` | 30 s → 45 s | `components/postgres` only. pgvector 0.8.0 on Postgres 16, pinned by digest. In from the Postgres clients on 5432; nothing out |
| `exemplar-cloudflared` | Deployment | 2 | 2000 metrics | `/healthcheck` (liveness), `/ready` (an edge connection) | 20 s → 30 s | Tunnel builds only. cloudflared 2026.9.3, pinned by digest. Egress to Cloudflare on 7844 and to the webhook and API |

Every pod runs as UID 65532 with a read-only root filesystem, no capabilities, no privilege escalation and RuntimeDefault seccomp, so it passes the namespace's `restricted` Pod Security level; has no service account token, since nothing calls the Kubernetes API; and has an `emptyDir` for `/tmp` and any other path it writes. Postgres runs as the image's `postgres` user (UID 999).

### Config sync

The worker, gateway, search and learn pods read `exemplar-config` from a git-sync v4.7.1 sidecar, cloning `$(EXEMPLAR_GH_WEB_URL)$(EXEMPLAR_CONFIG_REPO)` over HTTPS as the review app (`--github-app-*`, from `exemplar-github-app`). Services use a native sidecar (`initContainers` with `restartPolicy: Always`) on port 2020 `sync`, whose startup probe holds the main container until the first sync. CronJobs use a one-time init container.

### CronJob schedules

| CronJob | Schedule (UTC) | Job |
|---|---|---|
| `exemplar-learn-ingest` | `15 * * * *` | Mines review-thread roots from eligible repos, resuming from each repo's cursor. |
| `exemplar-learn-extract` | `*/30 * * * *` | Collects finished extraction batches and submits one new batch to the gateway, skipping comments from repositories the config no longer makes eligible. |
| `exemplar-learn-path-events` | `30 2 * * *` | Records paths touched by reverts and incident-linked PRs. |
| `exemplar-learn-cluster` | `30 3 * * *` | Python: clusters embedded extractions. |
| `exemplar-learn-rank` | `30 4 * * *` | Python: scores the latest cluster run into candidates. |
| `exemplar-learn-evals` | `0 5 * * *` | Generates eval cases for new candidates. |
| `exemplar-learn-compile` | `30 5 * * *` | Suspended: compiles candidates to ast-grep or Semgrep in a sandbox ([How to enable the compile CronJob](../guides/operate/compile-cronjob.md)). |
| `exemplar-learn-propose` | `30 6 * * *` | Opens draft PRs for the top candidates, at most 3 open per owner team. |
| `exemplar-learn-demote` | `0 7 * * *` | Nightly auto-demotion. |
| `exemplar-learn-expiry` | `30 7 * * *` | Daily rule expiry. |
| `exemplar-learn-teach` | `45 7 * * *` | Nightly teach-the-bot proposals. |
| `exemplar-learn-digest` | `0 8 * * 1` | Weekly rule-health issues per owner. |
| `exemplar-learn-export` | `15 8 * * *` | Daily rule exports. |

The worker's own periodic jobs (`reconcile_deliveries`, `sweep_prs`, `poll_reactions` every 30 minutes, `retention`, `refresh_stats`) run on River's maintenance queue on the leader and need no CronJob.

## Setup mode

Until `exemplar-github-app` exists, exemplar-api serves only its probes and, on `127.0.0.1:8081` (`--setup-addr`, the container's `setup` port, behind no Service), the setup page (`internal/setup`). The page answers only requests for its own host (`localhost:8081`, or `127.0.0.1` on its port), against DNS rebinding. It registers the App from a manifest in the config repo owner's App settings (an organization's, or a user's own: it asks GitHub which the owner is), finds its installation and offers the Secret as YAML; the state lives in that pod's memory. After setup, `/ui/setup` checks the installation's GitHub side.

## Merge policy repos

A repo with `approval_mode: merge_policy_check` requires the `exemplar/merge-policy` check, restricted to the review app as its source, in its ruleset in place of a code-owner review requirement, and keeps a break-glass bypass actor for incidents. Exemplar never sets that actor. The check counts an approval only from someone with write access to the repo. A `native_approve` repo that requires approvals must dismiss stale approvals on push, or the branch-rules gate refuses to approve.

## Known limits

- **Kubernetes 1.29 or later**, for native sidecars (written against 1.33).
- **Egress by host name** needs an FQDN-aware CNI policy on top; the shipped policies match addresses.
- **The compile CronJob** needs unprivileged user namespaces.
- **The App private key is in the worker.** It mints scoped tokens per job, but the key could mint any flavor, including config-writer. A token-minting service that holds the key and hands out only the worker's flavors would close this. The model's tools themselves make no network calls.
- **The tunnel's edge rules are outside the repo**, and nothing in the cluster can check them.
- **Sizing** is a starting point; re-size from your own monorepo benchmark.
