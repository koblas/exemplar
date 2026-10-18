# Deploying Exemplar

Kubernetes manifests for the services (spec §2.2, §13) and the images they run. Nothing here has been applied to a cluster. Locally:
- `go test ./deploy` checks the manifests against the code;
- `kubectl kustomize` renders each build (the default `base`, 53 resources; `direct`, 50; both ways in, 57; `base` with in-cluster Postgres, 57), and kubeconform v0.7.0 `-strict` for Kubernetes 1.33 found all valid;
- both images build, and run with a read-only root as UID 65532 with every engine present.

CI repeats the build, render and schema check (the `images` job).

## Layout

| Path | What |
|---|---|
| `docker/Dockerfile` | The Go services image: every `cmd/` binary, plus git, universal-ctags, ast-grep 0.45.1, Semgrep 1.172.0, unshare and prlimit. Base images are pinned by digest, ast-grep by checksum. Built for linux/amd64 only. |
| `docker/learning.Dockerfile` | The Python learning image (`learning/`: cluster, rank), with dependencies from `uv.lock`. |
| `k8s/core/` | Plain manifests for everything but inbound, with a `kustomization.yaml` (and the image pins). Not built alone. |
| `k8s/components/cloudflare-tunnel/` | Inbound through a Cloudflare Tunnel: the `exemplar-cloudflared` Deployment, its routes, and the NetworkPolicies that let it reach the webhook and the API. |
| `k8s/components/ingress/` | Inbound through an Ingress controller: the two Ingresses and the NetworkPolicies that admit `ingress-nginx`. |
| `k8s/components/postgres/` | **Optional** in-cluster Postgres 16 with pgvector, for installations without a managed database. In no shipped build; see [In-cluster Postgres](#in-cluster-postgres). |
| `k8s/base/` | **The default build:** core plus the tunnel. Render with `kubectl kustomize deploy/k8s/base`. |
| `k8s/direct/` | Core plus the Ingresses, with no tunnel. Render with `kubectl kustomize deploy/k8s/direct`. |
| `manifests_test.go` | Checks the manifests against the code (below). |
| `../examples/k8s/acme/` | An example installation overlay: the default build, Docker Hub images, settings, sign-in, tunnel routes and search storage. The Ingress and Postgres pieces are commented out. CI renders it. |

## Inbound

GitHub and people reach exemplar-webhook and exemplar-api one of three ways. Core's NetworkPolicies admit nothing to either; each component adds its own allow (`exemplar-webhook-from-tunnel`, `exemplar-webhook-from-ingress`, and the same for the API), so a build admits exactly the sources it ships.

| Build | How | Admits |
|---|---|---|
| `base` (**default**) | `exemplar-cloudflared` dials out to Cloudflare on 7844 (QUIC, falling back to HTTP/2 over TCP). Cloudflare balances between its 2 replicas. | Only `exemplar-cloudflared` pods |
| `direct` | Ingresses on the cluster's controller | Only the `ingress-nginx` namespace |
| `base` + `components/ingress`, in an overlay | Both | Both |

The tunnel is locally managed: routing is in the `exemplar-cloudflared` ConfigMap, so it's reviewed with the rest of the manifests rather than set in the dashboard.
- The rules match by path, not host name, so the ConfigMap is the same for every installation: only `^/webhook$` reaches the webhook, as the Ingress's `Exact` path did, and everything else reaches the API. Only the host names an installation routes to the tunnel (`cloudflared tunnel route dns`) reach it at all.
- The tunnel id comes from the `exemplar-cloudflared` Secret (`tunnel-id`), as `cloudflared tunnel run`'s argument.
- cloudflared reads the file at start, so restart the Deployment after changing it.

cloudflared runs as its own Deployment, never a sidecar. The tunnel credentials stay out of the webhook and API pods, and the webhook's egress stays Postgres only.

With the tunnel, Cloudflare does the work the Ingress did, and an installation must set it up there:
- **IN-1:** a WAF rule limits `/webhook` to GitHub's hook ranges, and the Access application bypasses that path.
- **IN-2:** an Access application puts org SSO in front of the host.

With a separate webhook host (`EXEMPLAR_WEBHOOK_URL`), each rule goes on its own host instead.

Cloudflare ends TLS at its edge and sees requests in clear text. The webhook still checks each delivery's HMAC. Its rejection log's `remote` is a cloudflared pod's address, not GitHub's.

## In-cluster Postgres

By default Postgres is outside the cluster: the installation supplies its URL in the `exemplar-database` Secret. For evaluation or a small installation without a managed database, add `components/postgres` to an overlay of `base` or `direct`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - <path>/deploy/k8s/base
components:
  - <path>/deploy/k8s/components/postgres
```

It adds `exemplar-postgres`: a one-replica StatefulSet of `pgvector/pgvector:0.8.0-pg16` (the image the store tests use, pinned by digest), a Service on 5432, a 50Gi volume on the default storage class, and a NetworkPolicy that admits only the webhook, API, gateway, worker and learning pods. The installation then:
- creates the `exemplar-postgres` Secret (`password`), the superuser's password;
- sets `exemplar-database`'s `url` to `postgres://exemplar:<password>@exemplar-postgres.exemplar.svc:5432/exemplar` (URL-encode the password);
- sets the volume's size and class in its overlay, as for search.

The binaries run migrations at start, as with an outside database. The app connects as the superuser, which `CREATE EXTENSION vector` needs.

Details:
- It runs as the image's `postgres` user (UID 999) under the namespace's `restricted` level, with a read-only root and emptyDirs for the socket directory, `/tmp` and a 256 MiB `/dev/shm`. `PGDATA` is a subdirectory of the volume, since initdb refuses a mount root holding `lost+found`.
- Probes run `pg_isready` over TCP, so the pod isn't ready while first-start init runs on the socket only. A startup probe allows 10 minutes of crash recovery before liveness counts.
- SIGTERM alone is Postgres's smart shutdown, which waits for every pooled client to leave. A preStop `pg_ctl stop --mode=fast --timeout=30` stops it first, under a 45 s grace.
- There is no PodDisruptionBudget: with one replica it would only block node drains. A drain or reschedule is downtime.

It is not a production database: one replica, no replication, no backups, no point-in-time recovery, no TLS between the pods and Postgres, and the app holds superuser. Use a managed Postgres 16 with pgvector for production, or an operator such as CloudNativePG.

## Workloads

| Binary | Kind | Replicas | Shutdown → grace | Notes |
|---|---|---|---|---|
| `exemplar-webhook` | Deployment + Service + Ingress | 2 | 15 s → 30 s | Public for GitHub; egress to Postgres only |
| `exemplar-api` | Deployment + Service + Ingress | 2 | 15 s → 30 s | Public for people, behind org SSO at the ingress; egress to Postgres and GitHub OAuth |
| `exemplar-gateway` | Deployment + Service | 2 | 25 s → 40 s | The only holder of provider keys |
| `exemplar-worker` | Deployment | 2 (×4 reviews) | 80 s → 120 s | Runs River's periodic jobs on its leader |
| `exemplar-search` | StatefulSet + 2 Services | 2, on different nodes | 25 s → 45 s | Local SSD per replica (`local-ssd`, 500Gi placeholder) |
| `exemplar-learn` | 11 CronJobs | — | — | ingest, path-events, extract, evals, compile (suspended), propose, demote, expiry, teach, digest, export |
| `exemplar-learning` | 2 CronJobs | — | — | cluster, rank |
| `exemplar-postgres` | StatefulSet + Service | 1 | 30 s → 45 s | `components/postgres` only. pgvector 0.8.0 on Postgres 16, pinned by digest. In from the Postgres clients on 5432; nothing out. `pg_isready` probes |
| `exemplar-cloudflared` | Deployment | 2 | 20 s → 30 s | Tunnel builds only. cloudflared 2026.9.3, pinned by digest. Egress to Cloudflare on 7844 and the webhook and API. `/healthcheck` (liveness) and `/ready` (an edge connection) on port 2000 |

Every pod:
- runs as UID 65532 with a read-only root filesystem, no capabilities, no privilege escalation and RuntimeDefault seccomp, so it passes the namespace's `restricted` Pod Security level;
- has no service account token, since nothing calls the Kubernetes API;
- has an `emptyDir` for `/tmp` and any other path it writes.

The `exemplar-config` checkout comes from a git-sync v4.7.1 sidecar, cloning `$(EXEMPLAR_GH_WEB_URL)$(EXEMPLAR_CONFIG_REPO)` over HTTPS as the review app (`--github-app-*`, from `exemplar-github-app`):
- **Services** use a native sidecar (`initContainers` with `restartPolicy: Always`), whose startup probe holds the main container until the first sync.
- **CronJobs** use a one-time init container.

## What an installation supplies

**Images.** `.github/workflows/images.yml` pushes both images to Docker Hub as `koblas/exemplar` on every push to main and each `v*` tag. The services image is tagged `sha-<short>`, `main` and `<version>`, and the learning image gets the same tags with a `learning-` prefix. Its run summary prints each digest and the entry below. The base keeps the logical names `exemplar` and `exemplar-learning`. Kustomize matches images by name alone, and both images share one repository, so renaming them in the base would leave an overlay unable to pin them apart. Each release attaches `install-tunnel.yaml` and `install-direct.yaml`, rendered by [`render-release.sh`](render-release.sh) with both digests pinned; an overlay can use one as its resource. An overlay of the base maps each name instead, as [`examples/k8s/acme`](../examples/k8s/acme) does:

```yaml
images:
  - name: exemplar
    newName: docker.io/koblas/exemplar
    digest: sha256:<services digest>
  - name: exemplar-learning
    newName: docker.io/koblas/exemplar
    digest: sha256:<learning digest>
```

The base's all-zero digests make an unpinned deploy fail to pull.

**Settings.** An overlay patches the `exemplar-settings` ConfigMap with two values: `EXEMPLAR_CONFIG_REPO` (`owner/name`; the App is registered on its owner, and sign-in defaults to that org's members) and `EXEMPLAR_API_URL` (exemplar-api's public `https://` base; GitHub posts to it plus `/webhook`, and the OAuth callback is `/oauth/callback`). Optional: `EXEMPLAR_WEBHOOK_URL` for a separate webhook host; `EXEMPLAR_GH_WEB_URL` and `EXEMPLAR_GH_API_URL` for GitHub Enterprise Server (default github.com's); `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` (service callers of `/v1/evaluate`; both empty turns them off). Everything about the GitHub App is in the `exemplar-github-app` Secret, not in settings. Startup refuses values left from the examples (`internal/envcfg`). The overlay also sets:
- inbound, for Ingress builds: the Ingress host (the same on both), class and TLS secret, and org SSO in front of everything but `/webhook` (an IdP-aware proxy or ingress auth annotations);
- the search volume size (from the T-0.4 benchmark) and storage class.

**Setup mode.** Until `exemplar-github-app` exists, exemplar-api serves only its probes and, on `127.0.0.1:8081` (`--setup-addr`, the container's `setup` port, behind no Service), the setup page (`internal/setup`). Port-forward one exemplar-api pod to 8081 and keep it for the whole flow: the state lives in that pod's memory. The page answers only requests for its own host (`localhost:8081`), against DNS rebinding. The page registers the App from a manifest, finds its installation and offers the Secret as YAML. The other workloads reference the Secret without `optional`, so they wait for it and start once it's applied; exemplar-api needs a restart. After setup, `/ui/setup` checks the installation's GitHub side.

**Secrets**, created outside this repository from the KMS-held values. They're referenced by name only:

| Secret | Keys | Used by |
|---|---|---|
| `exemplar-database` | `url` | every binary but search |
| `exemplar-github-app` | `app-id`, `installation-id`, `client-id`, `client-secret`, `private-key`, `webhook-secret`: written by the setup page | worker, search, learn, every git-sync; api (`app-id`, `client-id`, `client-secret`, `private-key`); webhook (`webhook-secret`) |
| `exemplar-exports-app` | `app-id`, `private-key` (optional: the separate exports app: contents and pull requests write on opted-in repos only, never installed on exemplar-config) | learn `export` |
| `exemplar-root-key` | `key` (at least 32 bytes; `openssl rand -hex 32`) | api, worker, gateway, search, learn. The link, session, gateway, search and pseudonym keys derive from it with HKDF-SHA256 (`internal/envcfg`). An explicit `EXEMPLAR_LINK_KEY_FILE`, `EXEMPLAR_SESSION_KEY_FILE`, `EXEMPLAR_GATEWAY_TOKEN`, `EXEMPLAR_SEARCH_TOKEN` or `EXEMPLAR_PSEUDONYM_KEY` still wins, for rotating one key alone. Rotating the root key signs everyone out and changes every pseudonym. |
| `exemplar-providers` | `anthropic-api-key` | gateway |
| `exemplar-cloudflared` | `credentials.json` and `tunnel-id` (from `cloudflared tunnel create`) | cloudflared, tunnel builds only |
| `exemplar-postgres` | `password` | exemplar-postgres, `components/postgres` only |

**Merge policy repos.** A repo with `approval_mode: merge_policy_check` requires the `exemplar/merge-policy` check, restricted to the review app as its source, in its ruleset in place of a code-owner review requirement. It keeps a break-glass bypass actor for incidents. Exemplar never sets that actor.

**Sign-in.** exemplar-api's sign-in sources follow Coder's settings. The `exemplar-auth` ConfigMap holds them, and the API reads it through `envFrom`.
- **GitHub OAuth** is on with the App's client id (`exemplar-github-app`). `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` defaults to the config repo's owner; `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true` opens it to anyone, and takes `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL` for GHES. Repo access is each person's GitHub permission.
- **OpenID Connect** is on with `EXEMPLAR_OIDC_ISSUER_URL`, `EXEMPLAR_OIDC_CLIENT_ID` and the `exemplar-oidc` Secret (`client-secret`). Repo access comes only from `EXEMPLAR_OIDC_GROUP_REPOS`, which maps IdP groups to repo patterns. The full list of settings is in `internal/oidc`.
- Register `https://<api>/oauth/callback` (GitHub) and `https://<api>/oidc/callback` (OIDC) as redirect URIs.
- For `exemplar login --oidc`, register a public client with the device flow enabled and set it as `EXEMPLAR_OIDC_CLI_CLIENT_ID`.
- **Connect GitHub.** With both sources on, an OIDC user connects their GitHub account once after signing in, so feedback roles and rule-owner checks use their real GitHub login. `EXEMPLAR_OIDC_GITHUB_LINK` is `required` (the default), `optional` or `off`. To use GitHub only for connecting, not as a sign-in button, set `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false`. People switch accounts at `/ui/connect-github`.

**Telemetry.** Traces and metrics go wherever the standard OTel variables say, and nowhere when none are set (`internal/telemetry`). Every container reads the optional ConfigMap and Secret `exemplar-otel` through `envFrom`. Put `OTEL_EXPORTER_OTLP_ENDPOINT` (or the per-signal `_TRACES_` and `_METRICS_` endpoints), `OTEL_METRICS_EXPORTER` and `OTEL_METRIC_EXPORT_INTERVAL` in the ConfigMap. Put credentials such as `OTEL_EXPORTER_OTLP_HEADERS=Authorization=...` in the Secret. Only OTLP over HTTP (`http/protobuf`) is built in. The `allow-otel` NetworkPolicy opens port 4318 to pods labelled `app.kubernetes.io/component: opentelemetry-collector`. A collector elsewhere needs an overlay, unless it is reached on 443 by pods that already have HTTPS egress.

**Bedrock**, when `global.yaml` routes to it, uses workload identity: annotate the `exemplar-gateway` service account for the cloud's identity federation. Don't add AWS keys to the Secret.

## Checking locally

```bash
go test ./deploy
```

It checks:
- **Builds:** every check runs on each of four builds: `base`, `direct`, `base` with the ingress component, and `base` with the Postgres component.
- **Rendering:**
  - every manifest in each directory a build uses is listed in its `kustomization.yaml`;
  - no YAML anchors (kustomize mishandles them);
  - no patches, since the test reads manifests without kustomize;
  - no object defined twice.
- **Pods and containers:**
  - pods run non-root with RuntimeDefault seccomp and no token;
  - containers are read-only, drop all capabilities, have CPU and memory requests and limits, and a writable `/tmp`;
  - images are either kustomize-pinned or digest-pinned.
- **References:** every volume, ConfigMap key and Secret resolves to a defined or documented one.
- **Workloads:**
  - each long-running workload's selector matches its pods;
  - it listens on the port it exposes;
  - its probes are `/healthz` and `/readyz` on that port (cloudflared: `/healthcheck` and `/ready` on its metrics port; Postgres: `pg_isready` on 5432, with a preStop fast shutdown);
  - `terminationGracePeriodSeconds` outlasts the shutdown deadlines read from the binary's source and args.
- **Selectors:** every Service, PodDisruptionBudget and NetworkPolicy selects a workload, and each workload has its own NetworkPolicy.
- **Inbound:**
  - the build has the Ingresses it should (none in the default);
  - each Ingress and tunnel route goes to a Service port;
  - the tunnel's routes end in a catch-all;
  - the webhook and the API admit exactly the build's sources;
  - the webhook's only egress is Postgres, whichever policies select it.
- **Environment:** every `EXEMPLAR_*` variable is one the Go or Python code actually reads.

Devenv has no kubectl, Kustomize or kubeconform. These ran with OrbStack's kubectl (`kubectl kustomize` renders locally and contacts no cluster) and kubeconform through `go run`, as CI does:

```bash
kubectl kustomize deploy/k8s/base > rendered.yaml
```

Use `deploy/k8s/direct` for the Ingress build. CI also renders `base` with the ingress component, and `base` with the Postgres component.

```bash
kubeconform -strict -summary -kubernetes-version 1.33.0 rendered.yaml
```

Build the images (BuildKit; nothing is pushed):

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/Dockerfile -t exemplar:dev .
```

A schema check is still not proof of admission or runtime health.

## Requirements and risks

- **Kubernetes 1.29 or later**, for native sidecars (written against 1.33).
- **Egress by host name.** NetworkPolicy matches addresses, so "HTTPS out" is any public address on 443 (and 22 for git-sync). The spec's per-host lists (GitHub only for search, providers only for the gateway, GitHub and Jira for the worker) need an FQDN-aware policy from the CNI, such as Cilium `toFQDNs`, on top of these.
- **The compile CronJob is suspended.** It runs model-written rules only inside an `unshare --net --map-root-user` sandbox. Unprivileged user namespaces are normally refused under RuntimeDefault seccomp at the restricted level. Enable it once the cluster provides user namespaces (for example `hostUsers: false` with a seccomp profile that allows them). Never turn off `--require-sandbox` instead.
- **The App private key is in the worker.** It mints scoped tokens per job, but the key could mint any flavor, including config-writer, which spec §5 keeps away from agent processes. A token-minting service that holds the key and hands out only the worker's flavors would close this. The model's tools themselves make no network calls.
- **The ingress namespace.** In Ingress builds, `exemplar-webhook-from-ingress` and `exemplar-api-from-ingress` admit the `ingress-nginx` namespace; change it to match the cluster's controller.
- **The tunnel's egress** is any public address on 7844, like the other address-only rules. Cloudflare's edge ranges (or an FQDN policy for `region1.v2.argotunnel.com` and `region2.v2.argotunnel.com`) would narrow it.
- **The tunnel's edge rules are outside the repo.** Without the WAF rule, anyone can post to `/webhook`, though deliveries are still HMAC-checked. Without Access, the API's own sign-in is the only gate. Both are installation steps, and nothing in the cluster can check them.
- **Sizing.** Worker, search and storage requests are starting points. Re-size from the Phase 0 monorepo benchmark (T-0.4).
- **Debian packages** float within the pinned base snapshot. Rebuild on a new base digest to update them.
- **The gateway** marks itself unready and starts shutting down at the same moment. A request routed in that window is retried by the caller.
