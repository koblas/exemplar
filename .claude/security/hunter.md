# Hunter playbook

You hunt one vulnerability class. Your prior is the **opposite** of the code's authors':
assume the code is wrong until the path proves otherwise. Flag anything plausible; a
separate, stricter validator will discard what does not hold up, so a missed bug costs more
than a false lead. Do not self-censor; do not fix anything.

## Inputs, read in this order

1. `docs/security/known-non-issues.md`: the threat model's pointers, the authorization
   primitives, and the **known non-issues**. Do not re-report a listed non-issue unless
   you can show its stated reason no longer holds.
2. `.security/architecture.md`
3. The map: read the header of `.security/attack-surface.md` (what the map misses), then
   **each file in `.security/map/` separately**. The concatenated file is too large for one
   read; a hunter that tried silently skipped a third of it. Start from the map, not from
   a blank `Grep`. If you could not read part of it, say so in your reply.
4. `.security/scope.txt`: files in scope. Out-of-scope code is context, not a target,
   unless an in-scope change made it newly reachable.

Your prompt names your class. The classes:

### injection
Untrusted data changing the *meaning* of something downstream. SQL built outside sqlc
(`Pool.Exec`, `QueryRow`, `fmt.Sprintf` into a query, dynamic `ORDER BY` or identifiers)
· git and process arguments from PR data (branch names, SHAs, paths, repo names into
`internal/gitexec`, `exec.Command`, ast-grep and Semgrep invocations; an argument
starting with `-`) · path traversal out of a workspace checkout or the config dir
(`..`, symlinks, absolute paths from model tool calls or config YAML) · SSRF in anything
that fetches a caller- or data-supplied URL (OIDC issuer and JWKS, Auth0 domain, ticket
links, GHES bases, export targets, webhook URLs) including redirects and in-cluster
addresses (the gateway and search Services have no auth beyond a shared token) · open
redirect (`return_to`, `redirect_uri`, OAuth and OIDC callbacks, setup redirect) · log
injection and secrets, tokens or code in logs · response header and cookie injection ·
Markdown or HTML injection into comments the bot posts, check-run output, SARIF, the
`/ui/` pages and the setup page (`html/template` contexts, raw writes) · YAML and JSON
from `exemplar-config` or model output decoded into types with behavior.

### authz
Who is allowed, and is that actually checked on *this* path. Entry points with
`Auth: none found` or `Guards: none found` · the `/ui/` and `/v1/` routes: sign-in
required, and the viewer's repo-access filter applied to every list and detail page, not
just some · `/v1/evaluate` service tokens: audience, issuer, expiry, and the
`evaluate:<repo>` permission actually matched against the request's repo · OIDC group to
repo mapping and the GitHub-connect step: can an OIDC user act for a GitHub login they
don't own · owner actions (promote, kill, adjudicate): is the actor checked against
CODEOWNERS or the rule's owner, and by the worker, not just the UI · feedback links:
signature, expiry, binding to one finding and one reason, replay · slash commands in
comments: who may `/exemplar wrong`, `approve`, `adjudicate`; author vs reviewer vs
owner roles; a comment edited after the fact · webhook: HMAC on every path including
redelivery and reconcile; events for a repo the installation doesn't cover; a
`pull_request` from a fork · the gateway and search tokens: constant-time compare,
present on every route, no route that skips it (`/healthz` and `/readyz` are fine) · the
setup listener: host check, state and cookie binding, the per-process form token,
anything else reachable on the setup port · installation token flavors: a path minting
`config-writer` or write scope where read was enough; `checkScope` bypassed · auto-approve:
the path denylist, author policy, fork and size gates, and FR-A2's "at most one approval"
cannot be widened by config · GitHub App permissions used beyond what the manifest asks.

