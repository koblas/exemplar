# Security

Exemplar reviews pull requests with model calls and learns rules from past reviews. An
installation holds what that takes: a GitHub App that can read the repositories it is installed
on and write reviews, approvals and check runs to them; model-provider keys; source code
mirrored on the search volumes; and review history, transcripts and comment text in Postgres.
This file lists what is worth attacking and where the defenses live. It is for security reviews;
build rules live in `.claude/`.

The threat model itself is [§13 of the technical spec](docs/product/spec/13-security-model.md).
The guard for each authorization surface, and the findings reasoned not to be bugs, are in
[docs/security/known-non-issues.md](docs/security/known-non-issues.md).

## Reporting a vulnerability

Do not open a public issue. Contact the maintainer privately through GitHub. Include the commit
or release you ran, the request or event that triggers it, and what an attacker gains.

## What is worth protecting

Every Secret below lives in the `exemplar` namespace; no value is in this repository or the
rendered manifests ([Secrets and ConfigMaps](docs/reference/secrets-and-configmaps.md)).

| Asset | Where it lives | Rule |
| --- | --- | --- |
| GitHub App credentials (private key, webhook secret, OAuth client secret) | Secret `exemplar-github-app` | The private key mints installation tokens of every flavor; `checkScope` (`internal/gh/app.go`) limits what each caller may mint. The webhook secret is the only check on inbound deliveries. |
| Root key | Secret `exemplar-root-key` | Every internal key (feedback links, sessions, gateway and search tokens, pseudonyms, runner claims) derives from it with HKDF (`internal/envcfg`). A pod holding it can derive them all. |
| Model-provider keys | Secret `exemplar-providers` | Mounted only by `exemplar-gateway`; no other binary may reach a provider host. |
| Setup credentials | Secret `exemplar-setup` | The setup page's bootstrap token and signing key. Unused after setup. |
| Database URL and password | Secrets `exemplar-database`, `exemplar-postgres` | Postgres holds findings, approvals, transcripts, mined comment bodies and review history. |
| OIDC client secret, exports App key | Secrets `exemplar-oidc`, `exemplar-exports-app` | The exports App writes only to opted-in repos and is never installed on the config repo. |
| Source code | `exemplar-search` volumes, review runners' checkouts | Read-only GitHub tokens; search is reachable only from workers and the API; a runner's volume lives for one review. |
| Text sent to models | Gateway requests | Redacted twice, in the worker and again in the gateway (`internal/redact`); denylisted repos and excluded paths never leave (`internal/exclude`). |

## Entry points

| Surface | Who can reach it | Guard |
| --- | --- | --- |
| `/webhook` | GitHub, and any source until the operator adds the WAF rule | HMAC over the raw body before any parsing (`internal/webhook`). |
| `/setup` | The public host while setup mode lasts | Bootstrap token from `exemplar-setup`, then a signed session cookie; manifest `state` and form tokens signed too (`internal/setup`). |
| `/ui/` and `/v1/` | Signed-in people | GitHub OAuth or OIDC session; every page and list filtered by the viewer's repo access (`internal/api`, `internal/web`, `internal/oidc`, `internal/ghuser`). |
| `/v1/evaluate` | Service callers | Auth0 token whose `evaluate:<org>/<repo>` permission must match the request (`internal/svcauth`, `internal/auth0`). |
| Feedback links | Anyone holding a link | HMAC-signed per finding and reason; recording still needs sign-in and read access (`internal/feedbacklink`). |
| Gateway, search | Inside the namespace only | Shared token in `X-Api-Key`, constant-time compare (`internal/llm/gateway.go`, `internal/search/api`). |

## The model boundary

- PR descriptions, comments, repository files and ticket text reach prompts; that is the
  product. Injected text is the attack to review: a path where it changes what is posted or
  approved without passing the verifier, provenance and anchor validation is a finding.
- The model's tools are read-only, resolve paths inside the checkout and make no network calls
  (`internal/engine/tools.go`).
- Rule ids, provenance and severity come from code, never from model output
  (`internal/engine`).
- Auto-approve passes deterministic gates (path denylist, author policy, forks, size, bug lane)
  before any model-derived input, and is revoked on push (`internal/gates`).
- Model-written rules run only in the sandboxed compile job (`--require-sandbox` is never turned
  off); unmerged rule bodies run under CPU and memory limits.

## Sensitive paths

A change under any of these paths gets a security review before it merges. The list is the
`security_paths` pattern in `.claude/pipeline.json`; keep the two in step.

- `internal/auth0/`, `internal/oidc/`, `internal/svcauth/`
- `internal/feedbacklink/`, `internal/webhook/`
- `internal/envcfg/`, `internal/redact/`

Also review any change to `internal/setup/`, token minting in `internal/gh/app.go`, the session
and repo-access filter in `internal/api/` and `internal/web/`, the gateway and search token
checks, runner claims (`internal/worker/claims.go`), `internal/gates/`, `internal/exclude/`, the
compile job in `cmd/exemplar-learn`, and the NetworkPolicies and RBAC under `deploy/k8s/`.

## Reviews

`/security-hunt` runs the security review; the user runs it, CI never does. Each confirmed
finding becomes an issue labelled `security`. Each finding reasoned to be a non-issue goes in
`docs/security/known-non-issues.md` so it is not reported again.

## Not in scope

The security of GitHub, the model providers and the cluster itself; Kubernetes hardening beyond
what `deploy/k8s/` declares; a hosted trial, which is outside the design until it has its own
tenancy.
