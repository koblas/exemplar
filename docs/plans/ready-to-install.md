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
| 1 | Fix GHES env wiring and the optional exports-app mount | §2 | done (2026-10-17) |
| 2 | Reject placeholder settings at startup | §4.4 | done (2026-10-17) |
| 3 | Root key with HKDF derivation; explicit files still win | §4.2 | done (2026-10-17) |
| 4 | Setup mode and the GitHub App manifest flow in exemplar-api | §4.3 | done (2026-10-17); not run against real GitHub |
| 5 | Installation id discovered from the App | §4.3 | done (2026-10-17) |
| 6 | Derive URLs from one hostname; config git URL from the repo | §4.4 | done (2026-10-17) |
| 7 | Config repo sync through the GitHub App | §4.4 | done (2026-10-17) |
| 8 | `/ui/setup` checks and `/ui/setup.json` | §4.5 | done (2026-10-17), GitHub checks only |
| 9 | First tag with rendered install manifests | §4.6 | workflow done (2026-10-17); not run in CI; the tag waits for the release PR |
| 10 | Quickstart overlay and sizing patches | §4.6 | done (2026-10-17) |
| 11 | Docs split into quickstart, production, upgrades, reference | §4.7 | done (2026-10-17) |
| 12 | Confirm goose locks concurrent migrations | §2 | done (2026-10-17): it didn't; Migrate now takes an advisory lock |
| 13 | GHES clone URLs for worker, search and learn evals | §7 | done (2026-10-17) |

Items 1, 2 and 12 are small and independent. Item 4 depends on 6 for the manifest's URLs. Item 11 goes last.

## 6. Open questions

- Rename the image `koblas/exemplar-server` before the first tag? And does anything in the cluster run the CLI from the image?
- ~~One hostname or two?~~ Decided 2026-10-17: one host routed by path by default, `EXEMPLAR_WEBHOOK_URL` for a separate one (§7).
- **For Security, together:** is a root key that gateway and search can use to derive the session and pseudonym keys acceptable, or should the pseudonym key stay a separate Secret? And the gateway pod's git-sync sidecar now holds the review App's private key where it held a read-only deploy key (§7, config sync as the App).
- Store the App credentials in Postgres (no restart) in a later cut, or stay with show-and-apply?
- Does the manifest flow accept a `localhost` redirect for the port-forwarded setup page, and can the manifest turn on the device flow? Both need a real org to confirm.
- Setup state lives in one exemplar-api pod. With two replicas, a dropped and restarted `port-forward deployment/exemplar-api` can land on the other pod between "Create" and GitHub's redirect, leaving an App on GitHub whose code was never exchanged. Today's recovery is to delete that App on GitHub and start again; the docs say to port-forward one pod. Running setup with one replica, or keeping the state in Postgres, would remove it.

## 7. Log

Each piece of work adds a dated section here: what was built, the decisions taken, how it is tested, and what is still missing.

### Install experience review (2026-10-17)

Compared the install path with about 16 projects that install on Kubernetes (§3), from their current docs. Found the two manifest bugs in §2. Decided against a desktop `exemplar init` (§4.1). Nothing in the code or manifests changed.

### GHES wiring, optional exports app, migration lock (2026-10-17)

Work items 1 and 12.

- **GHES.** Core `exemplar-settings` now has `EXEMPLAR_GH_WEB_URL` and `EXEMPLAR_GH_API_URL`, both empty (github.com). Every container that calls GitHub reads `EXEMPLAR_GH_API_URL` from it with `optional: true`: worker, search, api, and the learn CronJobs ingest, path-events, evals, compile, propose, demote, expiry, teach, digest and export. Api, teach and digest, which build links, also read `EXEMPLAR_GH_WEB_URL`. extract, cluster, rank, webhook and gateway don't call GitHub. `go test ./deploy` now fails a container that has `EXEMPLAR_GH_APP_ID`, `EXEMPLAR_EXPORTS_APP_ID` or `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID` without the API base, or api/teach/digest without the web base.
- **Exports app.** The export CronJob's `exemplar-exports-app` volume is `optional: true`. `exemplar-learn export` exits 0 with a log line when `EXEMPLAR_EXPORTS_APP_ID` is unset or 0 and no enabled repo configures `exports.targets`. It fails if a repo does, or if the app id is set and the key file is missing (`skipExport`, table-tested; `exports.Repos` is shared with the runner).
- **Migrations.** Goose's provider had no session locker and River's migrator doesn't lock, so webhook, api, worker and learn pods starting together raced. `Store.Migrate` now takes a Postgres advisory lock on its own connection (outside the pool) around both. It polls `pg_try_advisory_lock` instead of blocking in `pg_advisory_lock`: a session blocked in that statement holds a snapshot, and migration 15's `CREATE INDEX CONCURRENTLY` waits for it, so the first version deadlocked in the concurrent test. Tests: `TestMigrateWaitsForLock` (Migrate does nothing while another session holds the lock, then succeeds; fails with the lock removed) and `TestMigrateConcurrent` (8 stores on an empty database; caught the deadlock). `storetest.EmptyURL` gives an unmigrated database.
- **Found, then fixed (item 13).** Clone URLs ignore GHES: worker (`internal/worker/worker.go:113`) and learn evals (`internal/learn/evalgen/model.go:133`) hard-code `https://github.com/`, and search takes `--git-url`, which the manifest doesn't set. On GHES, reviews and the search mirror still clone from github.com.

### GHES clone URLs (2026-10-17)

Work item 13. `gh.CloneURL` builds `<web base><owner>/<repo>.git` from `EXEMPLAR_GH_WEB_URL`, defaulting to github.com. The worker (reviews, evaluations, playground), learn `evals` and `compile`, and the CLI's `eval`, `try` and prechecks use it. Search's `--git-url` defaults to the same variable, and an explicit flag still wins. The manifests give `EXEMPLAR_GH_WEB_URL` to worker, search, evals and compile. `go test ./deploy` requires it there and in api, teach and digest. The README no longer warns that GHES clones go to github.com. Replay fixtures keep their own `clone_url`, defaulting to github.com, because they are test data.


### Placeholder settings fail startup (2026-10-17)

Work item 2. `internal/envcfg` checks settings against the values the manifests ship for the operator to replace: `example-org`, `Iv1.example`, `example.invalid`, `exemplar.example.`, and `<…>` blanks such as the acme overlay's `"<App ID>"` (case-insensitive). `envcfg.Check` joins every failure, so one startup error lists everything still to fill in. Empty values pass; whether a setting is required stays the caller's check. Checked where each binary loads its settings: api (`API_URL`, OAuth client id and allowed orgs, GitHub bases, OIDC issuer), worker, search and learn (app id, installation id, config repo, GitHub bases; the worker also `API_URL`), and learn `export` (exports app id). The check runs in `cmd/`, not in library constructors, so tests' `.invalid` fixtures are unaffected. Tests: `TestPlaceholder`, `TestCheckJoins`, and `TestRunRejectsPlaceholders` (api exits before connecting to anything).

