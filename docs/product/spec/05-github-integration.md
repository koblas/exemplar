## 5. GitHub integration

Exemplar uses **two GitHub Apps** so that write access to repository contents (rule exports) is isolated from the app that reviews PRs.

**`exemplar` (review app) — permissions**

| Permission | Level | Used for |
|---|---|---|
| Metadata | read | Required |
| Contents | read | Clone, read CODEOWNERS and `.exemplar/` from base |
| Pull requests | read & write | Read diffs/threads; submit reviews, comments, approvals |
| Checks | read & write | Own check runs; **read other check runs' annotations for linter dedupe** (§6.10) |
| Code scanning alerts | read & write | Upload SARIF for Security-category findings; read dismissal reasons as feedback (§6.12) |
| Issues | read & write | PR conversation replies; **linked-issue context** (§6.4); write only in `exemplar-config`, for flag issues and owner digests (§9.4) |
| Members (org) | read | Team CODEOWNERS and allowed-author teams |
| Administration | read | Branch protection lookup for Stage 2 |

On `exemplar-config` only, the review app is installed with **Contents: write** so it can open candidate, suppression and demotion PRs; tokens for that repo are minted separately and never reach agent workers.

**`exemplar-exports` (export app)** — installed only on repos that opt into rule exports; permissions Contents: write, Pull requests: write, Metadata: read. It does nothing but open and update the generated-instructions PRs (§10.2). It never reads PR content and never runs model code.

**Token flavors from the review app** (down-scoped at mint time):

| Holder | Repos | Permissions |
|---|---|---|
| Agent workers (all lanes, verifier) | the one PR repo | `contents:read` , `pull_requests:read` |
| Context provider (code search, issues) | allowlisted repos per §6.4 policy | `contents:read` , `issues:read` |
| Poster | the one PR repo | `pull_requests:write` , `checks:write` , `security_events:write` |
| Config writer | `exemplar-config` only | `contents:write` , `pull_requests:write` , `issues:write` |

**As built,** one process, `exemplar-worker`, mints the first three flavors per job (a clone token, a gate token for CODEOWNERS and branch-rule lookups, a poster token) and the config-writer token only in the learn jobs and owner-action job, so the App private key is in the worker. With the control/runner split (§2.4 C1–C2) the key stays in the control process, which mints a review's tokens into a single-use claim; the runner, the only process that runs model-facing code, receives tokens for one repository and can mint nothing. Minting a poster or config-writer token for `exemplar-config` from a review path is refused in code either way.

**Subscribed events:** `pull_request` (opened, synchronize, reopened, ready_for_review, edited, closed), `pull_request_review`, `pull_request_review_comment`, `pull_request_review_thread`, `issue_comment` (slash commands), `check_run` (completed — for late linter dedupe; rerequested; requested_action — Re-run), `code_scanning_alert` (closed_by_user, reopened_by_user — feedback), `push`, `installation`, `installation_repositories`.

**Rate limits:** an installation on a GitHub Enterprise Cloud org gets 15,000 REST requests/hour; outside Enterprise Cloud the floor is 5,000 and scales with users and repos. Wrap the `go-github` HTTP client with `gofri/go-github-ratelimit` (secondary limits) and a shared `x/time/rate` limiter per installation, prefer GraphQL for thread/reaction reads, and keep per-review API calls to roughly 15–25 (the extra over v0.2 is check-run annotation reads and one SARIF upload; see §6.12). Content-creation endpoints have stricter secondary limits — another reason to post one review with many comments rather than many single comments.

**Webhook reliability:** GitHub does not automatically retry failed deliveries. Run two safety nets: (a) a reconciler that lists the app's failed deliveries and requests redelivery; (b) a sweeper every 10 minutes that runs one GraphQL `search(type: ISSUE, query: "is:pr is:open updated:>…")` over opted-in repos (not a REST list per repo) and enqueues any `(repo, pr, head_sha)` without a review row.

---