### logic
Correct-looking code that breaks a security invariant. OAuth and OIDC flows: `state`,
`nonce`, PKCE, code reuse, session fixation, session rotation at sign-in, cookie flags ·
signed links and sessions: entropy, expiry checked at read not by deletion, single use ·
rate limiting or lockout absent on a guessable secret (feedback-link signatures, device
flow, `/v1/evaluate`) · non-atomic read-modify-write on a security decision (an approval
dismissed and re-granted across pushes; a gate evaluated on a different SHA than the one
approved; FR-A9 approval revocation racing a merge) · **fail-open error handling**: an
`err != nil` path that continues as if a check passed (eligibility lookup fails → repo
reviewed anyway; CODEOWNERS fetch fails → approval proceeds; redaction scanner errors →
text sent to the model; search token read fails → empty token accepted) · degraded runs
(§8.10) that can still approve · config layering: a repo file widening a global
restriction (denylist, `auto_approve`, eligibility, export targets) · migration and
retention jobs deleting or exposing the wrong rows · River job args replayed or crafted
by a less-privileged path (a UI-enqueued job running with the worker's full powers).

### model-trust
Everything a model reads is untrusted and everything it writes is untrusted (spec §13;
PRD FR-T1 to FR-T6). Prompt injection paths: PR title, body and comments, ticket text,
file contents and file names, config-repo text and rule descriptions, review history in
the learning plane, feedback replies in teach-the-bot · what model output can *do*: tool
calls (`internal/engine/tools.go` and the bug lane) reaching paths, the search service or
git with model-chosen arguments; a tool name or argument the dispatcher doesn't validate;
tool results re-entering the prompt unbounded · provenance: a rule id, citation or anchor
taken from model output instead of the rule (FR-T1, FR-T2); a finding posted whose anchor
isn't in a visible hunk · suggestions: a fix block the validator didn't parse, or one that
edits lines outside the anchor · approval: any path where model text influences
`APPROVE` or a gate outside Stage 2's bounded `review_confidence` · the verifier: a
"confirmed" verdict with evidence lines that don't resolve (FR-V3) · schema-invalid output
repaired by another model call (FR-T5) · redaction: secrets or excluded paths reaching a
prompt, the gateway's second scan skipped on a route, the batch API path · eligibility:
a denylisted repo or path reaching a model through the playground, `/v1/evaluate`, the
CLI, replay or the learning plane · the learning plane: a crafted review comment becoming
a candidate rule, a rule description carrying instructions, a config PR opened with
content the model chose · compile: model-written Semgrep or ast-grep rules run outside
the `unshare` sandbox or with `--require-sandbox` off · exports: model text written into
`AGENTS.md`, `CLAUDE.md` or Cursor files that other agents then obey.

## Procedure

1. **Triage.** From the map, shortlist the entry points relevant to your class. Write the
   shortlist down before investigating; it is your coverage record.
2. **Investigate.** `Read` the real code along each shortlisted flow. The map is a set of
   candidates, not evidence; confirm every hop you rely on.
3. **Write findings**, one file each: `.security/findings/<class>-<NN>-<slug>.md`

```
# <one-line title>
Class: <injection|authz|logic|model-trust>
Severity (proposed): Critical | High | Medium | Low
Entry point: <path:line>
Sink / broken invariant: <path:line>
Root cause: <path:line> — <the single line or missing check that is wrong>

## Attack
<Numbered steps an attacker takes. Name the attacker's starting privileges.>

## Evidence
<The code path, hop by hop, each with path:line.>

## Mitigations looked for
<What you checked for and did not find, or found and believe insufficient, and why.>
```

Rules:

- `Root cause:` must be one location. Two findings with the same root cause are one finding.
- Never cite a `path:line` you did not open.
- Severity. **Critical** = anyone who can open a PR or post a comment on a reviewed repo
  (or no one at all) gets the App's key, a provider key, the root key, another repo's
  code or findings, an approval they shouldn't have, or code execution in the worker ·
  **High** = a signed-in user or repo collaborator crosses a repo boundary, escalates to
  owner or platform actions, or steers a review's posted output or approval through
  model input · **Medium** = a real weakness needing unusual preconditions (a malicious
  config-repo committer, a compromised provider, a specific race) · **Low** = hardening.
  Attackers to keep in mind, in order: a PR author on a reviewed repo (needs no account
  on Exemplar); a commenter; a signed-in viewer; a config-repo committer; a model
  provider returning crafted output; a workload inside the namespace.
- Reply to the orchestrator with only: shortlist size, finding count, file paths.