### One root key (2026-10-17)

Work item 3. `envcfg.Keys` resolves the link key, session key, gateway token, search token and pseudonym key. An explicit setting wins (`EXEMPLAR_LINK_KEY_FILE`, `EXEMPLAR_SESSION_KEY_FILE`, `EXEMPLAR_GATEWAY_TOKEN`, `EXEMPLAR_SEARCH_TOKEN`, `EXEMPLAR_PSEUDONYM_KEY`); otherwise the key is HKDF-SHA256 (stdlib `crypto/hkdf`, no salt, 32 bytes) of the file at `EXEMPLAR_ROOT_KEY_FILE` with a fixed label (`exemplar/link/v1` and so on). File keys are raw bytes; tokens and the pseudonym key are hex, since they travel as strings. The root key must be at least 32 bytes and is read once per process.

- **Python.** The cluster job reads the gateway token too, so `exemplar_learning.rootkey` implements the same derivation with `hmac`. Go and Python test one shared vector (root `0123…cdef`, the gateway label), computed independently.
- **Manifests.** The `exemplar-gateway`, `exemplar-search` and `exemplar-pseudonym` Secrets are gone, and `exemplar-api` keeps only `oauth-client-secret`. api, worker, gateway, search and the learn jobs that need a derived key (ingest, path-events, extract, cluster, evals, compile) mount `exemplar-root-key` at `/var/run/secrets/exemplar/root`. The webhook doesn't. Nothing has been released, so the manifests don't keep the old Secrets; the code still honours the explicit settings.
- **Decision (open to Security, §6):** a pod with the root key can derive every internal key, so gateway and search can now derive the session and pseudonym keys. Before, the worker already held the link key and both tokens.
- **Tests:** `TestDeriveVector`, `TestKeys` (derived, explicit wins, neither set, short root, missing explicit file), `TestTokens` (labels give distinct keys), `TestRootReadOnce`; `learning/tests/test_rootkey.py`. `go test ./deploy` passes with the new Secret in the settings header.
- **Local note:** `uv run` needs `--frozen` here, because a global `exclude-newer` in the user's uv config makes the lockfile unresolvable.

### Two settings, App values in one Secret, config sync as the App (2026-10-17)

Work items 5, 6 and 7, done together so the manifests change once.

- **Settings.** An installation sets `EXEMPLAR_CONFIG_REPO` and `EXEMPLAR_API_URL`. `EXEMPLAR_CONFIG_GIT_URL`, `EXEMPLAR_GH_APP_ID`, `EXEMPLAR_GH_INSTALLATION_ID`, `EXEMPLAR_EXPORTS_APP_ID` and `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID` left `exemplar-settings`. `EXEMPLAR_GH_WEB_URL` and `EXEMPLAR_GH_API_URL` now default to github.com's bases instead of empty (every reader already treated empty as those), because git-sync takes an empty `GITSYNC_GITHUB_BASE_URL` literally.
- **One host (answers the §6 question).** `envcfg.PublicURLs` derives the webhook URL (`<API URL>/webhook`) and the OAuth callback from `EXEMPLAR_API_URL`; `EXEMPLAR_WEBHOOK_URL` is an optional separate host. The tunnel's `config.yaml` now routes by path only (`^/webhook$` to the webhook, everything else to the API), so it holds nothing per installation and the acme `tunnel.yaml` patch is gone. The tunnel id moves to the `exemplar-cloudflared` Secret (`tunnel-id`) and reaches `cloudflared tunnel run` as an argument. Checked with the pinned cloudflared image: `ingress validate` accepts host-free path rules, `ingress rule` sends `/webhook` to the webhook on any host and everything else to the API, and `tunnel run` without an id fails, hence the argument. Edge rules become path-scoped: Access on the host with a bypass for `/webhook`, and the WAF CIDR rule on `/webhook`. The Ingress component uses one host for both Ingresses. `go test ./deploy` now accepts path rules without a host name and requires the webhook's route to be exactly `^/webhook$`.
- **GitHub App values (`exemplar-github-app`).** `app-id`, `installation-id`, `client-id`, `client-secret`, `private-key`, `webhook-secret`, read with `secretKeyRef`. The `exemplar-api` Secret is gone: the OAuth client secret comes from the App's Secret. The exports app's Secret gains `app-id`. Api's references are optional, for setup mode (item 4).
- **Installation id (item 5).** Optional in search and learn: unset, they look up the App's installation on the config repo (`gh.App.ConfigInstallation`, 30-second timeout). The worker already took ids from events. git-sync still needs it, so setup writes it into the Secret anyway; the lookup covers an operator who leaves it out.
- **Config sync (item 7).** Checked with `git-sync:v4.7.1 --help`: it has `--github-app-application-id`, `--github-app-installation-id`, `--github-app-private-key-file` and `--github-base-url`, and reads them from `GITSYNC_*` env too. Every git-sync clones `$(EXEMPLAR_GH_WEB_URL)$(EXEMPLAR_CONFIG_REPO)` over HTTPS as the review app, so `exemplar-config-git` (deploy key, known_hosts) is gone and so is SSH egress (port 22) from gateway, worker, search and learn. **Decision:** the gateway pod's git-sync sidecar now holds the App's private key, where it held a read-only deploy key. Not checked against a real GitHub: that git-sync's App auth clones a private repo.
- **Sign-in default.** With no `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` and no allow-everyone, GitHub sign-in is for members of the config repo's owner. The core `exemplar-auth` no longer ships `example-org`, and the acme `auth.yaml` patch is gone.
- **Tests:** `TestPublicURLs`, `TestOwner`, `ConfigInstallation` in `TestHookDeliveriesAndRedeliver`; `go test ./deploy`; `kubectl kustomize` renders base, direct and acme.

### Setup mode and the App manifest flow (2026-10-17)

Work item 4. Without `EXEMPLAR_GH_APP_ID`, exemplar-api runs `runSetup` instead of the server: it needs no database or keys, so it starts on a fresh install. The public listener (`:8080`) answers the probes and, for every other path, a 503 naming the port-forward command. The setup page (`internal/setup`) listens on `127.0.0.1:8081` (`--setup-addr`), which no Service, tunnel or Ingress routes to; the operator runs `kubectl -n exemplar port-forward deployment/exemplar-api 8081` and opens `http://localhost:8081/setup` (`--setup-url`). Being able to port-forward is the authorization, so there is no setup token in the logs.

