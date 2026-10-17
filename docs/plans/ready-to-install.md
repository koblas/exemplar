# Ready to install: implementation plan

**Source:** the [plans index](README.md) for how this fits the roadmap. [`phase-0.md`](phase-0.md) holds the Phase 0 plan and the log of every head start up to 2026-10-17. New work is planned and logged here, one dated section per piece.

## 1. Goal and exit criteria

Phases 0–6 are built in head starts, but no one outside the repository can install Exemplar in an afternoon, let alone an hour. This plan makes a first installation short, ordered and checkable.

Exit criteria:

1. An operator with a cluster, a Postgres URL and org-owner rights on GitHub goes from nothing to a reviewed PR **without running anything on their desktop except `kubectl` and `openssl`** (plus `cloudflared` for tunnel builds, until the quick-tunnel and API options in §4.6–4.7 are settled).
2. They type each value once. Nothing they type is a placeholder that passes startup.
3. A setup page tells them what is still wrong (App, installation, webhooks, config repo, providers, search) instead of a pod in CrashLoopBackOff.
4. A tagged release ships the manifests the docs tell people to apply.

## 2. Where we are (2026-10-17)

The README's path ([README.md:28-411](../../README.md)) has 11 steps and says to allow an afternoon. An operator has to:

- create about 11 Secrets one at a time (13 keys, 5 of them random values they generate themselves);
- fill about 15 values in the overlay, plus the exemplar-config repo;
- register the GitHub App by hand, about 30 form fields;
- run 4 `cloudflared` commands and set 2 rules in the Cloudflare dashboard (tunnel builds);
- create the Postgres role, database and extension by hand;
- type the same hostname in about 5 places, and the config repo twice (`CONFIG_REPO`, `CONFIG_GIT_URL`).

Mistakes surface late. Each binary exits with a clear message, but the operator sees CrashLoopBackOff first. Placeholders such as `Iv1.example` and `example-org` pass every startup check. Readiness only pings Postgres, so a wrong App ID, a missing installation or webhooks that never arrive look healthy. A bad deploy key leaves pods in init with no Exemplar message. There are no tags, so `?ref=<version>` can't be followed yet.

Bugs found while reviewing this:

- **GHES doesn't work.** The README and [`examples/k8s/acme/settings.yaml:1-3`](../../examples/k8s/acme/settings.yaml) say to set `EXEMPLAR_GH_WEB_URL` and `EXEMPLAR_GH_API_URL` in `exemplar-settings`. Workloads list their env one by one, with `envFrom` only for `exemplar-otel` and `exemplar-auth`. Neither worker ([`worker.yaml:103-123`](../../deploy/k8s/core/worker.yaml)) nor search ([`search.yaml:101-110`](../../deploy/k8s/core/search.yaml)) gets the two keys, so both still call github.com. Learn is the same according to the review agent; not checked by hand.
- **The export CronJob can't start without the exports app.** It mounts `exemplar-exports-app` without `optional: true` ([`learn.yaml:1175-1179`](../../deploy/k8s/core/learn.yaml)), though the docs call that app optional.
- **Unchecked (from the review agent):** the example `global.yaml` routes to both Claude and Bedrock, so the gateway stops unless one is removed. Several pods run `st.Migrate` at once at startup; check that goose locks.

## 3. What other systems do

From their current docs (fetched 2026-10-17). These are the techniques that most reduce install friction, in order.

