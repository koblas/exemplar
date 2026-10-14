# Deploying Exemplar

Kubernetes manifests for the services (spec §2.2, §13) and the images they run. Nothing here has been applied to a cluster. Locally:
- `go test ./deploy` checks the manifests against the code;
- `kubectl kustomize` renders the base (48 resources), and kubeconform v0.7.0 `-strict` for Kubernetes 1.33 found all valid;
- both images build, and run with a read-only root as UID 65532 with every engine present.

CI repeats the build, render and schema check (the `images` job).

## Layout

| Path | What |
|---|---|
| `docker/Dockerfile` | The Go services image: every `cmd/` binary, plus git, universal-ctags, ast-grep 0.45.1, Semgrep 1.172.0, unshare and prlimit. Base images are pinned by digest, ast-grep by checksum. Built for linux/amd64 only. |
| `docker/learning.Dockerfile` | The Python learning image (`learning/`: cluster, rank), with dependencies from `uv.lock`. |
| `k8s/base/` | Plain manifests with a `kustomization.yaml`. Render with `kubectl kustomize deploy/k8s/base`. |
| `manifests_test.go` | Checks the manifests against the code (below). |

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

Every pod:
- runs as UID 65532 with a read-only root filesystem, no capabilities, no privilege escalation and RuntimeDefault seccomp, so it passes the namespace's `restricted` Pod Security level;
- has no service account token, since nothing calls the Kubernetes API;
- has an `emptyDir` for `/tmp` and any other path it writes.

The `exemplar-config` checkout comes from a git-sync v4.7.1 sidecar with a read-only deploy key:
- **Services** use a native sidecar (`initContainers` with `restartPolicy: Always`), whose startup probe holds the main container until the first sync.
- **CronJobs** use a one-time init container.

## What an installation supplies

**Images.** The release pipeline builds both images and pins them in an overlay. Match the names the base renders (the base already rewrote `exemplar` to its placeholder registry):

```yaml
images:
  - name: registry.example.invalid/koblas/exemplar
    newName: <registry>/exemplar
    digest: sha256:<digest it built>
  - name: registry.example.invalid/koblas/exemplar-learning
    newName: <registry>/exemplar-learning
    digest: sha256:<digest it built>
```

The base's all-zero digests make an unpinned deploy fail to pull.

**Settings.** An overlay patches the `exemplar-settings` ConfigMap: `EXEMPLAR_CONFIG_REPO`, `EXEMPLAR_CONFIG_GIT_URL`, `EXEMPLAR_GH_APP_ID`, `EXEMPLAR_GH_INSTALLATION_ID`, `EXEMPLAR_EXPORTS_APP_ID`, `EXEMPLAR_API_URL`, `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` (service callers of `/v1/evaluate`; both empty turns them off) and `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID` (GitHub sign-in; leave it out for OIDC only). It also sets:
- the webhook and API Ingress hosts, classes and TLS secrets, and org SSO in front of the API (an IdP-aware proxy or ingress auth annotations);
- the search volume size (from the T-0.4 benchmark);
- the storage class.

**Secrets**, created outside this repository from the KMS-held values. They're referenced by name only:

| Secret | Keys | Used by |
|---|---|---|
| `exemplar-database` | `url` | every binary but search |
| `exemplar-github-app` | `private-key`, `webhook-secret` | worker, search, learn; webhook (`webhook-secret` only) |
| `exemplar-exports-app` | `private-key` (the separate exports app: contents and pull requests write on opted-in repos only, never installed on exemplar-config) | learn `export` |
| `exemplar-gateway` | `token` | gateway, worker, learn |
| `exemplar-providers` | `anthropic-api-key` | gateway |
| `exemplar-search` | `token` (at least 32 bytes) | search, worker |
| `exemplar-config-git` | `ssh`, `known_hosts` | every git-sync |
| `exemplar-pseudonym` | `key` (at least 32 bytes) | ingest, path-events |
| `exemplar-api` | `link-key`, `session-key` (each at least 32 bytes), `oauth-client-secret` | api; worker (`link-key` only) |

**Merge policy repos.** A repo with `approval_mode: merge_policy_check` requires the `exemplar/merge-policy` check, restricted to the review app as its source, in its ruleset in place of a code-owner review requirement. It keeps a break-glass bypass actor for incidents. Exemplar never sets that actor.

**Sign-in.** exemplar-api's sign-in sources follow Coder's settings. The `exemplar-auth` ConfigMap holds them, and the API reads it through `envFrom`.
- **GitHub OAuth** is on with `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID`. It requires `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` or `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true`, and takes `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL` for GHES. Repo access is each person's GitHub permission.
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
- **Rendering:** every manifest in `k8s/base` is listed in `kustomization.yaml`, and no YAML anchors (kustomize mishandles them).
- **Pods and containers:**
  - pods run non-root with RuntimeDefault seccomp and no token;
  - containers are read-only, drop all capabilities, have CPU and memory requests and limits, and a writable `/tmp`;
  - images are either kustomize-pinned or digest-pinned.
- **References:** every volume, ConfigMap key and Secret resolves to a defined or documented one.
- **Workloads:**
  - each long-running workload's selector matches its pods;
  - it listens on the port it exposes;
  - its probes are `/healthz` and `/readyz` on that port;
  - `terminationGracePeriodSeconds` outlasts the shutdown deadlines read from the binary's source and args.
- **Selectors:** every Service, PodDisruptionBudget and NetworkPolicy selects a workload; each workload has its own NetworkPolicy; the webhook's only egress is Postgres.
- **Environment:** every `EXEMPLAR_*` variable is one the Go or Python code actually reads.

Devenv has no kubectl, Kustomize or kubeconform. These ran with OrbStack's kubectl (`kubectl kustomize` renders locally and contacts no cluster) and kubeconform through `go run`, as CI does:

```bash
kubectl kustomize deploy/k8s/base > rendered.yaml
```

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
- **The ingress namespace.** The webhook's ingress rule allows the `ingress-nginx` namespace; change it to match the cluster's controller.
- **Sizing.** Worker, search and storage requests are starting points. Re-size from the Phase 0 monorepo benchmark (T-0.4).
- **Debian packages** float within the pinned base snapshot. Rebuild on a new base digest to update them.
- **The gateway** marks itself unready and starts shutting down at the same moment. A request routed in that window is retried by the caller.
