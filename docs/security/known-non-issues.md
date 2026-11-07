# Threat model pointers and known non-issues

What `/security-hunt` reads before it hunts. The threat model itself is **§13 of the
[technical spec](../spec/technical-spec.md)** (the network boundary, the threat table,
and §13.1 data handling); this file only points at it, lists the authorization
primitives a hunter must look for, and records the findings that keep coming back and
why they are not bugs. Edit the non-issues list only after a human has reviewed the
reasoning: it suppresses future findings.

## Authorization primitives

| Where | Guard | Code |
|---|---|---|
| Webhook | HMAC over the raw body with the App's webhook secret, before any parsing | `internal/webhook` (`github.ValidatePayload`) |
| exemplar-api `/ui/`, `/v1/` | Session from GitHub OAuth or OIDC; every page and list filtered by the viewer's repo access (GitHub permissions, or OIDC group → repo map) | `internal/api`, `internal/web`, `internal/oidc`, `internal/ghuser` |
| exemplar-api `/v1/evaluate` (services) | Auth0 client-credentials token with an `evaluate:<org>/<repo>` permission, matched against the request's repo | `internal/svcauth`, `internal/auth0` |
| Feedback links | HMAC signature bound to one finding and one reason; no expiry of its own: a link stops working when retention deletes its finding, and recording needs sign-in and read access to the finding's repo; key derived from the root key | `internal/feedbacklink`, `internal/api` |
| Setup page | Bootstrap token from the `exemplar-setup` Secret (constant-time compare), then a 12 h HMAC-signed session cookie (HttpOnly, Secure on https, SameSite=Lax); manifest `state` HMAC-signed with a 1 h expiry and bound to a cookie; form token HMAC-derived from the same key; the key lives in `exemplar-setup` so every replica checks the same | `internal/setup` |
| Gateway, search | Shared token in `X-Api-Key`, constant-time compare, derived from the root key | `internal/llm/gateway.go`, `internal/search/api` |
| GitHub App tokens | Installation tokens minted per job with a *flavor* (read, review, config-writer, …) and a repo list; `checkScope` refuses a flavor the caller may not have | `internal/gh/app.go` |
| Model tools | Paths resolved inside the checkout; read-only tools; search and git calls rate-limited and capped | `internal/engine/tools.go`, `internal/search` |
| Model output | Provenance from the rule, not the model; anchors must be in visible hunks; schema-invalid output discarded; verifier evidence must resolve | `internal/engine` |
| Text leaving for a model | Redaction scanner in the worker, and again in the gateway; repo and path eligibility denylist | `internal/redact`, `internal/exclude` |
| Auto-approve | Deterministic Stage 1 gates (path denylist, author policy, fork, size, bug lane ran) before any model-derived input; at most one approval where two are required; revoked on push | `internal/gates`, `internal/worker/mergepolicy.go` |
| Compile job | Model-written rules run only inside `unshare --net --map-root-user`; `--require-sandbox` is never turned off | `cmd/exemplar-learn` compile |

## Known non-issues

One line each: what a hunter will see, and why it is not a bug. A non-issue stops being
one when its reason stops holding.

- **The setup page is on the public host while setup mode lasts.** Its only authorization is the bootstrap token in the `exemplar-setup` Secret: reading it takes `get` on Secrets in the namespace, the same access the old port-forward required. The token is never logged; the log prints the `kubectl` command that reads it. A route under `/setup` that skips the session check *is* a finding.
- **exemplar-api keeps `get` and `patch` on seven named Secrets after setup.** The `exemplar-api-setup` Role and the projected token stay mounted in App mode, though App mode makes no Kubernetes API call (tracked as a follow-up issue). It can't `create`, `list` or `delete`, and the names are fixed in `deploy/k8s/core/rbac.yaml`. Report a path from a request to a Kubernetes API call in App mode.
- **The worker (control) holds the App's private key and could mint any token flavor.** Known and tracked as a deployment risk (`docs/reference/workloads.md`, "Known limits"); `checkScope` is the guard on what it mints. Review runners hold only their job's tokens from a single-use claim (`internal/worker/claims.go`); with `--launcher=in-process` they run in the worker's process. Report a path by which a runner gets a token outside its claim, or redeems another job's claim. Report only a path that bypasses `checkScope`.
- **The gateway and search accept a static shared token with no per-caller identity.** They are reachable only inside the namespace (NetworkPolicies admit the worker and learn jobs); the token derives from the root key. A route on either that skips the token check *is* a finding.
- **A pod with the root key can derive every internal key.** Decided in `docs/plans/ready-to-install.md` §4.2 and open to Security in its §6; not a code finding.
- **`/webhook` is reachable from any source in tunnel builds until the operator adds the WAF rule.** Documented install step; every delivery is HMAC-checked regardless. The rejection log's `remote` is a cloudflared pod's address, not GitHub's.
- **The gateway starts without a provider's credentials.** Intended (`llm.BuildProviders`): a routed provider without credentials is skipped with a warning as long as every stage keeps one that works.
- **Model-written Semgrep and ast-grep rules exist in the config repo.** They run only in the sandboxed compile job, which is suspended until the cluster provides user namespaces. The worker runs merged, human-reviewed compiled rules in live reviews without limits; unmerged bodies (mode eval on config PRs, the playground) run under prlimit data and CPU caps (`compiled.LimitCommand`), not in a network sandbox. Report a path that runs an unmerged body without those limits.
- **PR descriptions, comments and ticket text reach prompts.** That is the product; the defenses are the verifier, provenance and anchor validation, the adversarial eval set and the "model never approves outside bounded gates" rule. Report a path where injected text changes what is *posted or approved* without passing those, not the fact that the text is read.
- **Secrets in the diff are sent to the model after redaction.** The scanner replaces them; a secret finding is posted. Report only a route that bypasses `internal/redact` or the gateway's second scan.