- **Flow.** `/setup` posts the manifest to `<web>organizations/<org>/settings/apps/new?state=…`, where `<org>` is the config repo's owner. The manifest holds the README step 4 permissions and events, the webhook URL and OAuth callback from `envcfg.PublicURLs`, `public: false`, and `redirect_url` back to the setup page. The state is random, single-use, expires in an hour (as GitHub's code does) and must match an HttpOnly, SameSite=Lax cookie set by the same page. `/setup/created` exchanges the code (`gh.CompleteManifest`, go-github's `CompleteAppManifest`). After the operator installs the App, "I've installed it" (a form with a per-process token) finds the installation on the org (`gh.App.OrgInstallation`). The page then offers `exemplar-github-app.yaml` (app-id, installation-id, client-id, client-secret, webhook-secret, private-key) and the `kubectl apply` and `rollout restart deployment/exemplar-api` commands. A second button registers the optional exports app (contents and pull requests write, no events, webhook off) and offers `exemplar-exports-app.yaml`.
- **Before setup.** Every other workload references `exemplar-github-app` without `optional`, so their pods wait with `secret "exemplar-github-app" not found` and start on their own once it exists. Only exemplar-api needs the restart, because its optional env was read at start.
- **Handling of credentials.** They live only in the handler's memory until the operator downloads the Secret; pages are `no-store` with a CSP whose `form-action` allows only GitHub; logs carry the App id and slug, never the key or secrets.
- **Assumed, checked only against a fake GitHub (httptest):** that GitHub accepts a `http://localhost:8081` `redirect_url`, and that the manifest can't turn on the device flow, so the page links to the App's settings for **Enable Device Flow**. Both need a real org.
- **Tests:** `TestManifestFlow` (manifest contents and target, CSP, state bound to the cookie and spent once, replayed redirect refused, no Secret before install, forged form refused, installation lookup, Secret YAML parsed and checked), `TestExportsManifest`, `TestCompleteManifest`, `TestOrgInstallation`. Smoke run of the binary: probes 200, public page names the port-forward, setup page renders the form.

### Setup checks page (2026-10-17)

Work item 8. `/ui/setup` (any signed-in viewer: it shows the org and webhook URL, nothing secret) and `/ui/setup.json` (same sign-in, for scripts) run `setup.Checker`, each check with what was found and what to do:

1. **GitHub App installed on the org**: `OrgInstallation` with the App's JWT; separates "not installed" from "GitHub refused the credentials" (app id and key from different Apps, or a revoked key).
2. **App can read exemplar-config**: `ConfigInstallation`; every workload's config sync needs it.
3. **GitHub delivers webhooks**: GitHub's own delivery log for the last 24 hours (`HookDeliveries`). None is a warning (open a PR); a non-2xx last delivery fails, naming the webhook URL, the tunnel or Ingress route, SSO or WAF on `/webhook`, and a 401 as a webhook-secret mismatch.
4. **exemplar-webhook stores events**: `max(received_at)` from `gh_events`; fails when GitHub reports 2xx deliveries but nothing was stored.

The page runs the checks on load with a 15-second bound; readiness and liveness don't change.

- **Changed from the plan:** the JSON is `/ui/setup.json` behind the UI's sign-in rather than `/v1/setup/checks`, so it needs no new auth path. Not built: the provider check through the gateway and the search-index check. Both need new NetworkPolicy paths from exemplar-api to the gateway and search; `kubectl get pods` shows their readiness meanwhile. Migrations aren't checked because exemplar-api runs them before it serves.
- **Tests:** `TestChecks` (healthy, not installed, bad key, config repo missing, no deliveries, failing deliveries, delivered but not stored), `TestSetupPage` (page, JSON, sign-in required).

### Release install manifests (2026-10-17)

Work item 9. `deploy/render-release.sh <tag> <services digest> <learning digest> <dir>` renders `install-tunnel.yaml` (base) and `install-direct.yaml` (direct) with both images pinned, through a throwaway overlay under `deploy/k8s/.release-*`; it refuses malformed digests and fails if an all-zero digest survives. release-please.yml's new `manifests` job runs after `images` and `cli`, reads the release's digests with `docker buildx imagetools inspect koblas/exemplar:<version>` (and `learning-<version>`), and attaches both files to the release. The install files keep the core placeholders, so the operator still sets the two settings (startup refuses the placeholders), or uses the file as a kustomize resource and patches them.

- **Not run in CI yet:** the job only runs when a release PR merges. actionlint passes. CI and `make manifests` now render the release files with stand-in digests and schema-check them. No tag was created: the first one comes from merging release-please's PR.
- A v* tag pushed by hand (the CONTRIBUTING.md fallback) doesn't get the files; run the script and `gh release upload` by hand.

### Quickstart overlay (2026-10-17)

Work item 10. `examples/k8s/quickstart`: the default build plus `components/postgres`, one replica of every Deployment and of search (whose required anti-affinity needs two nodes for two), search on the default storage class at 20Gi with 500m/2Gi requests, the worker at 500m/1Gi, and Postgres at 10Gi. Two settings in `settings.yaml`. Not a separate sizing component: components must stay resource-only for `go test ./deploy`, so the patches live in the overlay. The overlay test now covers both examples (`TestExampleOverlays`), and CI and `make manifests` render and schema-check both. Not tried on a real kind cluster here (no cluster available); GitHub still needs the public URL the tunnel provides.

### Install docs (2026-10-17)

Work item 11. The README's 11-step INSTALLATION is now a **Quickstart** (seven steps on the quickstart overlay), a **Production install** (prerequisites, `exemplar-config`, Postgres, Secrets, the acme overlay, the path-scoped edge rules, sign-in, going live, optional pieces), **Releases** (the install files, or the base with digests pinned), **Upgrading** (now including key rotation) and **The CLI**. `deploy/README.md` stays the reference: two settings, setup mode, the `exemplar-github-app` keys, the path-routed tunnel and its `tunnel-id`, git-sync as the App, and the Secrets table without `exemplar-api`, `exemplar-config-git`, `exemplar-gateway`, `exemplar-search` and `exemplar-pseudonym`. The spec's stale Helm and `exemplar-orchestrator` references (§2.3, §15, §16) are fixed, and CLAUDE.md mentions setup mode and `internal/envcfg`.

**Where install stands.** On the desktop: `kubectl`, `openssl`, and `cloudflared` for tunnel builds. Before applying: 3 Secrets (database, root key, provider key) plus the tunnel's, 2 settings. Then one setup page creates the App and its Secret. Before this plan: 9 required Secrets with 13 keys, about 15 values, about 30 App form fields, and a deploy key.

**Still open:** the manifest flow and git-sync's App auth have only been run against fakes and `--help`; a real GitHub org and a real cluster are the next check. The exports app can only be created on the setup page during setup. The provider and search checks on `/ui/setup` need new network paths.

### Setup hardening and checks of the docs (2026-10-17)

- **Host check.** The setup listener answers only requests naming the setup URL's host (`localhost:8081` by default) or `127.0.0.1` on its port, with 403 otherwise. Without it, a DNS-rebinding page in the operator's browser could read the Secret YAML through the open port-forward. `TestRefusesOtherHosts`.
- **Port-forward one pod.** The setup-pending text and the README now port-forward a single pod, so the whole flow stays on the pod holding the state (§6).
- **Remote resources, checked:** `kubectl kustomize` accepts a GitHub release asset URL as a resource (tried with cert-manager's, which redirects to objects.githubusercontent.com) with a strategic-merge patch on it, and `github.com/koblas/exemplar//deploy/k8s/base?ref=main` with the remote postgres component (57 resources). Exemplar's own install files don't exist until the first release.

### Documentation site (2026-10-18)

The README had grown to 340 lines. User documentation moved to an MkDocs Material site published to GitHub Pages (`mkdocs.yml`, `.github/workflows/docs.yml`): overview, install (quickstart, production, sign-in, releases and images, upgrading), operate (going live, optional pieces, dashboards), reference (deployment, CLI), architecture and developing. Pages live in `docs/`; `docs/spec`, `docs/plans` and `docs/security` stay repository-only (`exclude_docs`). The README is now a short intro with links; `ARCHITECTURE.md`, `deploy/README.md` and `dashboards/README.md` point at their pages. Links into the repository became GitHub URLs, and manifest and code comments name the new paths.

- **Tooling:** mkdocs 1.6.1 and mkdocs-material 9.6.14 through `uvx`, pinned in the Makefile; no project dependency. `make docs` (strict: broken links and anchors fail) and `make docs-serve`. ci.yml's new `docs` job runs `make docs` on every PR, and `make ci` includes it.
- **Not done here:** the repository's Pages source must be set to "GitHub Actions" (Settings > Pages) before the first deploy; `docs.yml` hasn't run yet.

### First-run fixes from the install review (2026-10-18)

A review of the install path against the code found four things a fresh install hits before the first review, and that the quickstart offered only the tunnel. Fixed here:

- **Gateway crash-loop.** The example `global.yaml` routed every stage to `claude` and `bedrock`; the gateway built both and exited on the first missing credential, so every install that didn't edit the file crash-looped (the quickstart buried "drop the bedrock provider" in step 1). `llm.BuildProviders` now builds each routed provider, skips one that fails with a warning, and fails only when a stage is left with no working provider, naming every provider that didn't build. The example routes to Claude with the Bedrock entries commented out. `TestBuildProvidersSkipsFallbackWithoutCredentials`.
- **Setup page for a personal account.** The manifest form posted to `organizations/<owner>/settings/apps/new`, which GitHub refuses for a user. `Handler.settingsPath` asks `GET /users/<owner>` once and uses `settings/apps/` for a user; on a failed lookup it assumes an organization and asks again on the next page load. `TestUserOwnedConfigRepo`.
- **Config repo CI red from day one.** `eval.yml`'s `eval`, `volume`, `linter-overlap` and `verifier-calibration` jobs needed secrets a fresh copy doesn't have, and the gateway jobs need a route to exemplar-gateway that no build exposes. They now run only when their repository variable is set (`EXEMPLAR_API_URL` and `EXEMPLAR_AUTH0_DOMAIN`; `EXEMPLAR_GATEWAY_URL`, now a variable; `EXEMPLAR_LINTER_OVERLAP=true`). Optional pieces has the table.
- **Shadow default.** The quickstart said "keep `shadow: true`" but the shipped repo file has `shadow: false`, so a trial posted live comments. The file stays `false` (it is also the fixture for the engine, worker, replay, eval and calibration tests) with a comment saying to set it, and the quickstart says "set". The adversarial runner now judges attacks on what would be posted regardless of shadow (`p.Effective.RepoConfig.Shadow = false` after Prepare), so the example config's own `adversarial` CI job doesn't depend on that flag.
- **Ingress as a first-class quickstart.** `examples/k8s/quickstart-direct`: the direct build plus Postgres and the quickstart's one-replica patches, with the host, class and `exemplar-tls` Secret patched on both Ingresses. The quickstart presents the tunnel and an Ingress as two equal paths with the steps marked "tunnel only" / "Ingress only"; a Cloudflare quick tunnel was considered and left out (a dev convenience, not something to configure into an environment). `go test ./deploy`, CI and `make manifests` render it (54 resources with OrbStack's kubectl).
- Small: the settings header names `tunnel-id`; CONTRIBUTING no longer points at the README for digests.

### `/security-hunt` skill (2026-10-19)

Ported content_buddy's six-phase security review (map, hunt by class in parallel, dedupe, validate adversarially, report) as `.claude/skills/security-hunt/SKILL.md` with its playbooks in `.claude/security/` and a deterministic `entrypoints.sh` for Exemplar's surface: HTTP routes on every listener, the webhook's HMAC and the worker's event routing, River workers, the learn CronJob and CLI commands, the Python jobs, model tool dispatch and stage calls, and the GitHub-write, exec and raw-SQL sinks (219 rows over the whole tree). A fourth hunter class, `model-trust`, covers prompt injection, tool calls, provenance and anchors, approval, redaction and eligibility. `docs/security/known-non-issues.md` points at spec §13, lists the authorization primitives, and seeds the non-issues list with the deliberate decisions (loopback setup page, worker holds the App key, shared tokens inside the namespace, root-key derivation). Output goes to gitignored `.security/`. Not run end to end yet: the first `all` hunt is the next step, and its validated findings go to `/go-service` or the backlog.

**Still open from the review:** no release tag yet, so the quickstart's `releases/download/<version>` URLs can't be followed outside the repository; Pages isn't deployed; the quickstart overlay still needs its resource swapped for the release URL by hand (a rendered `install-quickstart.yaml` per release would remove that); exemplar-api still needs a manual `rollout restart` after the Secret appears; `/ui/setup` has no gateway or search check; the Cloudflare Access and WAF rules are still prose; `examples/exemplar-config` has no README.

### Security hunt fixes (2026-10-19)

`/security-hunt all` confirmed 1 Critical, 1 High and 8 Medium findings (`.security/FINDINGS.md`, gitignored). All ten are fixed here, each with a test that failed first.

- **ast-grep project config (Critical).** ast-grep found `sgconfig.yml` in its working directory, which is the PR checkout, and loaded the shared libraries its `customLanguages` name. `compiled.Runner` now always passes `--config` naming an empty config it writes beside the rule file, which turns that discovery off. The fix was reproduced with the pinned 0.45.1. Every caller (worker, eval, playground, compile, CLI) goes through `Runner`.
- **Merge policy approvers (High).** `gates.HumanMergePolicy` counts an approval only from someone with write access (`gh.CanWrite`, from the collaborator permission). Before this, any account's review could pass `exemplar/merge-policy` on an unowned path.
- **Mode eval case repos.** Each case repo in a config head must be readable by the requester. For a GitHub login that is the repo's collaborator permission. For a service or OIDC person it is their `evaluate:` permissions, now stored on the evaluation (`requester_scopes`, migration 00028). A case on any other repo is reported unavailable. Config CI's Auth0 client needs `evaluate:` permissions for the case repos (`docs/operations/optional.md`).
- **Model message markup.** The comment body breaks code fences (so a `suggestion` block can't skip fix validation), `@` mentions and HTML tags in the model-written message.
- **evalgen eligibility.** `evalgen.New` requires an `Eligible` check, as compile does. `exemplar-learn evals` takes `--config`, and its CronJob gets the one-time config-sync init container.
- **Unlinked OIDC logins.** Without a connected GitHub account the login is `oidc:<username>`, both on the web and for `/v1/evaluate`. `owneract.allowed` never matches a login that has a provider prefix.
- **Excluded files block auto-approve.** Stage 1 gets an `Unreviewed` count with the reason `changed_files_not_reviewed`. Nested `.gitattributes` and `.gitmodules` are denylisted in any directory.
- **Stale approvals.** When a `native_approve` branch requires approvals, it must also dismiss stale approvals on push (classic protection or a ruleset). Otherwise the gate fails with `stale_approvals_not_dismissed_on_push`.
- **Config writer.** `EnsureBranch` reuses an existing branch only if every extra commit on it is the App's own: authored by `<slug>[bot]` (`App.BotLogin`, read once from `GET /app`) and signed by GitHub. Any Bot account was not enough, because a committer's workflow can push as `github-actions[bot]`. `MergePR` merges only a PR the App opened that holds only its commits, pinned to the head SHA it checked. This also covers expiry's merge of a PR found by branch name.
- **Mode eval limits.** The mode-eval matcher runs engines under `prlimit` (4 GiB data, 120 s CPU; `compiled.LimitCommand`). The first cut used 2 GiB, which made Semgrep segfault on Linux; CI's `TestRealEnginesUnderLimits` caught it after merge, and a Linux container confirmed 4 GiB works. The cluster has no user namespaces, so `unshare` isn't available.

**Breaking for operators** (in `docs/install/upgrading.md`): Config CI's Auth0 client needs `evaluate:` permissions for its case repos. `native_approve` branches that require approvals must dismiss stale approvals. Any excluded file blocks auto-approve, deletions included; this is stricter than the validator's suggestion, chosen so a deleted check cannot be approved unseen. Overlays that patch the evals CronJob need `--config`. OIDC sessions from before the upgrade keep their bare login until they expire (8 hours).

**Not verified:** `prlimit --data` with the real engines (`TestRealEnginesUnderLimits` runs only on Linux, so CI is its first run). Also that GitHub marks the App's Contents-API commits as verified. If it doesn't, every config-writer merge is refused, including kill-switch owner actions, and those PRs wait for a human; check one on a real installation before relying on it. **Still open:** the playground runs draft compiled rules through the review engine's runner, without limits. The Low findings and the two entries in `docs/security/known-non-issues.md` that no longer match the code (link expiry; "merged compiled rules only") are left for review. So is the validator's lead that `extract.Driver.Submit` sends mined comments without re-checking eligibility, the same gap evalgen had; it has not been validated.

### Documentation restructure: Divio quadrants (2026-10-20)

The site had 16 pages written for installers only, blending quadrants (`install/production.md`, `reference/deployment.md`, `operations/optional.md`), citing spec sections and phases, and leaving most of the surface undocumented: 17 of 64 `EXEMPLAR_*` variables, 12 of 15 CLI subcommands, every schema in `schema/`, the web UI, the feedback commands, the runbook. Rebuilt on the [Divio system](https://docs.divio.com/documentation-system/) with `navigation.tabs`:

- **Start here:** the landing page (cards, lifecycle, capability map), the quickstart (tunnel and Ingress as content tabs), *Your first review* (shadow → live → feedback) and *Your first rule* (rule, eval cases, `exemplar try`, merge).
- **Concepts:** why, architecture, how a review works, rule lifecycle, trust and data handling, glossary.
- **Guides** by persona, every title "How to …": install (production, sign-in, in-cluster Postgres, images, upgrade, rotate keys), operate (troubleshoot, the runbook from spec §15, auto-approve, Grafana, telemetry, rule exports, the compile CronJob), rule owners (write a rule, eval cases, test a rule, config CI, candidates, adjudicate), PR authors (respond to a finding, review before pushing, coding agents), develop (devenv, manifests, docs).
- **Reference:** `exemplar-config` (layout plus one page per schema), environment variables per binary, Secrets and ConfigMaps, workloads/ports/probes/schedules, every CLI subcommand and flag, web UI pages and routes, feedback commands and signals, dashboards and alerts, releases and images.

Every fact from the old pages was kept; spec `§`, `FR-`, `T-0.x` and `Phase N` references are gone from public pages (the spec is linked on GitHub where the design rationale matters). Old URLs redirect (`mkdocs-redirects`); pages carry a "last updated" date (`git-revision-date-localized`, so the docs jobs check out full history). `go test ./docs` fails when the code reads an `EXEMPLAR_*` variable, has a cobra subcommand or a top-level schema property without a reference entry, when a page is missing from the nav, when a redirect dangles, or when a public page cites an internal reference; `make docs` runs it before the strict build, and CI's `docs` job sets up Go for it. `/docs` (quadrant table, templates, persona checklist, then the checks) and `/docs-audit` (read-only) are the skills; CONTRIBUTING has a Documentation section.

**Gaps left on purpose:** the rule schema's `eval` field is documented as not read by the code; the compile CronJob and the program board stay "not built yet" as before; `exemplar-search bench` has no page beyond its variable; `/v1/evaluate` has a section on the web UI page rather than an API page. The nested schema properties are hand-written and only the top level is test-checked.

### Positioning: the review bottleneck (2026-10-20)

The site's lead is now the problem Exemplar is for: AI-assisted coding multiplied PR volume, review did not scale, and standards slip under the load. Why Exemplar opens with the measured picture (Faros, Harness 2026, DORA 2025, the 2026 habituation study, GitHub's pull-request controls), each footnoted, and frames Exemplar as two jobs: hold the review standard, and absorb the volume by giving every PR a verdict (approved with no human review needed, or routed to humans with reasons). How a review works gains a "Who reviews this PR?" section naming the three outcomes before the pipeline; the landing, README, concepts index, glossary (Verdict) and first-review tutorial follow. The `/docs` skill carries the positioning rule so new pages say which job a feature serves. Open: the check run itself does not yet state the verdict (proposed as a worker change).

### Documentation site built by Zensical (2026-10-20)

The site is now built by Zensical 0.0.70 (the Material for MkDocs team's successor, with its `modern` look) instead of MkDocs with Material. Zensical reads `mkdocs.yml` as-is, so the config, the nav, the redirects plugin and `go test ./docs` are unchanged; `site_dir` moved into the config (`out/site`) because Zensical has no `--site-dir`. The git-revision-date plugin is not supported by Zensical yet (on its roadmap), so pages no longer show a "last updated" date and the docs jobs no longer need the full git history. Verified in the built site: landing-card icons, content tabs, Mermaid, footnotes and the old-path redirects all render.

### The check run states the human-review verdict (2026-10-20)

The `exemplar` check run now answers "who reviews this PR?" first. Its title ends in `approved` or `human review needed`, and its summary's first line is either "No human review needed. Every auto-approve gate passed" or "Human review needed. Not auto-approved because …" with up to three reasons in plain words, derived only from the code-measured gate traces (`internal/pipeline/verdict.go`): auto-approve off (stated alone, with no gate list), then policy (denylisted or disallowed paths, author, fork, size), then the review's outcome (excluded files, candidate findings, coverage, bug lane, degraded, same-family), then the approval checks (CODEOWNERS, branch rules, merge safety, the categorizer). The conclusion stays neutral; the audit page keeps the full trace. Shadow repos and incomplete runs are unchanged. Docs: the three-outcome table, the posting section, the first-review tutorial and the finding guide.

### Security hunt follow-ups (2026-10-20)

The items the 2026-10-19 hunt left open, one commit each, each with a test that failed first.

- **Playground compiled drafts.** `engine.Options.Compiled` overrides the compiled runner for one review. `playground.Runner.Compiled` passes it, and the worker sets it to mode eval's prlimit matcher. Live reviews of merged rules keep the unlimited runner.
- **Extract eligibility.** Validated: `extract.Driver.Submit` sent pending comments without re-checking eligibility. `extract.New` now requires an `Eligible` check, as evalgen does. Comments from ineligible repos are marked `error` before the cache lookup, so they never sit at the front of every batch. They stay `error` if the repo becomes eligible again, which errs toward not sending. `exemplar-learn extract` takes `--config`, and its CronJob gets the config-sync init container.
- **Semgrep under limits.** Tested on Linux (`exemplar:dev` in OrbStack, amd64 under emulation). Semgrep's `--jobs` defaults to the node's CPU count, and each job reserves its own heap. One job is killed at a 3 GiB data limit and passes at 4 GiB; two jobs are killed even at 4 GiB. So mode eval's limit failed on any multi-file PR, and compile's 2 GiB `--as` limit always failed. `compiled.Runner` passes `--jobs 1`. `SandboxCommand` caps data rather than address space. `--sandbox-memory` defaults to 4 GiB, and the compile pod's limit goes from 4Gi to 6Gi. `TestRealEnginesUnderLimits` now scans 13 files, under both prefixes when user namespaces are available. Without `--jobs 1` it fails on Linux. One job costs little: 336 files took 5 s, against 3 s with six jobs. The sandbox keeps `--data`, not `--as`, though `--data` does not count shared file mappings: Semgrep needs it, and the pod's memory limit is the hard cap.
- **CLI output.** `exemplar review` drops C0 and C1 control characters from model text and flattens line breaks. `exemplar try` drops them too.
- **Verifier evidence.** Evidence counts only if it cites the finding's file, overlaps its lines (give or take three), and quotes at least eight non-space characters. A finding on removed (LEFT) lines is checked against the base, where its line numbers are. The verifier prompt states the rule (`verifier-v2`).
- **Human dedupe.** It shares the linter dedupe's `judgeable` exemption: secret, blocking and Security findings are never judged duplicates.
- **Ingest commenters.** Ingest fetches `authorAssociation` and keeps only OWNER, MEMBER and COLLABORATOR (`not-a-collaborator` drop reason).
- **Feedback links.** Confirm and record require read access to the finding's repo: the GitHub permission with the person's token, or their OIDC groups' repos. Otherwise the link answers as for a missing finding. Every sign-in now keeps the sealed access cookie for `/f/` too (HttpOnly, AES-GCM; for GitHub it holds the user token); a link sign-in still gives `/ui/` none. OIDC viewers need the finding's repo in their groups' mapping.
- **checkScope.** Refuses `FlavorPoster` on the config repo. Declined for `FlavorGate`: it is read-only, and owner actions check rule-owner teams with a gate token scoped to the config repo. The playground's comment handler (`playground.Handler`) is not wired in any binary, so nothing posts on config PRs.
- **Candidate queue.** A candidate's title and description show only to viewers who can read every source repo. Others see the row with its text hidden.
- **Known non-issues.** Feedback links have no expiry of their own; the doc now says they end with their finding's retention, behind sign-in and repo access. Adding an issued-at and TTL was offered and not chosen. The compiled-rules entry now says unmerged bodies run under prlimit caps.

**Breaking for operators** (in `docs/guides/install/upgrade.md`): overlays that patch the extract CronJob need `--config`. Feedback-link sessions from before the upgrade sign in again. OIDC viewers see "not found" on links for repos outside their groups' mapping.

**Not done:** comments from non-collaborators that ingest already mined stay in `mined_comments` and in any candidates they fed. A one-off cleanup would need the association, which earlier ingest never stored. The Semgrep memory figures come from amd64 under Rosetta on arm64; native amd64 nodes may reserve differently, and CI's `TestRealEnginesUnderLimits` is the check. User namespaces worked only with seccomp and AppArmor unconfined, so the compile CronJob stays suspended.

### Landing page: one reason (2026-10-20)

`docs/index.md` is now a value-focused landing page in the style of a product site, with the navigation and table of contents hidden and its own stylesheet (`docs/stylesheets/landing.css`, scoped to `.landing`). It makes one argument, "give your reviewers their time back without lowering the bar": a hero with two calls to action, the three problem numbers, the two verdicts, five guarantees, the one-pass pipeline, the PRD targets labelled as targets, and a closing call to action. The capability map ("Where things are") moved to the Guides index; the binaries table was already in Architecture. Why Exemplar opens on the same sentence so the landing and the long-form case agree.

### Security hunt open items (2026-10-20)

The items left after #22, #23 and #27, from the hunt's Low findings and the follow-ups' "Not done" notes.

- **Agent tools and exclusions (Low model-trust-03).** `Prepared.Exclusions` carries the repo's exclusion rules into the toolbox. `read_file` and `list_dir` refuse excluded paths, `list_dir` leaves them out of listings, and `grep` drops their hits. A directory is excluded when a file directly in it would be (`vendor/**` covers `vendor`).
- **Signed app commits.** GitHub's commit-signature docs say an app's API commits are signed and verified when the request names no custom author, committer or signature. `PutFile` sends none, and the config-repo test fake now refuses a write that does, so adding one would fail a test instead of silently stopping the config writer's merges. Not yet checked on a real installation.
- **Earlier non-collaborator comments.** `mined_comments.author_trusted` (migration 00029) is true for comments ingest keeps and NULL for rows from before the check. `exemplar-learn ingest --recheck-authors` clears each repo's cursor and sets `ingest_cursors.recheck_since`. When that repo's pass finishes, its human comments still NULL are marked false. False rows are left out of `PendingComments` and out of clustering, so the next cluster and rank runs retire candidates that only they supported. Nothing is deleted.
- **Extract and eligibility.** A pending comment from an ineligible repo is parked as `ineligible` (migration 00030 adds the status and converts earlier "repo is not eligible" errors). Each `Submit` first returns parked comments to `pending` for repos that are eligible again.
- **Denylist.** Build and dev-environment files are denied in any directory: Dockerfile and Containerfile variants, Compose files, setup.py and setup.cfg, Gemfile and gemspecs, Rakefile, .npmrc and .yarnrc, pre-commit config, Justfile and Taskfile, build.sbt, meson.build. `.devcontainer/**` and `.husky/**` are denied at the root.

**Not code changes:** the Semgrep memory figures came from amd64 emulation; CI's `TestRealEnginesUnderLimits` on native runners is the ongoing check, and it passed on #23. The compile CronJob stays suspended until the cluster allows user namespaces under RuntimeDefault seccomp, or a sandboxing runtime such as gVisor is used. `release-please` has failed on every push since at least 2026-10-18 because the repository does not allow GitHub Actions to create pull requests (Settings > Actions > General).

### Quickstart under Guides > Install (2026-10-20)

The quickstart moved from `docs/tutorials/` to `docs/guides/install/quickstart.md` and sits first in the Install section of the Guides nav, as "Quickstart (a trial install)", next to the production install that builds on its steps. The Start here tab keeps the landing and the two tutorials. Old paths (`tutorials/quickstart.md`, `install/`, `install/quickstart.md`) redirect. The landing's primary button now says "Try it: a 20-minute trial install".

### Starter rules for any codebase (2026-10-20)

The example config's `rules/` should give a quickstart findings on whatever the first repo is written in. They held three Go rules. Six more, scoped to source in about twenty common languages (tests, `testdata` and fixtures excluded) or to file types most stacks have:

- **Model rules (`exemplar`).** `no-secrets-in-logs` (important), `untrusted-input-to-sink` (blocking: SQL, shell, paths, HTML, deserializers, SSRF), `external-call-timeouts` (important: timeouts and bounded retries), `migration-backward-compatible` (important, scoped to migration directories and `*.sql`), and `tests-for-behavior-change` (suggestion, `shadow`: expected to be noisy, so its findings are stored but not posted).
- **Compiled rule.** `pin-container-images`: Semgrep `languages: [regex]` for `:latest` in Dockerfiles and YAML, no model cost.
- **No committed-key rule.** A Semgrep key-pattern rule was written and dropped: the built-in `possible-secret-in-diff` lane (gitleaks) already reports keys, and redacts them before any model sees the diff.
- **`no-raw-sql-concat` never matched.** In Go, ast-grep parses a bare `$DB.Query(...)` pattern ambiguously, and `, $$$` does not match zero trailing arguments, so the rule found nothing. It is now a structural rule over `Query`, `QueryRow`, `Exec`, `Prepare` and their `Context` variants: concatenation or `fmt.Sprintf` in the query argument, not in later arguments. The `docs.internal` citation is an OWASP link. The rule reference and How to write a rule carry the working form. The guide says to run a matcher on a sample file, since `exemplar validate` checks only the schema.
- **Owners.** The two Go rules move from `@platform/backend` to `@platform/exemplar`, so the rules name only teams CODEOWNERS already does; the quickstart says to replace them.

Each compiled matcher was run with ast-grep 0.45.1 or Semgrep 1.172 on positive and negative files, and the shared `file_patterns` on sample paths. Cost per PR: every rule whose patterns match runs one agent, shadow rules included, three at a time. A source change in a Go repo runs six (four language-agnostic, two Go) and a migration adds one. `untrusted-input-to-sink` is a blocking Security rule, so it starts on the high model tier and is posted over the comment budget.

**Not done:** no eval cases ship, since a case needs a commit in the installer's repositories; How to set up config CI says editing a starter rule fails `eval` until it has them. The model rules have not been run against real PRs; their precision is unknown until a shadow week.

### Go 1.27.3 (2026-10-22)

govulncheck failed CI on every branch: GO-2026-6809 in `crypto/x509`, found in Go 1.27.2 and fixed in 1.27.3. `go.mod`, the devenv Go override (source tarball and its sha256 from go.dev/dl) and the image's build stage (`golang:1.27.3-trixie`, pinned by index digest) move to 1.27.3; CI follows `go.mod`. With Go 1.27.3, `go build`, `go vet` and `go test ./...` (engines required, Postgres via testcontainers) pass and govulncheck reports no vulnerabilities. Not checked: the devenv shell itself was not rebuilt here, nor was the image built.

### ripgrep for the agents' grep tool (2026-10-22)

`TestToolsRespectExclusions` had failed on main's CI since it landed (234c8fb) with `grep = "", search failed`. The agents' `grep` tool runs `rg`, which devenv provides but neither the CI runner nor the services image installed, so the tool failed on every call outside a dev shell, in production included. CI's engine step and the image's apt line now install `ripgrep`, CI prints its version with the other engines, and the test's grep check skips without `rg` unless `EXEMPLAR_REQUIRE_ENGINES=1`, like the other engine tests. The reference pages that list the image's tools name ripgrep.

### Faster Go tests (2026-10-22)

`go test ./...` took about 55 s locally, set by `internal/engine` (38 s), `internal/worker` (28 s) and `internal/calibrate` (24 s). No test called `t.Parallel()`, and most of each test's time was spent waiting on `git` processes: every fixture repository was built from scratch (`init`, `add`, two `commit`s) and cloned into a workspace.

- **Fixture cache.** `synth.UseCache` keeps one built repository per distinct fixture and copies it for repeat builds into an empty directory; commits have fixed authors and dates, so the copy has the same SHAs. `synth.RunCached` sets it up from `TestMain` in the packages that build fixtures (engine, worker, calibrate, adversarial, evalrun, replay, playground, learn/evalgen, cmd/exemplar). `storetest.Run` is `Main` without the exit, so the two compose. Production callers never turn the cache on.
- **Parallel tests.** Top-level tests in engine, worker, calibrate, search/index, search/api and cmd/exemplar call `t.Parallel()`, as do the subtests of the nine that have them (each builds its own environment; `tparallel` requires it), except those that set the environment or the working directory, and `TestReviewStage2`, which swaps the package-level `evaluateStage1`. `TestSlotsAcrossReplicas` counts advisory locks in its own database only, since `pg_locks` spans the server.
- **`redact.New` raced with itself.** gitleaks' `NewDetectorDefaultConfig` loads rules through viper's global instance; two Scanners built at once raced (480 reports under `-race` once tests ran in parallel). Construction is now serialized; each binary builds its Scanner once at startup, so production was exposed only where a binary builds more than one.

Result: 39 s wall locally (from 55 s); engine 14 s, worker under 10 s, calibrate 11 s. The parallel packages pass three times under `-race`. The slowest packages are now `compiled` (real engines), `pathhistory` and `store`, none of them changed here.

### Release assets (2026-10-22)

No release had assets: v0.1.0, v0.2.0 and v0.2.1 carried no CLI archives, `SHA256SUMS` or install manifests, so the quickstart's `install-tunnel.yaml` and `install-direct.yaml` links returned 404. In release-please.yml's run, `release.yml`'s `release` job downloaded every artifact of the run, including the Docker build records `images.yml` uploads (`*.dockerbuild`); those failed to download after five retries, the job failed, and `manifests` was skipped. The CLI builds and both images succeeded.

- `release.yml` downloads only `exemplar-*`, its own archives.
- The manifests job moves to `manifests.yml`, called by release-please.yml as before and runnable by hand with a tag, so a release's manifests can be re-rendered without cutting a new one. CONTRIBUTING.md says when to run it.

**Not done here:** backfilling the existing releases. Once merged, running `release` and `manifests` with `v0.2.1` attaches its archives and manifests; earlier tags can be done the same way if their images are on Docker Hub.

### Running without exemplar-search as an install mode (2026-10-24)

Search is the one stateful service besides Postgres (local SSD, a mirror and index per repository), and a trial or a single-repository install can do without it: the worker already treats `EXEMPLAR_SEARCH_URL` as optional. What was missing was the documentation that makes it a mode rather than a degraded state.
- **New how-to** `docs/guides/install/without-search.md`: three overlay patches (`$patch: delete` on the StatefulSet and its Services, an empty `EXEMPLAR_SEARCH_URL`), what to check, and how to add search later. The quickstart overlay carries the same patches commented out. Rendered with `kubectl kustomize` on the quickstart: 54 resources, no search StatefulSet, the settings key empty.
- **The install ladder** in the quickstart's "What you have": shadow (search optional), posting with search, auto-approve.
- **Concept:** How a review works now lists the agents' tools as the code offers them (four over the checkout, `find_references` and `path_history` from search) and has a "Without exemplar-search" paragraph: what is lost, that a skipped bug class is never auto-approved, and what the check run says.
- **Reference:** the `EXEMPLAR_SEARCH_URL` row and the search workload row say what empty means and that it is optional.
- **Operate:** the troubleshooting entry distinguishes search-off on purpose from search missing; the auto-approve note lists search as a prerequisite.

**Not done (code):** answering `find_references` and `path_history` from the runner's own checkout (ripgrep, ctags, `git log` over the touched paths) would close most of the gap for single-repository installs, and the check run could name the missing search explicitly. Both are in the hosting design doc's section 9.

### Technical spec Draft 1.4 (2026-10-24)

`docs/spec/technical-spec.md` is reviewed against PRD v0.9 (it implemented v0.6) and the hosting design of the same day.
- **PRD v0.7–v0.9 carried into the spec:** verifier quorum (§6.8), round cap and class-of-bug grouping (§6.2, §6.10), the clean-PR set and the change gate (§8.8, §8.9), the clean-run definition, author experience, blast radius, base binding and system accountability in Stage 1 and its trace (§6.13, schema), the `same_family` calibration rule with `calibration_results`, rules per agent and overflow by expected value (§6.3), importance priors (§8.11), owner seeds and merged-change clusters as co-equal sources (§8.1, §8.5), `review_by_defaults` (§8.10), severity-weighted acceptance and the adjudicated shadow exit (§9.2, §11, §12.6, §16).
- **Hosting:** new §2.4 with the host requirements (R1–R15), the elastic changes (C1–C11: control/runner split, single-use token claims, config snapshots, advisory locks, queue-driven autoscaling, per-review volumes, pooling, search and gateway scaling, the sandbox in the task boundary, the small-install overlay), the GKE Autopilot recommendation with the alternatives, trial paths, the hosted-trial gap and the cost picture; the runner boundary in §13; §14, §15, §16 (an H1–H5 order of work) and §17 (inputs 22–25) follow.
- **Search optional:** §6.4.6 specifies search-off as an install mode: what the checkout gives, `LocalReferences` and a local `PathHistory` behind the same tool names, `bug.contract` running on single-repository installs (so they may auto-approve) and staying `degraded_skip` with `extra_repos`, the check-run scope line, and the install ladder. The backlog item stays until the code lands.
- **As built** recorded: the worker's roles and token flavors (§2.2, §5), the blob-less depth-1 checkout (§6.4.2), the tunnel (§13), the code layout (§3), tree-sitter's module path (§14).

**Not done:** the PRD does not yet say search is optional (constraint 6, FR-E12); a v0.10 should. None of the newly specified behaviour is built; the backlog's gate and search rows still hold the briefs.
