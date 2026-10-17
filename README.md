# Exemplar

Exemplar is a self-correcting AI code reviewer for GitHub. It reviews pull requests against rules your teams own in a git repository (`exemplar-config`). It learns new rules from your reviewers' comments, and demotes rules people say are wrong. It can auto-approve low-risk changes once a set of deterministic gates pass.

- **Architecture:** [`ARCHITECTURE.md`](ARCHITECTURE.md) (components and workflows as built, with diagrams)
- **Spec:** [`docs/spec/technical-spec.md`](docs/spec/technical-spec.md) (`§` references point here)
- **PRD:** [`docs/spec/prd.md`](docs/spec/prd.md) (`FR-…` requirement ids)
- **Roadmap and status:** [`docs/plans/README.md`](docs/plans/README.md), with a log of every step in [`docs/plans/phase-0.md`](docs/plans/phase-0.md)
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

This installs Exemplar into one Kubernetes namespace (`exemplar`), reviewing repositories in one GitHub organization. Allow an afternoon, most of it spent registering the GitHub App and creating secrets.

The steps:
1. Check the prerequisites.
2. Choose the image version.
3. Create the `exemplar-config` repository.
4. Register the GitHub App.
5. Prepare Postgres.
6. Create the secrets.
7. Write a Kustomize overlay.
8. Render, validate and apply it.
9. Verify the installation.
10. Install the CLI.
11. Add the optional pieces.

Nothing below touches a cluster until step 8 tells you to.

### 1. Prerequisites

