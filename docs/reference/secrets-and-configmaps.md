# Secrets and ConfigMaps

What an installation supplies in the `exemplar` namespace. No Secret value is in this repository or the rendered output. The Secrets the setup page writes (`exemplar-setup`, `exemplar-github-app`, `exemplar-exports-app`, `exemplar-root-key`, `exemplar-providers`, and with `components/postgres` `exemplar-postgres` and `exemplar-database`) are declared in the manifests with no data, and setup fills them through the Kubernetes API ([Workloads, ports and probes](workloads.md#setup-mode)); setup keeps a value that is already there, except `anthropic-api-key` when one is pasted on its last step. The others come from your secret manager or `kubectl`. Which variable each key feeds is on [Environment variables](environment-variables.md).

## Secrets

| Secret | Keys | Used by | Notes |
|---|---|---|---|
| `exemplar-database` | `url`, `runner-url` | every binary but search; `runner-url` the runner template only | `url` is the direct Postgres URL, for example `postgres://exemplar:<password>@db.internal:5432/exemplar?sslmode=require`. `runner-url` is optional: review runners' URL through a pooler in transaction mode ([How to pool runner connections](../guides/install/pool-runner-connections.md)); without it runners use `url`. Setup writes `url` for `components/postgres`; declared by that component. exemplar-api starts without it in setup mode. |
| `exemplar-root-key` | `key` | api, worker, gateway, search, learn, the learning jobs | At least 32 bytes (`openssl rand -hex 32`); setup writes one when it's done (models chosen, database there) and the Secret has none. Every workload but api mounts it without `optional`, so they start once it's there. See [below](#exemplar-root-key). |
| `exemplar-providers` | `anthropic-api-key`, `openai-api-key`, `voyage-api-key`, `bedrock-api-key` | gateway | Each key is optional. `anthropic-api-key` and `openai-api-key` are the Claude and OpenAI API keys, needed only when `global.yaml` routes to that provider (`claude`, `openai`). `voyage-api-key` is the Voyage AI key for `providers.embed`, which embeds extractions for the learning plane; without it reviews run as usual and the `embed` job fails. `bedrock-api-key` is a Bedrock API key, read as `AWS_BEARER_TOKEN_BEDROCK`; without it Bedrock uses workload identity on the gateway's service account ([How to use Bedrock on EKS](../guides/install/bedrock-on-eks.md)). The setup page writes `anthropic-api-key` when you paste it. |
| `exemplar-setup` | `token`, `key`, `providers` | api | Written by exemplar-api on its first start in setup mode. `token`: the setup page's sign-in token. `key`: signs the page's cookies, forms and states. `providers`: `own` when **I've created `exemplar-providers` myself** was chosen. Unused after setup. |
| `exemplar-github-app` | `app-id`, `installation-id`, `client-id`, `client-secret`, `private-key`, `webhook-secret`, `setup-webhook-secret`, `slug`, `html-url` | worker, search, learn, every git-sync; api (`app-id`, `client-id`, `client-secret`, `private-key`); webhook (`webhook-secret`) | Written by the setup page: the App's credentials as soon as GitHub creates it, `installation-id` once the installation includes exemplar-config. `webhook-secret` is held as `setup-webhook-secret` until setup is done, so exemplar-webhook starts only then. `slug` and `html-url` are the App's name and page, for the setup page. The other workloads reference it without `optional`, so they wait for it. |
| `exemplar-exports-app` | `app-id`, `private-key`, `slug`, `html-url` | learn `export` | Written by the setup page when the exports app is created there. Optional: the separate exports app with Contents and Pull requests write on opted-in repos only, never installed on exemplar-config. The export run skips without it. |
| `exemplar-oidc` | `client-secret` | api | Only for OIDC sign-in. |
| `exemplar-cloudflared` | `credentials.json`, `tunnel-id` | cloudflared | Tunnel builds only. Both from `cloudflared tunnel create`; the id is `cloudflared tunnel run`'s argument. |
| `exemplar-postgres` | `password` | exemplar-postgres | `components/postgres` only: the in-cluster superuser's password. Setup writes it with `exemplar-database`, on exemplar-api's first start; declared by that component. |
| `exemplar-pgbouncer` | `databases.ini`, `userlist.txt` | exemplar-pgbouncer | `components/pgbouncer` only: the `[databases]` section naming the upstream Postgres, and the runners' role with its password (plain text or SCRAM secret), which PgBouncer also logs in to Postgres with. |
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
| `EXEMPLAR_BEDROCK_REGION` | empty | The gateway's Bedrock region; empty falls back to `AWS_REGION`. |

### `exemplar-auth`

Optional; read by exemplar-api through `envFrom`. Holds every `EXEMPLAR_OAUTH2_GITHUB_*` and `EXEMPLAR_OIDC_*` setting except the two secret files ([Environment variables](environment-variables.md#exemplar-api)). Ships empty with the settings commented out.

### `exemplar-otel`

Optional; read by every container through `envFrom`. `OTEL_EXPORTER_OTLP_ENDPOINT` (or the per-signal `_TRACES_` and `_METRICS_` endpoints), `OTEL_METRICS_EXPORTER`, `OTEL_METRIC_EXPORT_INTERVAL`.

### `exemplar-cloudflared`

Tunnel builds only: the tunnel's routing, matched by path (`^/webhook$` to the webhook, everything else to the API), the same for every installation. cloudflared reads it at start, so restart the Deployment after changing it.

### `exemplar-pgbouncer`

`components/pgbouncer` only: `pgbouncer.ini`, the `[pgbouncer]` section. Transaction mode (`pool_mode = transaction`) with `max_prepared_statements = 200`, which the runners' prepared statements need; listens on 5432; `scram-sha-256` against the Secret's `userlist.txt`; `max_client_conn = 2000` and `default_pool_size = 20` per replica; `server_tls_sslmode = prefer`. It includes the Secret's `databases.ini` last.

## What the overlay sets besides

- The build: a release's `install-tunnel.yaml` or `install-direct.yaml`, or `deploy/k8s/base?ref=<version>` with both image digests ([Releases and images](releases.md)).
- The search volume's size and storage class (500Gi on `local-ssd` as placeholders).
- For Ingress builds, the host, class and TLS secret on both Ingresses, and the ingress controller's namespace in the two NetworkPolicy patches.
- For `components/postgres`, the volume's size and class.
- For `components/pgbouncer`, `default_pool_size` in the `exemplar-pgbouncer` ConfigMap, against the database's `max_connections`.
