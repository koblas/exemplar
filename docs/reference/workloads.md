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
| `base` (default) | `exemplar-cloudflared` dials out to Cloudflare on 7844 (QUIC, falling back to HTTP/2 over TCP). Cloudflare balances between its 2 replicas. | Only `exemplar-cloudflared` pods | 58 |
| `direct` | Ingresses on the cluster's controller | Only the `ingress-nginx` namespace | 55 |
| `base` + `components/ingress` | Both | Both | 62 |
| `base` + `components/postgres` | As `base`, with the in-cluster database | As `base` | 62 |

The tunnel is locally managed: routing is in the `exemplar-cloudflared` ConfigMap, reviewed with the rest of the manifests rather than set in the dashboard. The rules match by path, not host name: only `^/webhook$` reaches the webhook, as the Ingress's `Exact` path does, and everything else reaches the API. Only the host names an installation routes to the tunnel (`cloudflared tunnel route dns`) reach it at all. cloudflared runs as its own Deployment, never a sidecar, so the tunnel credentials stay out of the webhook and API pods and the webhook's egress stays Postgres only. Cloudflare ends TLS at its edge and sees requests in clear text; the webhook still checks each delivery's HMAC, and its rejection log's `remote` is a cloudflared pod's address, not GitHub's. The edge rules an installation adds are on [How to install for production](../guides/install/production.md#4-write-the-overlay).

## Workloads

| Binary | Kind | Replicas | Port | Probes | Shutdown → grace | Notes |
|---|---|---|---|---|---|---|
| `exemplar-webhook` | Deployment + Service + (Ingress) | 2 | 8080 `http`, Service 80 | `/healthz`, `/readyz` | 15 s → 30 s | Public for GitHub at `/webhook`; egress to Postgres only |
| `exemplar-api` | Deployment + Service + (Ingress) | 2 | 8080 `http`, Service 80; 8081 `setup` (loopback, no Service) | `/healthz`, `/readyz` | 15 s → 30 s | Public for people, behind org SSO at the edge; egress to Postgres and GitHub OAuth |
| `exemplar-gateway` | Deployment + Service | 2 | 8080 `http`, Service 80 | `/healthz`, `/readyz` | 25 s → 40 s | The only holder of provider keys; marks itself unready and starts shutting down at the same moment, so a request routed in that window is retried by the caller |
| `exemplar-worker` | Deployment | 2 (×4 reviews each) | 8080 `http` (probes only) | `/healthz`, `/readyz` | 80 s → 120 s | Control: routes events, runs the cheap queues and River's periodic jobs on its leader, and dispatches reviews to runners ([Runners](#runners)) |
| `exemplar-runner` | PodTemplate → a Job per review | One per review in flight | none | none | 60 s grace | `--launcher=kubernetes` only. One review, then exits; no config checkout, no Kubernetes API token |
| `exemplar-search` | StatefulSet + 2 Services | 2, on different nodes | 8080 `http`, Services 8080 and 80 | `/healthz`, `/readyz` | 25 s → 45 s | Local SSD per replica (`local-ssd`, 500Gi placeholder). Optional: an overlay may delete it and empty `EXEMPLAR_SEARCH_URL` ([How to run without exemplar-search](../guides/install/without-search.md)) |
| `exemplar-learn` | 11 CronJobs | | | | | ingest, path-events, extract, evals, compile (suspended), propose, demote, expiry, teach, digest, export |
| `exemplar-learning` | 2 CronJobs | | | | | cluster, rank (Python) |
| `exemplar-postgres` | StatefulSet + Service | 1 | 5432 | `pg_isready` over TCP; startup probe allows 10 min; preStop `pg_ctl stop --mode=fast --timeout=30` | 30 s → 45 s | `components/postgres` only. pgvector 0.8.0 on Postgres 16, pinned by digest. In from the Postgres clients on 5432; nothing out |
| `exemplar-cloudflared` | Deployment | 2 | 2000 metrics | `/healthcheck` (liveness), `/ready` (an edge connection) | 20 s → 30 s | Tunnel builds only. cloudflared 2026.9.3, pinned by digest. Egress to Cloudflare on 7844 and to the webhook and API |

Every pod runs as UID 65532 with a read-only root filesystem, no capabilities, no privilege escalation and RuntimeDefault seccomp, so it passes the namespace's `restricted` Pod Security level; has no service account token, except exemplar-worker's `worker` container (below); and has an `emptyDir` for `/tmp` and any other path it writes. Postgres runs as the image's `postgres` user (UID 999).

### Config sync

The worker (control), gateway, search and learn pods read `exemplar-config` from a git-sync v4.7.1 sidecar, cloning `$(EXEMPLAR_GH_WEB_URL)$(EXEMPLAR_CONFIG_REPO)` over HTTPS as the review app (`--github-app-*`, from `exemplar-github-app`). Services use a native sidecar (`initContainers` with `restartPolicy: Always`) on port 2020 `sync`, whose startup probe holds the main container until the first sync. CronJobs use a one-time init container. Runners have no checkout: exemplar-worker stores each config version it loads in `config_snapshots`, and a runner reads the version its job was dispatched with. A review's `config_version` is the git-sync commit of `exemplar-config` (or `tree-` and a content hash for a directory that is not a git-sync worktree); unused versions are deleted after 30 days.

### Runners

`exemplar-worker serve` dispatches every `pr_review`, `evaluate` and `playground_run` job to a runner, which does that one job and exits. The dispatcher holds the per-repo slot, chooses the config version, and maps the runner's exit to River.

| `serve` flag | Default | Meaning |
|---|---|---|
| `--launcher` | `in-process` | Where runners run: `in-process` (inside the worker pod; the quickstart overlays) or `kubernetes` (a Job per review; the core manifests) |
| `--runner-template` | `exemplar-runner` | The PodTemplate runner Jobs are made from (`kubernetes` only) |
| `--concurrency` | `4` | `pr_review` jobs at once per worker pod: reviews in this process, or runner Jobs in flight |

With `kubernetes`, the worker makes a Job named `exemplar-runner-<job>-<attempt>` from `runner.yaml`'s PodTemplate, adds `run-job --job <id> --config-version <version>` to the `runner` container's args, and polls it. The Job never retries (`backoffLimit: 0`; River retries), has an `activeDeadlineSeconds` of the job's timeout plus 2 minutes (reviews 30 minutes, evaluations 50 minutes, playground runs 1 minute), and is deleted 10 minutes after it ends. Before a Job starts, every earlier Job of the same River job is deleted and waited for (up to 60 s, else the job waits 20 s and tries again), so two runners never review the same job at once. A superseded review's runner sees the cancel on its River row within 5 s and posts its notice; past 30 s its Job is deleted. A job past its timeout has its Job deleted at once. When the worker itself stops, running Jobs are left; River retries the job and the retry deletes them, so a rolling update of the worker restarts the reviews in flight.

A runner gets its GitHub access from a **claim**. Before launching, the worker mints the installation tokens the job needs from the App and seals them (AES-GCM) into a `runner_claims` row:

| Job | Tokens |
|---|---|
| `pr_review` | The PR repo: worker (read, clone), gate and poster |
| `evaluate` (`exemplar review`) | The repo: worker |
| `evaluate`, mode eval (config CI) | `exemplar-config`: worker; and per installation, one read-only token over every eligible repo with a known installation, for the eval cases |
| `playground_run` | The PR repo: worker |

The Job's `EXEMPLAR_RUNNER_NONCE` redeems the claim once: a second redeem, another job's nonce, or a claim past its tokens' expiry gets nothing, and the runner exits 10. The runner asks GitHub only with those tokens, for those repositories; it never holds the App private key. Its tokens are cleared from the row when it exits, and the retention job deletes claims whose tokens expired. Installation tokens last an hour, so no runner job's timeout is longer than 50 minutes.

`exemplar-worker run-job --job ID --config-version V --work-dir DIR` reads the same environment as `serve` but the App's, and no `--config`. Its exit code:

| Exit | Meaning | River |
|---|---|---|
| 0 | Done | Completed |
| 10 | Not a runnable job, the job or its data is gone, or its claim was refused | Cancelled, not retried |
| 11 | Superseded (cancelled remotely) | Cancelled |
| anything else, or no code (evicted, out of memory, past the deadline, node lost) | Failed | Retried |

Only the worker's `worker` container mounts a service account token (a projected volume; `automountServiceAccountToken` stays false on every pod). `rbac.yaml` binds it to a namespaced Role: Jobs (create, get, list, delete), pods (get, list), and the `exemplar-runner` PodTemplate (get). The `exemplar-runner` ServiceAccount has no token and no role. The worker's NetworkPolicy allows the Kubernetes API on 443 and 6443 at private addresses; the `exemplar-runner` NetworkPolicy allows the gateway, search, HTTPS out and Postgres.

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
- **The App private key is in the worker (control).** It mints scoped tokens per job and hands runners only their job's; the key could mint any flavor, including config-writer, so the worker runs no model-facing code with `--launcher=kubernetes`. With `--launcher=in-process`, reviews run in the worker pod, beside the key.
- **GitHub rate limiting is per runner.** Each runner throttles its own requests and backs off on GitHub's secondary limits; reviews of one installation in different runners do not share a budget.
- **The Kubernetes API's address** is the cluster's: the worker's policy allows the private ranges on 443 and 6443; narrow it to the `kubernetes` endpoint in an overlay.
- **The tunnel's edge rules are outside the repo**, and nothing in the cluster can check them.
- **Sizing** is a starting point; re-size from your own monorepo benchmark.