| Technique | Who does it | Exemplar today |
|---|---|---|
| Create the GitHub App from the web UI with the [manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest) | [Atlantis](https://www.runatlantis.io/docs/access-credentials.html) (`/github-app/setup`), [Sourcegraph](https://sourcegraph.com/docs/admin/code_hosts/github) site admin | ~30 fields by hand |
| Generate bootstrap secrets, with a command to read them back | [Argo CD](https://argo-cd.readthedocs.io/en/stable/getting_started/) `argocd-initial-admin-secret`, GitLab root password, `abctl local credentials`, Supabase `generate-keys.sh` | 5 random values by hand |
| One install command, few required values | Argo CD and [cert-manager](https://cert-manager.io/docs/installation/) release `install.yaml`; Gitea, Temporal, ARC Helm | 15 values, no release |
| Health page showing what's misconfigured | [Coder `/health`](https://coder.com/docs/admin/monitoring/health-check) | readiness = DB ping |
| One value drives the rest | GitLab `global.hosts.domain` | hostname in ~5 places |
| Local trial path | [Airbyte `abctl`](https://docs.airbyte.com/platform/deploying-airbyte/abctl), GitLab kind values, Coder embedded Postgres | `components/postgres` only |
| Migrations run for you | Temporal pre-install hooks, Sourcegraph migrator | built (on startup) |
| `existingSecret` for every credential | ARC, Coder, Gitea, Temporal | built (Secrets by name) |
| Sizing presets | GitLab reference architectures, Coder validated architectures, Loki modes, Airbyte low-resource | one size: 2 × 500Gi SSD |
| Upgrade guide, compatibility matrix | Sourcegraph, Gitea, cert-manager, Argo CD | one paragraph |
| Preflight and support bundles | [troubleshoot.sh](https://troubleshoot.sh/docs/), `coder support bundle` | none |

Not worth copying: an Operator (GitLab says its own is "only suitable for specific scenarios in production use", and it wraps their Helm chart), and a bundled production Postgres (GitLab removed its bundled databases in 19.0; Temporal bundles none). None of the items above needs Helm.

## 4. Design

### 4.1 Nothing to run on the desktop

**Decision:** there is no `exemplar init`. Setup happens in the cluster, in `exemplar-api`, the first time it starts.

**Why:** `exemplar` is two things to a reader: the CLI people install on their laptops (`login`, `review`, `try`, `validate`), and the Docker Hub image `koblas/exemplar` that runs every server. A command named `exemplar init` leaves them asking where to run it: on the laptop, in a pod with `kubectl exec`, or as a Job. A desktop command would also need cluster credentials and the GitHub org owner's session at once. The projects that make this easiest (Atlantis, Sourcegraph, Coder, Argo CD) put first-run setup in the server and its web UI, and keep their CLI for users.

So:

- The **CLI** is for people using Exemplar. Install docs never mention it. Its README section moves to the user docs.
- The **server** does setup. The only desktop tools an install needs are `kubectl` and `openssl` (plus `cloudflared` for tunnel builds, until §4.7).
- **Naming in docs:** "the `exemplar` CLI" and "the server image". Open question (§6): rename the image `koblas/exemplar-server` before the first tag, while it's cheap, and stop shipping the CLI inside it if nothing in the cluster calls it.

### 4.2 One root key instead of five random Secrets

The operator creates one Secret, `exemplar-root-key` (`key`, at least 32 bytes, one `openssl rand` command). Each binary derives the keys it needs with HKDF-SHA256 and a fixed label:

| Derived key | Label | Replaces |
|---|---|---|
| feedback link key | `exemplar/link/v1` | `exemplar-api`/`link-key` |
| session key | `exemplar/session/v1` | `exemplar-api`/`session-key` |
| gateway token | `exemplar/gateway-token/v1` | `exemplar-gateway`/`token` |
| search token | `exemplar/search-token/v1` | `exemplar-search`/`token` |
| pseudonym key | `exemplar/pseudonym/v1` | `exemplar-pseudonym`/`key` |

- **An explicit key file wins.** If the old per-key Secret is mounted, the binary uses it. Existing installs keep working, and one key can be rotated without rotating the rest.
- **Why not generate Secrets in the cluster:** no service account calls the Kubernetes API today ([`serviceaccounts.yaml:1-2`](../../deploy/k8s/core/serviceaccounts.yaml)), and RBAC can't limit `create` on Secrets by name. A Job that can create Secrets can overwrite any Secret in the namespace.
- **Why not store them in Postgres:** search doesn't connect to Postgres. And the pseudonym key must not live beside the pseudonymised data.
- **Cost:** a pod that mounts the root key can derive every internal key. Today the worker already holds the link key and both service tokens. The new exposure is the session and pseudonym keys reaching gateway, search and webhook. Mitigation: mount the root key only where a derived key is needed (not webhook), and treat a leaked root key like a leaked session key.
- **Rotation:** rotating the root key logs everyone out and changes every pseudonym, which breaks joins on older data. The upgrade guide says so. To rotate one key, mount its explicit file.

### 4.3 GitHub App from the setup page

When `exemplar-api` starts without GitHub App credentials, it runs in **setup mode**:

1. `/setup` is served on a **separate setup port** that no Service, tunnel or Ingress routes. The operator reaches it with `kubectl port-forward deploy/exemplar-api 8081` and opens `http://localhost:8081/setup`. Being able to port-forward into the namespace is the authorisation, so nobody who reaches the public hostname first can take the App's private key. This avoids a Jenkins-style setup token printed to the logs, which would break the rule against logging credentials. To check: the manifest flow's `redirect_url` is a browser redirect, so a `localhost` URL should work. Confirm it with a real org before building on it.
2. `/setup` builds the App manifest from the configured hostname: webhook URL, callback URL, the 7 repository permissions plus Members: Read, Contents write for exemplar-config, and the 8 events (now README step 4). It posts the manifest to `github.com/organizations/<org>/settings/apps/new` with a `state` value. To check: whether the manifest schema can turn on the device flow (`exemplar login`). If it can't, the page links to the App's settings for that one toggle.
3. GitHub redirects back to the setup port with a `code`. The API checks `state` and exchanges the code at `POST /app-manifests/{code}/conversions` (within the hour) for the App ID, private key, webhook secret, client ID and client secret.
4. The page shows those as one ready-to-run `kubectl create secret` command and a downloadable Secret YAML, then links to "Install on your org". The operator applies the Secret and the pods restart. This is Atlantis's model: it keeps the cluster free of RBAC and of credentials in Postgres.
5. Once credentials are present, setup mode is off and the setup port closes.

- **Installation id:** the worker finds it from the App (`GET /app/installations`, the one for the configured org). `EXEMPLAR_GH_INSTALLATION_ID` becomes optional, for Apps installed on more than one org.
- **Exports app:** the same flow, as a second button, with its narrower permissions.
- **Later:** store the credentials in Postgres, encrypted with a key derived from the root key, so no restart is needed. That needs search to get the App token another way (it has no database), so it isn't in the first cut.

### 4.4 Type each value once

The overlay needs three values: the **hostname**, the **config repo** (`owner/name`) and the **image version**.

- `EXEMPLAR_API_URL`, the webhook URL, the OAuth callback URL and the tunnel hostnames derive from the hostname. Check whether webhook and API can share it by path; if not, derive `hooks.<host>`.
- `EXEMPLAR_CONFIG_GIT_URL` derives from the config repo and the GitHub host.
- The App ID, client ID and installation id come from §4.3.
- `exemplar-auth` defaults `ALLOWED_ORGS` to the config repo's owner.
- **Config repo access:** sync it with the GitHub App instead of a deploy key, so `exemplar-config-git` (`ssh`, `known_hosts`) goes away. To check: git-sync's GitHub App auth flags and the version that added them.
- **Placeholders fail startup.** `example-org`, `Iv1.example`, App ID `0` and `*.invalid` hosts are rejected by name.

### 4.5 Setup and health page

`/ui/setup` (admin only after setup) runs the checks that readiness can't, each with what to do when it fails:

- App credentials work (`GET /app` with the App JWT).
- The App is installed on the org, and covers exemplar-config and the eligible repos.
- Webhooks arrive: the last delivery from `GET /app/hook/deliveries`, with its status, compared with the last event the webhook receiver stored.
- The config repo synced and validates.
- Each provider in `global.yaml` answers through the gateway.
- Search is ready, with index coverage.
- Migrations are at the latest version.

The same checks are served as JSON (`/v1/setup/checks`) for the CLI and for scripts. Liveness and readiness don't change: they must not depend on GitHub or the providers.

### 4.6 Release and install artifacts

- Tag the first release. Attach rendered `install-tunnel.yaml` and `install-direct.yaml` with image digests pinned, so the docs' `?ref=<version>` works and Kustomize users can use the remote base.
- A **quickstart overlay** (`examples/k8s/quickstart`): `components/postgres`, a small search volume, and resource requests that fit a laptop's kind cluster. GitHub still needs a public webhook URL. Evaluate Cloudflare quick tunnels, which need no account, for trials only.
- Sizing: `small` and `standard` patches (search volume, worker concurrency, requests).

### 4.7 Docs in layers

Split the README's install section into: **Quickstart** (kind, about 15 minutes) → **Production install** (external Postgres, ingress or a named tunnel, sizing, Bedrock workload identity) → **Upgrades** (per-version notes, migration rules, rotation) → **Reference** (`deploy/README.md`, every setting and Secret). Spell out the Cloudflare WAF and Access rules exactly, as commands or API calls rather than prose. Fix the stale Helm references in the spec (`technical-spec.md:230`, `:1958`) and the `exemplar-orchestrator` runbook entry (`:1897`).

### 4.8 What the install looks like after

1. Create the namespace, the Postgres URL Secret and the root key (two commands), or use `components/postgres` for a trial.
2. Copy the example overlay and set the hostname, the config repo and the version.
3. `kubectl apply -k`.
4. `kubectl port-forward deploy/exemplar-api 8081`, open `http://localhost:8081/setup`, create the App, install it, and apply the Secret the page gives you.
5. Add the model provider key, then open `/ui/setup` until every check passes.

Before: 9 required Secrets, 13 keys, about 15 values, about 30 App fields. After: 4 Secrets (database, root key, GitHub App, provider), plus `exemplar-cloudflared` for tunnel builds; 3 values; no App fields.

## 5. Work items

| # | Item | Section | Status |
|---|---|---|---|
| 1 | Fix GHES env wiring and the optional exports-app mount | §2 | not started |
| 2 | Reject placeholder settings at startup | §4.4 | not started |
| 3 | Root key with HKDF derivation; explicit files still win | §4.2 | not started |
| 4 | Setup mode and the GitHub App manifest flow in exemplar-api | §4.3 | not started |
| 5 | Installation id discovered from the App | §4.3 | not started |
| 6 | Derive URLs from one hostname; config git URL from the repo | §4.4 | not started |
| 7 | Config repo sync through the GitHub App | §4.4 | not started (check git-sync first) |
| 8 | `/ui/setup` checks and `/v1/setup/checks` | §4.5 | not started |
| 9 | First tag with rendered install manifests | §4.6 | not started |
| 10 | Quickstart overlay and sizing patches | §4.6 | not started |
| 11 | Docs split into quickstart, production, upgrades, reference | §4.7 | not started |
| 12 | Confirm goose locks concurrent migrations | §2 | not started |

Items 1, 2 and 12 are small and independent. Item 4 depends on 6 for the manifest's URLs. Item 11 goes last.

## 6. Open questions

- Rename the image `koblas/exemplar-server` before the first tag? And does anything in the cluster run the CLI from the image?
- One hostname or two? Webhook and API on one host by path is simpler to set up, but a separate webhook host is easier to put behind different edge rules.
- Is a root key that gateway and search can use to derive the session and pseudonym keys acceptable to Security, or should the pseudonym key stay a separate Secret?
- Store the App credentials in Postgres (no restart) in a later cut, or stay with show-and-apply?
- Does the manifest flow accept a `localhost` redirect for the port-forwarded setup page, and can the manifest turn on the device flow? Both need a real org to confirm.

## 7. Log

Each piece of work adds a dated section here: what was built, the decisions taken, how it is tested, and what is still missing.

### Install experience review (2026-10-17)

Compared the install path with about 16 projects that install on Kubernetes (§3), from their current docs. Found the two manifest bugs in §2. Decided against a desktop `exemplar init` (§4.1). Nothing in the code or manifests changed.
