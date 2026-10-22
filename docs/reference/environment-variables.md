# Environment variables

Every `EXEMPLAR_*` variable the binaries read, grouped by the binary that reads it. **Set by** says where the shipped manifests get the value: the `exemplar-settings` ConfigMap (patched per installation), the `exemplar-auth` ConfigMap, a Secret key, a fixed value in the manifest, or nothing (set it yourself where the row says so). Startup refuses placeholder values left from the examples (`example-org`, `Iv1.example`, `*.example.invalid`, `<…>`), naming each one. `go test ./docs` fails when the code reads a variable this page doesn't list; `go test ./deploy` fails when a manifest sets one the code doesn't read.

Keys that end in `_FILE` name a mounted file; the rest carry the value itself.

## Shared settings

Read by more than one binary. The ConfigMap is described on [Secrets and ConfigMaps](secrets-and-configmaps.md#exemplar-settings).

| Variable | Read by | Set by | Meaning |
|---|---|---|---|
| `EXEMPLAR_CONFIG_REPO` | api, worker, gateway, search, learn, git-sync | `exemplar-settings` | The config repository, `owner/name`. The App is registered on its owner; GitHub sign-in defaults to that owner's members. Required. |
| `EXEMPLAR_API_URL` | api, worker, the CLI | `exemplar-settings` | exemplar-api's public `https://` base. GitHub posts webhooks to it plus `/webhook`; the OAuth callback is `/oauth/callback`; feedback links and `exemplar review` use it. Required. |
| `EXEMPLAR_WEBHOOK_URL` | api | `exemplar-settings` (optional) | A separate `https://` host for webhooks. Empty means the API host. |
| `EXEMPLAR_GH_WEB_URL` | api, worker, gateway, search, learn, the CLI | `exemplar-settings` | GitHub's web base, `https://github.com/` by default; `https://<host>/` for GitHub Enterprise Server. Used for clone URLs and links. |
| `EXEMPLAR_GH_API_URL` | api, worker, search, learn, the CLI | `exemplar-settings` | GitHub's API base, `https://api.github.com/` by default; `https://<host>/api/v3/` for GHES. |
| `EXEMPLAR_DATABASE_URL` | webhook, api, worker, gateway, learn, the learning jobs, the CLI (`replay`, `calibrate`) | Secret `exemplar-database`, key `url` | The Postgres URL. The gateway uses it only to record `model_calls`. |
| `EXEMPLAR_ROOT_KEY_FILE` | api, worker, gateway, search, learn, the learning jobs | fixed path to Secret `exemplar-root-key`, key `key` | The root key file. The link, session, gateway, search and pseudonym keys derive from it with HKDF-SHA256 unless their explicit variable is set. |
| `EXEMPLAR_GATEWAY_URL` | worker, learn, the learning jobs, the CLI, config CI | `exemplar-settings` (`http://exemplar-gateway.exemplar.svc`) | exemplar-gateway's base URL. |
| `EXEMPLAR_GATEWAY_TOKEN` | gateway, worker, learn, the learning jobs, the CLI, config CI | derived from the root key | The gateway's shared secret. Set explicitly to rotate it alone, or outside the cluster (config CI). |
| `EXEMPLAR_SEARCH_URL` | worker | `exemplar-settings` (`http://exemplar-search.exemplar.svc`) | exemplar-search's base URL. Empty disables the repository tools. |
| `EXEMPLAR_SEARCH_TOKEN` | search, worker | derived from the root key | The search API's shared secret. Set explicitly to rotate it alone. |
| `EXEMPLAR_GH_APP_ID` | api, worker, search, learn, the CLI (`sandbox`) | Secret `exemplar-github-app`, key `app-id` | The review App's id. Without it exemplar-api runs in setup mode. |
| `EXEMPLAR_GH_INSTALLATION_ID` | search, learn, the CLI (`sandbox`) | Secret `exemplar-github-app`, key `installation-id` | The App's installation on the config repo's owner. The worker looks installations up per repo instead. |
| `EXEMPLAR_GH_PRIVATE_KEY_FILE` | api, worker, search, learn, the CLI (`sandbox`) | fixed path to Secret `exemplar-github-app`, key `private-key` | The App's private key (PEM). |
| `EXEMPLAR_AUTH0_DOMAIN`, `EXEMPLAR_AUTH0_AUDIENCE` | api; config CI (as repository variables) | `exemplar-settings` | Auth0 tenant domain and API audience for service callers of `/v1/evaluate`. Both empty turns service callers off. |
| `EXEMPLAR_AST_GREP` | worker, the CLI | nothing | Path to the `ast-grep` binary when it isn't `ast-grep` on `PATH`. |
| `EXEMPLAR_LOG_LEVEL` | every binary | nothing | `debug` for debug logging; anything else is info. |

## exemplar-api

Sign-in variables come from the `exemplar-auth` ConfigMap through `envFrom` ([How to set up sign-in](../guides/install/sign-in.md)).

| Variable | Set by | Meaning |
|---|---|---|
| `EXEMPLAR_LINK_KEY_FILE` | derived from the root key | Explicit feedback-link signing key file (shared with the worker). |
| `EXEMPLAR_SESSION_KEY_FILE` | derived from the root key | Explicit session key file. Rotating it signs everyone out; sessions last 8 hours. |
| `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID` | Secret `exemplar-github-app`, key `client-id` | The App's OAuth client id. Present means GitHub sign-in is on. Also the CLI's default `--client-id`. |
| `EXEMPLAR_OAUTH2_GITHUB_CLIENT_SECRET_FILE` | fixed path to Secret `exemplar-github-app`, key `client-secret` | The OAuth client secret file. |
| `EXEMPLAR_OAUTH2_GITHUB_ALLOWED_ORGS` | `exemplar-auth` | Comma-separated organizations whose members may sign in. Defaults to the config repo's owner. |
| `EXEMPLAR_OAUTH2_GITHUB_ALLOW_EVERYONE` | `exemplar-auth` | `true` lets any GitHub user sign in. |
| `EXEMPLAR_OAUTH2_GITHUB_ENTERPRISE_BASE_URL` | `exemplar-auth` | The GHES base URL for OAuth (`https://github.example.com`). |
| `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN` | `exemplar-auth` | `false` hides the GitHub sign-in button and keeps GitHub as the connect step only. |
| `EXEMPLAR_OIDC_ISSUER_URL` | `exemplar-auth` | The OIDC issuer. Set means OIDC sign-in is on; it must serve discovery. |
| `EXEMPLAR_OIDC_CLIENT_ID` | `exemplar-auth` | The confidential web client's id. |
| `EXEMPLAR_OIDC_CLIENT_SECRET_FILE` | fixed path to Secret `exemplar-oidc`, key `client-secret` | Its secret file; read only when the issuer is set. |
| `EXEMPLAR_OIDC_CLI_CLIENT_ID` | `exemplar-auth` | The public client with the device flow, for `exemplar login --oidc`. Also the CLI's default. |
| `EXEMPLAR_OIDC_SCOPES` | `exemplar-auth` | Comma-separated scopes to request. |
| `EXEMPLAR_OIDC_EMAIL_FIELD`, `EXEMPLAR_OIDC_USERNAME_FIELD`, `EXEMPLAR_OIDC_GROUP_FIELD` | `exemplar-auth` | Claim names for the email, username and groups. |
| `EXEMPLAR_OIDC_EMAIL_DOMAIN` | `exemplar-auth` | Comma-separated email domains allowed to sign in. |
| `EXEMPLAR_OIDC_ALLOWED_GROUPS` | `exemplar-auth` | Comma-separated groups allowed to sign in. |
| `EXEMPLAR_OIDC_GROUP_REPOS` | `exemplar-auth` | JSON object mapping a group to repo patterns, `{"engineering": ["acme/*"]}`. The only source of repo access for OIDC users. |
| `EXEMPLAR_OIDC_AUTH_URL_PARAMS` | `exemplar-auth` | JSON object of extra authorization-request parameters. |
| `EXEMPLAR_OIDC_IGNORE_EMAIL_VERIFIED` | `exemplar-auth` | `true` accepts an unverified email claim. |
| `EXEMPLAR_OIDC_IGNORE_USERINFO` | `exemplar-auth` | `true` skips the userinfo endpoint and uses the ID token's claims only. |
| `EXEMPLAR_OIDC_SIGN_IN_TEXT`, `EXEMPLAR_OIDC_ICON_URL` | `exemplar-auth` | The sign-in button's text and icon. |
| `EXEMPLAR_OIDC_GITHUB_LINK` | `exemplar-auth` | With both sources: `required` (default), `optional` or `off` for connecting a GitHub account after OIDC sign-in. |

## exemplar-webhook

| Variable | Set by | Meaning |
|---|---|---|
| `EXEMPLAR_WEBHOOK_SECRET_FILE` | fixed path to Secret `exemplar-github-app`, key `webhook-secret` | The HMAC secret every delivery is checked against. |

## exemplar-worker

Besides the shared settings: `EXEMPLAR_LINK_KEY_FILE` (as for the API; the worker signs the footers), `EXEMPLAR_GATEWAY_TOKEN`, `EXEMPLAR_SEARCH_TOKEN` and `EXEMPLAR_AST_GREP`.

## exemplar-gateway

| Variable | Set by | Meaning |
|---|---|---|
| `EXEMPLAR_GATEWAY_ADDR` | manifest (`:8080`) | The listen address. |
| `EXEMPLAR_CONFIG_DIR` | manifest (the git-sync checkout) | The `exemplar-config` checkout, for `global.yaml`'s providers and budgets. Required. |
| `EXEMPLAR_GATEWAY_RPM` | nothing | Requests per minute allowed per caller; unset or 0 means no limit. |
| `EXEMPLAR_CLAUDE_BASE_URL` | nothing | Overrides `https://api.anthropic.com` (tests, private endpoints). The key itself is `ANTHROPIC_API_KEY`, from Secret `exemplar-providers`, key `anthropic-api-key`. |
| `EXEMPLAR_BEDROCK_REGION` | nothing | The Bedrock region; `AWS_REGION` is the fallback. Credentials follow the AWS default chain (workload identity, instance role, `AWS_*`, a shared profile) or a Bedrock API key in `AWS_BEARER_TOKEN_BEDROCK`. |
| `EXEMPLAR_BEDROCK_BASE_URL` | nothing | Overrides the regional endpoint (tests, VPC endpoints). |
| `EXEMPLAR_BEDROCK_PROFILE` | nothing | A named AWS profile, for local runs only. |

## exemplar-search

Besides the shared settings and `EXEMPLAR_SEARCH_TOKEN`:

| Variable | Set by | Meaning |
|---|---|---|
| `EXEMPLAR_GH_TOKEN` | nothing | A GitHub token for `exemplar-search bench`, the one-off monorepo benchmark. `serve` uses the App. |

## exemplar-learn and the learning jobs

The CronJobs set the shared settings per job. The Python `cluster` and `rank` jobs read `EXEMPLAR_DATABASE_URL`, `EXEMPLAR_GATEWAY_URL`, `EXEMPLAR_GATEWAY_TOKEN` and `EXEMPLAR_ROOT_KEY_FILE`.

| Variable | Read by | Set by | Meaning |
|---|---|---|---|
| `EXEMPLAR_PSEUDONYM_KEY` | `ingest`, `path-events` | derived from the root key | The key that pseudonymises comment authors. Set explicitly to rotate it alone; changing it breaks joins with older mined data. |
| `EXEMPLAR_DOC_HOSTS` | `ingest`, `path-events` | nothing | Comma-separated internal documentation hosts, so links to them in comments are kept as citations (`--doc-hosts`). |
| `EXEMPLAR_BOT_LOGINS` | `ingest`, `path-events` | nothing | Comma-separated extra bot logins to skip, including the Exemplar app's (`--bot-logins`). |
| `EXEMPLAR_EXPORTS_APP_ID` | `export` | Secret `exemplar-exports-app`, key `app-id` (optional) | The exports App's id. Empty skips the export run. |
| `EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE` | `export` | fixed path to Secret `exemplar-exports-app`, key `private-key` | Its private key file. |

## The CLI and config CI

Read by `exemplar` subcommands as flag defaults ([The CLI](cli.md)) and by the workflows in `exemplar-config` ([How to set up config CI](../guides/rules/config-ci.md)).

| Variable | Used by | Meaning |
|---|---|---|
| `EXEMPLAR_TOKEN` | `review`, `eval --remote` | A bearer token for exemplar-api instead of `exemplar login`'s saved credentials: an Auth0 service token or a GitHub Actions OIDC token. |
| `EXEMPLAR_AGENT` | `review` | The coding agent running the review (`--agent`), for example `claude_code`. |
| `EXEMPLAR_EVAL_TOKEN` | `eval`, `volume`, `linter-overlap`, `calibrate` | A read-only GitHub token for the case repositories (`--token-env` names the variable; this is the default). `linter-overlap` needs `checks: read` on them. |
| `EXEMPLAR_TEAMS_TOKEN` | `export-approval` | A token with `members: read`, to read the platform team's membership (`--team-token-env`). |
| `EXEMPLAR_LINTER_OVERLAP` | config CI | Repository variable `true` turns the `linter-overlap` job on. |
| `EXEMPLAR_CALIBRATION_DATABASE_URL` | config CI (`nightly.yml`) | A read-only database URL so nightly calibration includes `verifier_labels` from author feedback. Passed to the CLI as `EXEMPLAR_DATABASE_URL`. |
| `EXEMPLAR_AUTH0_CLIENT_ID`, `EXEMPLAR_AUTH0_CLIENT_SECRET` | config CI | Repository secrets for the Auth0 client the `eval` job exchanges for `EXEMPLAR_TOKEN`. |

## Tests only

| Variable | Meaning |
|---|---|
| `EXEMPLAR_REQUIRE_DB` | `1` makes the Postgres tests fail instead of skip when no container runtime is available (CI sets it). |
| `EXEMPLAR_REQUIRE_ENGINES` | `1` makes engine-dependent tests (ast-grep, Semgrep, ctags, ripgrep, clang-format, git) fail instead of skip (CI sets it). |

## Grafana

| Variable | Meaning |
|---|---|
| `EXEMPLAR_GRAFANA_METRICS_URL` | In `dashboards/provisioning/datasource.yaml`: the Prometheus-compatible endpoint the `exemplar-metrics` datasource reads the exported OTel metrics from. |

## OpenTelemetry

The standard `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_METRICS_EXPORTER` and `OTEL_METRIC_EXPORT_INTERVAL`, read by every container from the optional `exemplar-otel` ConfigMap and Secret ([How to send telemetry](../guides/operate/telemetry.md)). Only OTLP over HTTP is built in.
