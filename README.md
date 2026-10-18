# Exemplar

Exemplar is a self-correcting AI code reviewer for GitHub. It reviews pull requests against rules your teams own in a git repository (`exemplar-config`). It learns new rules from your reviewers' comments, and demotes rules people say are wrong. It can auto-approve low-risk changes once a set of deterministic gates pass.

- **Architecture:** [`ARCHITECTURE.md`](ARCHITECTURE.md) (components and workflows as built, with diagrams)
- **Spec:** [`docs/spec/technical-spec.md`](docs/spec/technical-spec.md) (`§` references point here)
- **PRD:** [`docs/spec/prd.md`](docs/spec/prd.md) (`FR-…` requirement ids)
- **Roadmap and status:** [`docs/plans/README.md`](docs/plans/README.md); current work in [`docs/plans/ready-to-install.md`](docs/plans/ready-to-install.md)
- **Deployment reference:** [`deploy/README.md`](deploy/README.md)

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

---

## INSTALLATION

Exemplar installs into one Kubernetes namespace (`exemplar`) and reviews repositories in one GitHub organization. On the desktop you need only `kubectl` and `openssl` (and `cloudflared` for the default tunnel). There is no install CLI: the first time exemplar-api starts, it serves a setup page that registers the GitHub App for you.

- **[Quickstart](#quickstart)**: a trial on one node with an in-cluster Postgres. About 20 minutes.
- **[Production install](#production-install)**: your own Postgres, sizing and edge rules.
- **[Upgrading](#upgrading)**, and [`deploy/README.md`](deploy/README.md) for every setting, Secret and workload.

### Quickstart

You need Kubernetes 1.29+, org-owner rights on a GitHub org, an Anthropic API key, and a Cloudflare account with a zone for one host name (GitHub must reach the webhook from the internet). Pick a host, for example `exemplar.acme.com`.

1. **Create `exemplar-config`.** Make a repository in your org, for example `acme/exemplar-config`, from [`examples/exemplar-config`](examples/exemplar-config) (including `.github/workflows/`). In `global.yaml`, drop the `bedrock` provider. Rename `repos/exemplar-sandbox/playground.yaml` to `repos/<org>/<repo>.yaml` for a first repo, and keep `shadow: true` (Exemplar reviews and records, but posts nothing).
2. **Create the tunnel**, routed to your host:

   ```bash
   cloudflared tunnel login
   ```

   ```bash
   cloudflared tunnel create exemplar
   ```

   ```bash
   cloudflared tunnel route dns exemplar exemplar.acme.com
   ```

3. **Create the namespace and the Secrets** that aren't GitHub's. The root key derives every internal key (`deploy/README.md`, "Secrets"):

   ```bash
   kubectl create namespace exemplar
   ```

   ```bash
   kubectl -n exemplar create secret generic exemplar-root-key --from-literal=key="$(openssl rand -hex 32)"
   ```

   ```bash
   PGPASS=$(openssl rand -hex 24); kubectl -n exemplar create secret generic exemplar-postgres --from-literal=password="$PGPASS" && kubectl -n exemplar create secret generic exemplar-database --from-literal=url="postgres://exemplar:$PGPASS@exemplar-postgres.exemplar.svc:5432/exemplar"
   ```

   ```bash
   kubectl -n exemplar create secret generic exemplar-providers --from-literal=anthropic-api-key=<your key>
   ```

   ```bash
   kubectl -n exemplar create secret generic exemplar-cloudflared --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json --from-literal=tunnel-id=<tunnel-id>
   ```

4. **Write the overlay.** Copy [`examples/k8s/quickstart`](examples/k8s/quickstart). In `settings.yaml` set your config repo and `https://exemplar.acme.com`. In `kustomization.yaml`, replace the resource with the release's `install-tunnel.yaml` URL and drop the `images` list (the file pins them); see [Releases](#releases).
5. **Apply it:**

   ```bash
   kubectl --context <your-cluster> apply -k quickstart/
   ```

   exemplar-api and Postgres start. The other pods wait with `secret "exemplar-github-app" not found` until the next step.
6. **Register the GitHub App** from the setup page:

   Port-forward one exemplar-api pod, and keep that same pod for the whole step (the setup state lives in its memory):

   ```bash
   kubectl --context <your-cluster> -n exemplar port-forward "$(kubectl --context <your-cluster> -n exemplar get pod -l app.kubernetes.io/name=exemplar-api -o name | head -1)" 8081
   ```

   Open <http://localhost:8081/setup>, then:
   - **Create it on GitHub.** GitHub shows the App with its permissions, events, webhook and callback filled in; rename it if you like, and confirm.
   - **Install it** on your org, choosing the repos Exemplar reviews and `exemplar-config`, then **I've installed it**.
   - For `exemplar login`, tick **Enable Device Flow** in the App's settings (the page links there).
   - Save `exemplar-github-app.yaml`, apply it, and restart exemplar-api:

   ```bash
   kubectl --context <your-cluster> apply -f exemplar-github-app.yaml
   ```

   ```bash
   kubectl --context <your-cluster> -n exemplar rollout restart deployment/exemplar-api
   ```

   Keep the page open until the Secret is applied: the App's key lives only in that pod's memory. If the pod restarts after GitHub created the App but before you saved the Secret, delete that App in your org's settings on GitHub and start the step again.
7. **Check it.** Once `kubectl -n exemplar get pods` shows everything ready, open `https://exemplar.acme.com/ui/setup` and sign in with GitHub. It checks the App's installation, its access to `exemplar-config`, GitHub's delivery log and the events stored, and says what to fix. Then open a pull request in the shadow repo: the review appears in the web UI.

For a trial that's enough. The quickstart's Postgres has no backups or replication, the tunnel's edge has no SSO in front of the API, and everything runs one replica.

### Production install

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

**`exemplar-config`.** As in the quickstart, and also review `platform/eligibility.yaml` (repos listed there are never sent to a model) and `CODEOWNERS` (rule owners and the platform team review their files). Validate with `exemplar validate path/to/exemplar-config` ([the CLI](#the-cli)).

**Postgres.** Create a database and role. The services apply their migrations at startup, one pod at a time:

```sql
CREATE ROLE exemplar LOGIN PASSWORD '<password>';
CREATE DATABASE exemplar OWNER exemplar;
\c exemplar
CREATE EXTENSION IF NOT EXISTS vector;  -- as a superuser, if the exemplar role cannot
```

Put its URL in `exemplar-database` (`url`), for example `postgres://exemplar:<password>@db.internal:5432/exemplar?sslmode=require`.

**Secrets.** Create them from your secret manager; none are in this repository or the rendered manifests. Before applying: `exemplar-database`, `exemplar-root-key`, `exemplar-providers` (unless Bedrock only) and, with the tunnel, `exemplar-cloudflared`. The setup page produces `exemplar-github-app` (and `exemplar-exports-app`). `exemplar-oidc` is only for OIDC sign-in. The full table is in [`deploy/README.md`](deploy/README.md).

**The overlay.** Copy [`examples/k8s/acme`](examples/k8s/acme):

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

Then register the App from the setup page (quickstart step 6) and work through `/ui/setup` (step 7). From this repository, `go test ./deploy` also checks every build against the code: ports, probes, shutdown deadlines, every environment variable, security contexts and network paths.

#### Sign-in

People sign in to exemplar-api with GitHub OAuth, OpenID Connect, or both. The settings follow [Coder's](https://coder.com/docs/admin/users/oidc-auth) and live in the `exemplar-auth` ConfigMap.

- **GitHub OAuth** is on once `exemplar-github-app` exists (it holds the App's client id and secret).
  - By default only members of the config repo's owner may sign in. Set `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` (comma-separated) to change that, or `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true`.
  - Each person sees the repos their GitHub permissions allow.
  - For GitHub Enterprise Server, set `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL`.
- **OpenID Connect** (Okta, Auth0, Azure AD, Keycloak, …).
  - Set `EXEMPLAR_OIDC_ISSUER_URL` and `EXEMPLAR_OIDC_CLIENT_ID`, and create the `exemplar-oidc` Secret.
  - Register `https://<api-host>/oidc/callback` as the redirect URI.
  - Repo access comes **only** from `EXEMPLAR_OIDC_GROUP_REPOS`, which maps IdP groups to repos, for example `{"engineering": ["acme/*"]}`.
  - Restrict sign-in with `EXEMPLAR_OIDC_EMAIL_DOMAIN` and `EXEMPLAR_OIDC_ALLOWED_GROUPS`.
  - For `exemplar login --oidc`, add a public client with the device flow enabled and set it as `EXEMPLAR_OIDC_CLI_CLIENT_ID`.
  - The full list of settings is in [`internal/oidc`](internal/oidc/oidc.go).
- **Both.**
  - Each OIDC user connects their GitHub account once after signing in, so feedback and rule ownership use their real GitHub login.
  - `EXEMPLAR_OIDC_GITHUB_LINK` is `required` (the default), `optional` or `off`.
  - `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false` keeps GitHub as the connect step only, with no GitHub sign-in button.

#### Going live

1. **A first review.** Open a pull request in the shadow repo. The worker reviews it and stores the result, but posts nothing. When the findings look right, set `shadow: false` in that repo's file in `exemplar-config` and merge. The next push gets the `exemplar` check run and review comments.
2. **Feedback.** On a posted finding, reply `/exemplar wrong <why>` (or use its feedback link). The finding's page in the web UI shows the event.

If something is wrong, start with `/ui/setup`, then the logs. Every service logs JSON to stdout, and never logs credentials or code:

```bash
kubectl --context <your-cluster> -n exemplar logs deploy/exemplar-worker
```

#### Optional pieces

**Grafana.** Provision [`dashboards/`](dashboards/): boards for rule health, adjudication, operations, lanes, the auto-approve audit, counter-metrics, shift-left and telemetry, plus alert rules. Point the `exemplar-pg` datasource at a read-only role on the read replica. The grants are listed in [`dashboards/README.md`](dashboards/README.md).

**Telemetry.** Create an `exemplar-otel` ConfigMap with `OTEL_EXPORTER_OTLP_ENDPOINT`, and an optional Secret with `OTEL_EXPORTER_OTLP_HEADERS`. Every container picks them up. Only OTLP over HTTP is supported. The `allow-otel` NetworkPolicy opens port 4318 to an in-cluster OpenTelemetry Collector.

**Config CI.** The workflows in `exemplar-config` (`.github/workflows/eval.yml`):
- validate every config PR;
- run the adversarial set;
- evaluate changed rules through `/v1/evaluate` (`exemplar eval --remote`).

The eval job needs repository variables `EXEMPLAR_API_URL`, `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE`. It also needs secrets for an Auth0 client whose only permission is `evaluate:<org>/exemplar-config`. On the exemplar-api side, set `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` in `exemplar-settings`.

**Rule exports.** On the setup page (port-forward to exemplar-api during setup), also create the exports app, install it only on the repos that opt in (**never on exemplar-config**), and apply `exemplar-exports-app.yaml`. Then add `exports.targets` to each repo's file in `exemplar-config`. To add it after setup, register it by hand with Contents and Pull requests **Read & write** and no events, and create `exemplar-exports-app` with `app-id` and `private-key`.

**Auto-approve** stays off until the Phase 5 exit criteria are met (spec §16). Turn it on per repo in `exemplar-config` (`auto_approve` in the repo file, and `auto_approve: true` in `global.yaml`).
- Repos whose rulesets require code-owner review use `approval_mode: merge_policy_check`.
- They require the `exemplar/merge-policy` check instead of counting Exemplar's approval, and keep a break-glass bypass actor.

**The compile CronJob** is suspended. It runs model-written rules only inside a user-namespace sandbox, which the `restricted` Pod Security level normally refuses. Enable it only once the cluster provides unprivileged user namespaces; see [`deploy/README.md`](deploy/README.md).

### Releases

Exemplar ships two images in `docker.io/koblas/exemplar`, both running as UID 65532 with a read-only root filesystem, for `linux/amd64`:

| Image | Tags | What |
|---|---|---|
| Services | `<version>`, `<major>.<minor>`, `main`, `sha-<short>` | Every Go service, plus git, universal-ctags, ast-grep, Semgrep and clang-format |
| Learning | `learning-<version>`, `learning-main`, … | The Python learning jobs (`cluster`, `rank`) |

Each GitHub release (what changed is in [CHANGELOG.md](CHANGELOG.md); see [CONTRIBUTING.md](CONTRIBUTING.md#releasing)) attaches `install-tunnel.yaml` and `install-direct.yaml`, rendered with both images pinned by digest. Use one as your overlay's resource:

```yaml
resources:
  - https://github.com/koblas/exemplar/releases/download/<version>/install-tunnel.yaml
```

Building on `github.com/koblas/exemplar//deploy/k8s/base?ref=<version>` instead, pin both digests yourself; the base's all-zero digests fail to pull rather than run something unknown:

```bash
docker buildx imagetools inspect koblas/exemplar:<version> --format '{{json .Manifest.Digest}}'
```

### Upgrading

Point the overlay at the new release's install file (or move `ref=` and both digests to it). Then render, check and apply.
- **Migrations** run when the new pods start, under a lock, so only one pod migrates. They are forward-only in production.
- **Shutdown** is graceful. Each pod's termination grace period outlasts its shutdown deadline, so in-flight reviews finish or are retried by River.
- **Keys.** Rotating the root key signs everyone out and changes every pseudonym, which breaks joins with older mined data. To rotate one key alone, set its explicit Secret (`deploy/README.md`, "Secrets").

### The CLI

**Download.** Tagged releases attach archives for linux (amd64, arm64) and darwin (arm64, amd64) with `SHA256SUMS`. Download one from the GitHub release, check its checksum, and put `exemplar` on your `PATH`.

**Build from source.** The CLI embeds the tree-sitter grammars, so it needs cgo and a C compiler:

```bash
CGO_ENABLED=1 go build -o exemplar ./cmd/exemplar
```

**Sign in** with the App's client id (`client-id` in `exemplar-github-app`), or `--oidc` with the OIDC CLI client:

```bash
exemplar login --api https://exemplar.acme.com --client-id <Client ID>
```

**Review your working tree before opening a PR.** It exits 1 when there are findings:

```bash
exemplar review
```

### Building your own images

You only need this to run changes that aren't published, or to pull from your own registry. Build both images from the repository root (BuildKit, `linux/amd64` only):

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/Dockerfile -t exemplar:dev .
```

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/learning.Dockerfile -t exemplar-learning:dev .
```

Tag and push both to your registry, and look up each digest as in [Releases](#releases). In the overlay, set each image's `newName` to your repository, for example `<registry>/exemplar` and `<registry>/exemplar-learning`, and its `digest` to what you pushed.

---

## Developing

Use the repository's devenv environment: `devenv shell`, or its direnv setup. It provides:
- Go 1.27.2, golangci-lint, ast-grep, Semgrep and universal-ctags;
- Postgres 16 with pgvector;
- Python 3.13 with uv.

The `Makefile` wraps the usual commands; `make help` lists every target. Each mirrors a step in `.github/workflows/ci.yml`.

```bash
make check       # build, vet, lint, format check, tests
```

```bash
make test-race
```

```bash
make ci          # everything ci.yml runs except the image and manifest jobs
```

**Postgres tests** start a pgvector container with testcontainers-go, so they need Docker or Podman. They skip without one unless `EXEMPLAR_REQUIRE_DB=1`. With `EXEMPLAR_REQUIRE_ENGINES=1` (as in CI), engine-dependent tests fail instead of skipping.

**Python** lives in `learning/`:

```bash
make py-check    # or: make -C learning check
```

**Generated query code.** After changing `db/queries`, regenerate `internal/store/queries` with sqlc v1.31.1 (run through `go run`, so nothing to install):

```bash
make sqlc
```

`make images` and `make manifests` mirror CI's images job; they need Docker and kubectl, which devenv does not provide. Outputs (binaries from `make bins`, the replay result, rendered manifests) go to `out/`.

**Layout:**
- `cmd/`: the binaries;
- `internal/`: the packages;
- `db/`: migrations and queries;
- `schema/`: the JSON Schemas for exemplar-config;
- `deploy/`: images and manifests;
- `dashboards/`: Grafana;
- `examples/exemplar-config/`: a starting config;
- `docs/`: the spec, the PRD and the plans.