| You need | Notes |
|---|---|
| Kubernetes **1.29+** | Native sidecars (git-sync) need 1.29. The manifests are written against 1.33. The namespace runs at the `restricted` Pod Security level. |
| A way in, with TLS | Two public hosts: one for GitHub's webhooks, one for people (exemplar-api). **Default:** a Cloudflare Tunnel (a Cloudflare account and a zone for the hosts); the cluster then needs no public ingress at all. **Or** an ingress controller (`deploy/k8s/direct`, which assumes `ingress-nginx`), or both. See step 7. |
| A storage class for local SSD | `exemplar-search` asks for 500Gi per replica on `local-ssd`. Change both in the overlay. |
| **Postgres 16** with **pgvector** | Reachable from the cluster on 5432. Use a read replica for Grafana if you add it. |
| Docker Hub access | The cluster pulls the published images from `docker.io/koblas/exemplar`. To run images you build yourself, see [Building your own images](#building-your-own-images). |
| A model provider | An Anthropic API key, or AWS Bedrock through workload identity (see `global.yaml` `providers`). |
| GitHub org owner rights | To register and install the GitHub App. GitHub Enterprise Server works too (see step 7). |
| Tooling on your machine | Docker (`docker buildx imagetools` looks up image digests), `kubectl`, Go 1.27 (for the CLI and checks), `openssl`, `git`, and `cloudflared` (to create the tunnel, unless you use `deploy/k8s/direct`). |

### 2. Choose the image version

Exemplar ships two images, both in `docker.io/koblas/exemplar`. Both run as UID 65532 with a read-only root filesystem, and both are built for `linux/amd64`.

| Image | Tags | What |
|---|---|---|
| Services | `<version>`, `<major>.<minor>`, `main`, `sha-<short>` | Every Go service, plus git, universal-ctags, ast-grep, Semgrep and clang-format |
| Learning | `learning-<version>`, `learning-main`, … | The Python learning jobs (`cluster`, `rank`) |

CI publishes them (`.github/workflows/images.yml`) on every push to `main` and for every release. Use a release version: releases are listed on GitHub, with what changed in [CHANGELOG.md](CHANGELOG.md) (see [CONTRIBUTING.md](CONTRIBUTING.md#releasing) for how they are cut). The manifests pin images by **digest**, never by tag, so record both digests for step 7:

```bash
docker buildx imagetools inspect koblas/exemplar:<version> --format '{{json .Manifest.Digest}}'
```

```bash
docker buildx imagetools inspect koblas/exemplar:learning-<version> --format '{{json .Manifest.Digest}}'
```

Each publish run's summary also prints both digests.

### 3. Create the `exemplar-config` repository

`exemplar-config` is the source of truth for:
- the rules;
- which repos are reviewed, and how;
- model routing;
- retention;
- eval cases.

Exemplar only reads it, through git-sync, and changes it only by opening pull requests.

1. **Create the repository** in your org, for example `acme/exemplar-config`. Copy [`examples/exemplar-config`](examples/exemplar-config) into it, including `.github/workflows/`.
2. **Edit `global.yaml`.** Set `providers` to the models you can use: drop `bedrock` if you only use Anthropic directly, or the reverse. Leave `auto_approve: false` for now.
3. **Opt in a first repo.** Rename `repos/exemplar-sandbox/playground.yaml` to `repos/<org>/<repo>.yaml` for that repo. Start it with `shadow: true`: Exemplar reviews and records everything but posts nothing.
4. **Check the eligibility denylist.** Review `platform/eligibility.yaml`. Repos listed there are never sent to a model.
5. **Edit `CODEOWNERS`.** Make rule owners and the platform team review their files.
6. **Validate**, using the CLI from step 10, or `go run ./cmd/exemplar` from this repo:

   ```bash
   exemplar validate path/to/exemplar-config
   ```

7. **Create a deploy key for git-sync.** It must be read-only, without a passphrase:

   ```bash
   ssh-keygen -t ed25519 -N '' -C exemplar-config-git -f exemplar-config-git
   ```

   Add `exemplar-config-git.pub` to the repo as a read-only deploy key. Keep the private key for step 6.

### 4. Register the GitHub App

Register one **GitHub App** for reviewing (spec §5) under your org: *Settings → Developer settings → GitHub Apps → New*.

**Endpoints:**
- **Webhook URL:** `https://<webhook-host>/webhook`.
- **Webhook secret:** a random value:

  ```bash
  openssl rand -hex 32
  ```

- **Callback URL:** `https://<api-host>/oauth/callback`, for GitHub sign-in.
- **Device flow:** turn on **Enable Device Flow**, for `exemplar login`.

**Repository permissions:**

| Permission | Level |
|---|---|
| Metadata | Read |
| Contents | Read |
| Pull requests | Read & write |
| Checks | Read & write |
| Code scanning alerts | Read & write |
| Issues | Read & write |
| Administration | Read |

**Organization permissions:** Members: Read.

**Contents: write on exemplar-config.** Exemplar opens config PRs (candidates, demotions, owner actions) in exemplar-config. Grant Contents **Read & write** at the app level, and **install the app on `exemplar-config` too**. Exemplar mints tokens down-scoped per job, and only the config-writer token ever uses the write.

**Subscribe to events:**
- Pull request
- Pull request review
- Pull request review comment
- Pull request review thread
- Issue comment
- Check run
- Code scanning alert
- Push

Then:
- Generate a **private key** (a `.pem` file) and note the **App ID**.
- Generate an **OAuth client secret** and note the **Client ID**.
- Install the app on your organization, choosing the opted-in repos and `exemplar-config`. Note the **installation id**, the number at the end of the installation's settings URL.

**Optional: the exports app.** Register a second app only if repos will opt into rule exports to `AGENTS.md`, `CLAUDE.md`, Copilot or Cursor files (spec §10.2).
- Permissions: Contents **Read & write**, Pull requests **Read & write**. Subscribe to no events.
- Install it only on the repos that opt in, **never on exemplar-config**.

### 5. Prepare Postgres

Create a database and a role for Exemplar. The services apply their own schema migrations at startup (`db/migrations`, embedded in the binaries). There is no separate migration step.

```sql
CREATE ROLE exemplar LOGIN PASSWORD '<password>';
CREATE DATABASE exemplar OWNER exemplar;
\c exemplar
CREATE EXTENSION IF NOT EXISTS vector;  -- as a superuser, if the exemplar role cannot
```

The connection URL goes into the `exemplar-database` Secret in the next step, for example:

```text
postgres://exemplar:<password>@db.internal:5432/exemplar?sslmode=require
```

### 6. Create the secrets

Secrets are never in this repository or in rendered manifests. Create them in the `exemplar` namespace from your secret manager. The namespace itself comes from the manifests, so create it first:

```bash
kubectl create namespace exemplar
```

The keys below must be at least 32 random bytes. Generate each one like this:

```bash
openssl rand -base64 48 > link-key
```

| Secret | Keys | Contents |
|---|---|---|
| `exemplar-database` | `url` | The Postgres URL from step 5 |
| `exemplar-github-app` | `private-key`, `webhook-secret` | The app's `.pem`, and the webhook secret from step 4 |
| `exemplar-api` | `link-key`, `session-key`, `oauth-client-secret` | Two random keys, and the app's OAuth client secret |
| `exemplar-gateway` | `token` | A random key, shared by the gateway and its callers |
| `exemplar-search` | `token` | A random key |
| `exemplar-providers` | `anthropic-api-key` | Your Anthropic key (omit it if you use only Bedrock) |
| `exemplar-config-git` | `ssh`, `known_hosts` | The deploy key from step 3, and GitHub's SSH host keys |
| `exemplar-pseudonym` | `key` | A random key, for pseudonymizing reviewers in mined comments |
| `exemplar-exports-app` | `private-key` | Only with the exports app |
| `exemplar-oidc` | `client-secret` | Only with OIDC sign-in |
| `exemplar-cloudflared` | `credentials.json` | Only with the Cloudflare Tunnel (the default): the tunnel's credentials file (step 7) |

For example:

```bash
kubectl -n exemplar create secret generic exemplar-github-app --from-file=private-key=exemplar.private-key.pem --from-file=webhook-secret=webhook-secret
```

```bash
ssh-keyscan github.com > known_hosts
```

```bash
kubectl -n exemplar create secret generic exemplar-config-git --from-file=ssh=exemplar-config-git --from-file=known_hosts=known_hosts
```

### 7. Write a Kustomize overlay

Pick how traffic gets in:

| Build | Inbound | Public in the cluster |
|---|---|---|
| `deploy/k8s/base` (**default**) | A Cloudflare Tunnel: `exemplar-cloudflared` dials out to Cloudflare | Nothing |
| `deploy/k8s/direct` | Ingresses on your controller | The two Ingress hosts |
| `deploy/k8s/base` plus the `components/ingress` component | Both, on different host names | The two Ingress hosts |

**Create the tunnel** (default) with `cloudflared` on your machine. Routes stay in the repo's ConfigMap, not the dashboard:

```bash
cloudflared tunnel login
```

```bash
cloudflared tunnel create exemplar
```

```bash
cloudflared tunnel route dns exemplar exemplar-webhook.acme.com
```

```bash
cloudflared tunnel route dns exemplar exemplar.acme.com
```

```bash
kubectl -n exemplar create secret generic exemplar-cloudflared --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json
```

Then, in the Cloudflare dashboard. These take over from the Ingress's controls (spec §13 IN-1, IN-2), so a tunnel install is not done without them:
- **Webhook host:** a WAF custom rule that blocks any source outside GitHub's `hooks` ranges (`https://api.github.com/meta`). Put **no** Access policy on it: GitHub can't sign in. The webhook still checks every delivery's signature.
- **API host:** an Access application with your org's IdP in front of the whole host (org SSO), unless OIDC sign-in alone is enough for you.

Cloudflare ends TLS at its edge, so it sees requests in clear text, webhook payloads included. If that's not acceptable, use `deploy/k8s/direct`.

The build is complete except for your installation's values. Put those in an overlay. [`examples/k8s/acme`](examples/k8s/acme) is a working one: copy it to wherever you keep deployment config, then replace the `acme` values.

| File | Sets |
|---|---|
| `kustomization.yaml` | The build, the image digests from step 2, and the search volume's storage class and size. The Ingress and in-cluster Postgres pieces are there, commented out. |
| `settings.yaml` | The `exemplar-settings` ConfigMap: the config repo, the GitHub App, the API URL |
| `auth.yaml` | The `exemplar-auth` ConfigMap: who may sign in (see *Sign-in* below) |
| `tunnel.yaml` | The `exemplar-cloudflared` ConfigMap: the tunnel id and host names. Keep the webhook's path rule and the catch-all. |

Its `kustomization.yaml`, without the comments:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - github.com/koblas/exemplar//deploy/k8s/base?ref=<version>
# For both ways in, also:
# components:
#   - github.com/koblas/exemplar//deploy/k8s/components/ingress?ref=<version>
images:
  - name: exemplar
    newName: docker.io/koblas/exemplar
    digest: sha256:<services digest>
  - name: exemplar-learning
    newName: docker.io/koblas/exemplar
    digest: sha256:<learning digest>
patches:
  - path: settings.yaml
  - path: auth.yaml
  - path: tunnel.yaml
  - target: {kind: StatefulSet, name: exemplar-search}
    patch: |-
      - {op: replace, path: /spec/volumeClaimTemplates/0/spec/storageClassName, value: local-ssd}
      - {op: replace, path: /spec/volumeClaimTemplates/0/spec/resources/requests/storage, value: 500Gi}
```

In this repository it points at `../../../deploy/k8s/base` instead, so CI renders it against the current manifests.

For an Ingress (`deploy/k8s/direct`, or the ingress component for both ways in), uncomment the Ingress patches in the example: they set each Ingress's host, TLS secret and class. With `deploy/k8s/direct`, also drop `tunnel.yaml`.

The base pins both images to all-zero digests, so an overlay that forgets to pin one fails to pull rather than running something unknown.

**Other patches the overlay usually needs:**
- **Org SSO:** put it in front of the API host, unless you use OIDC sign-in: Cloudflare Access with the tunnel; an IdP-aware proxy or ingress auth annotations with an Ingress.
- **Ingress controller namespace** (Ingress builds only): if your controller doesn't run in `ingress-nginx`, uncomment the example's NetworkPolicy patches and set the namespace.
- **GitHub Enterprise Server:** set `EXEMPLAR_GH_WEB_URL` (`https://<host>/`) and `EXEMPLAR_GH_API_URL` (`https://<host>/api/v3/`) in `exemplar-settings`. Set `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL` in `exemplar-auth`. Reviews and the search mirror still clone from github.com ([ready-to-install.md](docs/plans/ready-to-install.md) item 13).

#### Sign-in

People sign in to exemplar-api with GitHub OAuth, OpenID Connect, or both. The settings follow [Coder's](https://coder.com/docs/admin/users/oidc-auth) and live in `exemplar-auth`.

- **GitHub OAuth.** It's on when `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID` is set.
  - It requires `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` (comma-separated), or `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE=true`.
  - Each person sees the repos their GitHub permissions allow.
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

### 8. Render, validate and apply

Render the overlay locally and check it before it reaches the cluster:

```bash
kubectl kustomize overlays/acme > rendered.yaml
```

```bash
go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0 -strict -summary -kubernetes-version 1.33.0 rendered.yaml
```

From this repository, `go test ./deploy` also checks every build's manifests against the code. That covers ports, probes, shutdown deadlines, every environment variable, the security context, and who may reach the webhook and the API. The default build renders 53 resources, `direct` 50, and both ways in 57.

Then apply it. Name the context and namespace explicitly:

```bash
kubectl --context <your-cluster> -n exemplar apply -f rendered.yaml
```

### 9. Verify the installation

1. **Pods.** Every pod reaches `Running` and `Ready`:

   ```bash
   kubectl --context <your-cluster> -n exemplar get pods
   ```

   Each service answers `/healthz` (liveness) and `/readyz` (readiness, including the database). git-sync must finish its first sync before the main containers start. With the tunnel, `exemplar-cloudflared` is ready once it holds a connection to Cloudflare, and the tunnel shows as healthy in the dashboard.
2. **Webhooks.** In the GitHub App's *Advanced → Recent deliveries*, the ping and later deliveries show `202`.
3. **Sign-in.** Open `https://<api-host>/ui/` and sign in. You should see the rules from `exemplar-config`.
4. **A first review.** Open a pull request in the shadow repo from step 3. The worker reviews it and stores the result, but posts nothing in shadow mode. Look for it on the web UI and on the Grafana boards (step 11). When the findings look right, set `shadow: false` in that repo's file in `exemplar-config` and merge. The next push gets the `exemplar` check run and review comments.
5. **Feedback.** On a posted finding, reply `/exemplar wrong <why>` (or use its feedback link). The finding's page in the web UI then shows the event.

If something is wrong, start with the logs. Every service logs JSON to stdout, and never logs credentials or code:

```bash
kubectl --context <your-cluster> -n exemplar logs deploy/exemplar-worker
```

### 10. Install the CLI

**Download.** Tagged releases attach archives for linux (amd64, arm64) and darwin (arm64, amd64) with `SHA256SUMS`. Download one from the GitHub release, check its checksum, and put `exemplar` on your `PATH`.

**Build from source.** The CLI embeds the tree-sitter grammars, so it needs cgo and a C compiler:

```bash
CGO_ENABLED=1 go build -o exemplar ./cmd/exemplar
```

**Sign in**, using the client id from step 4 (or `--oidc` with the OIDC CLI client):

```bash
exemplar login --api https://exemplar.acme.com --client-id <Client ID>
```

**Review your working tree before opening a PR.** It exits 1 when there are findings:

```bash
exemplar review
```

### 11. Optional pieces

**Grafana.** Provision [`dashboards/`](dashboards/): boards for rule health, adjudication, operations, lanes, the auto-approve audit, counter-metrics, shift-left and telemetry, plus alert rules. Point the `exemplar-pg` datasource at a read-only role on the read replica. The grants are listed in [`dashboards/README.md`](dashboards/README.md).

**Telemetry.** Create an `exemplar-otel` ConfigMap with `OTEL_EXPORTER_OTLP_ENDPOINT`, and an optional Secret with `OTEL_EXPORTER_OTLP_HEADERS`. Every container picks them up. Only OTLP over HTTP is supported. The `allow-otel` NetworkPolicy opens port 4318 to an in-cluster OpenTelemetry Collector.

**Config CI.** The workflows copied in step 3 (`.github/workflows/eval.yml` in `exemplar-config`):
- validate every config PR;
- run the adversarial set;
- evaluate changed rules through `/v1/evaluate` (`exemplar eval --remote`).

The eval job needs repository variables `EXEMPLAR_API_URL`, `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE`. It also needs secrets for an Auth0 client whose only permission is `evaluate:<org>/exemplar-config`. On the exemplar-api side, set `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` in `exemplar-settings`.

**Rule exports.** Register the exports app (step 4), add its private key as `exemplar-exports-app`, and set `EXEMPLAR_EXPORTS_APP_ID` in `exemplar-settings`. Then add `exports.targets` to each repo's file in `exemplar-config`.

**Auto-approve** stays off until the Phase 5 exit criteria are met (spec §16). Turn it on per repo in `exemplar-config` (`auto_approve` in the repo file, and `auto_approve: true` in `global.yaml`).
- Repos whose rulesets require code-owner review use `approval_mode: merge_policy_check`.
- They require the `exemplar/merge-policy` check instead of counting Exemplar's approval, and keep a break-glass bypass actor.

**The compile CronJob** is suspended. It runs model-written rules only inside a user-namespace sandbox, which the `restricted` Pod Security level normally refuses. Enable it only once the cluster provides unprivileged user namespaces; see [`deploy/README.md`](deploy/README.md).

### Upgrading

Look up the new release's digests (step 2), pin them in the overlay, and move its `ref=` to the same version. Then render, validate and apply.
- **Migrations** run automatically when the new pods start. They are forward-only in production.
- **Shutdown** is graceful. Each pod's termination grace period outlasts its shutdown deadline, so in-flight reviews finish or are retried by River.

### Building your own images

You only need this to run changes that aren't published, or to pull from your own registry. Build both images from the repository root (BuildKit, `linux/amd64` only):

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/Dockerfile -t exemplar:dev .
```

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/learning.Dockerfile -t exemplar-learning:dev .
```

Tag and push both to your registry, and look up each digest as in step 2. In the overlay, set each image's `newName` to your repository, for example `<registry>/exemplar` and `<registry>/exemplar-learning`, and its `digest` to what you pushed.

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
