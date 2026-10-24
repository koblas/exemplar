# Secrets and ConfigMaps

What an installation supplies in the `exemplar` namespace. Secrets are created outside this repository from your secret manager and referenced by name only; none are in the manifests or the rendered output. Which variable each key feeds is on [Environment variables](environment-variables.md).

## Secrets

| Secret | Keys | Used by | Notes |
|---|---|---|---|
| `exemplar-database` | `url` | every binary but search | The Postgres URL, for example `postgres://exemplar:<password>@db.internal:5432/exemplar?sslmode=require`. |
| `exemplar-root-key` | `key` | api, worker, gateway, search, learn, the learning jobs | At least 32 bytes (`openssl rand -hex 32`). See [below](#exemplar-root-key). |
| `exemplar-providers` | `anthropic-api-key` | gateway | The Claude API key. Not needed when `global.yaml` routes to Bedrock only; Bedrock uses workload identity on the gateway's service account instead of keys. |
| `exemplar-github-app` | `app-id`, `installation-id`, `client-id`, `client-secret`, `private-key`, `webhook-secret` | worker, search, learn, every git-sync; api (`app-id`, `client-id`, `client-secret`, `private-key`); webhook (`webhook-secret`) | Written by the setup page. The other workloads reference it without `optional`, so they wait for it. |
| `exemplar-exports-app` | `app-id`, `private-key` | learn `export` | Optional: the separate exports app with Contents and Pull requests write on opted-in repos only, never installed on exemplar-config. The export run skips without it. |
| `exemplar-oidc` | `client-secret` | api | Only for OIDC sign-in. |
| `exemplar-cloudflared` | `credentials.json`, `tunnel-id` | cloudflared | Tunnel builds only. Both from `cloudflared tunnel create`; the id is `cloudflared tunnel run`'s argument. |
| `exemplar-postgres` | `password` | exemplar-postgres | `components/postgres` only: the in-cluster superuser's password. |
| `exemplar-otel` | `OTEL_EXPORTER_OTLP_HEADERS` and any other OTel variable | every container, optional | Credentials for the telemetry endpoint, through `envFrom`. |

Explicit single-key Secrets for rotating one derived key alone are mounted by an overlay; see [How to rotate keys](../guides/install/rotate-keys.md).

### `exemplar-root-key`

The link, session, gateway, search, pseudonym and runner-claim keys derive from the root key with HKDF-SHA256 and a per-key label (`internal/envcfg`). An explicit `EXEMPLAR_LINK_KEY_FILE`, `EXEMPLAR_SESSION_KEY_FILE`, `EXEMPLAR_GATEWAY_TOKEN`, `EXEMPLAR_SEARCH_TOKEN`, `EXEMPLAR_PSEUDONYM_KEY` or `EXEMPLAR_CLAIM_KEY_FILE` still wins. Rotating the root key signs everyone out and changes every pseudonym.

## ConfigMaps

### `exemplar-settings`

Patched by every overlay. The first two are required; startup refuses the example values.

| Key | Default | Meaning |
|---|---|---|
| `EXEMPLAR_CONFIG_REPO` | `example-org/exemplar-config` | The config repository, `owner/name`. |
| `EXEMPLAR_API_URL` | `https://exemplar.example.invalid` | exemplar-api's public base. |
| `EXEMPLAR_WEBHOOK_URL` | empty | A separate webhook host. |
| `EXEMPLAR_GH_WEB_URL` | `https://github.com/` | GitHub's web base (GHES: `https://<host>/`). |
| `EXEMPLAR_GH_API_URL` | `https://api.github.com/` | GitHub's API base (GHES: `https://<host>/api/v3/`). |
| `EXEMPLAR_AUTH0_DOMAIN`, `EXEMPLAR_AUTH0_AUDIENCE` | empty | Service callers of `/v1/evaluate`; both empty turns them off. |
| `EXEMPLAR_GATEWAY_URL` | `http://exemplar-gateway.exemplar.svc` | In-cluster gateway. |
| `EXEMPLAR_SEARCH_URL` | `http://exemplar-search.exemplar.svc` | In-cluster search. |

### `exemplar-auth`

Optional; read by exemplar-api through `envFrom`. Holds every `EXEMPLAR_OAUTH2_GITHUB_*` and `EXEMPLAR_OIDC_*` setting except the two secret files ([Environment variables](environment-variables.md#exemplar-api)). Ships empty with the settings commented out.

### `exemplar-otel`

Optional; read by every container through `envFrom`. `OTEL_EXPORTER_OTLP_ENDPOINT` (or the per-signal `_TRACES_` and `_METRICS_` endpoints), `OTEL_METRICS_EXPORTER`, `OTEL_METRIC_EXPORT_INTERVAL`.

### `exemplar-cloudflared`

Tunnel builds only: the tunnel's routing, matched by path (`^/webhook$` to the webhook, everything else to the API), the same for every installation. cloudflared reads it at start, so restart the Deployment after changing it.

## What the overlay sets besides

- The build: a release's `install-tunnel.yaml` or `install-direct.yaml`, or `deploy/k8s/base?ref=<version>` with both image digests ([Releases and images](releases.md)).
- The search volume's size and storage class (500Gi on `local-ssd` as placeholders).
- For Ingress builds, the host, class and TLS secret on both Ingresses, and the ingress controller's namespace in the two NetworkPolicy patches.
- For `components/postgres`, the volume's size and class.
