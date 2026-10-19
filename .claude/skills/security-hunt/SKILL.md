---
name: security-hunt
description: "Six-phase security review of Exemplar: map the attack surface deterministically, hunt by vulnerability class in parallel (injection, authz, logic, model-trust), dedupe, validate each finding adversarially, report. Scope is changed files (default), a path, a PR, or `all` for the whole system."
argument-hint: "<optional scope: path(s), \"pr <number>\", \"staged\", or \"all\">"
disable-model-invocation: true
allowed-tools: Read, Write, Glob, Grep, Bash, Agent
---

Security review of: **$ARGUMENTS**

You orchestrate. You do not hunt or validate yourself: hunters and validators have
opposite priors on purpose, and an orchestrator that does both collapses them into one.

Typing this command is the user's ask for every subagent below. They are all
`subagent_type="general-purpose"` with a role playbook from `.claude/security/`, so
nothing here needs a session restart.

**Prompts stay short.** Each subagent prompt is: *"Read `.claude/security/<role>.md` and
follow it."* plus the one-line delta (slice, class, or finding file). Never paste a
playbook, the map, or a finding into a prompt; subagents read the shared files.

## What Exemplar is, in one paragraph (for your own orientation)

A GitHub App on Kubernetes that reviews pull requests with model agents. Bytes come in
from GitHub webhooks (`exemplar-webhook`), people and the CLI (`exemplar-api`: sign-in,
signed feedback links, `/ui/`, `/v1/evaluate`), and from the pull requests, comments,
tickets and repository contents the worker reads. The worker runs model agents whose
tool calls reach a checkout, `exemplar-search` and GitHub; the gateway alone holds model
keys; the worker alone holds the App's write key and posts comments, check runs and
approvals. The learning CronJobs read review history and open config PRs. **Everything a
model reads is untrusted, and so is everything a model writes**: the spec's security
model is §13 of `docs/spec/technical-spec.md`, and the threat model's known non-issues
are in `docs/security/known-non-issues.md`.

## Budget

A hunt is the most expensive single thing this repository runs.

- **The user runs this.** It is not part of `make check` or CI, and nothing else invokes it.
- **Size gate, after Phase 2 step 2 and before any spawn.** Print the scope file count and
  the narrowed entry-point row count. More than **60 scoped files or 80 rows** and the
  argument was not literally `all` → stop and put the numbers to the user.
- **Models are named, never inherited.** Hunters and validators run `model: "opus"`; a
  session on a pricier model must not silently raise the bill. Cartographers and dedupe as
  stated below.
- **Hunt only the classes the scope can reach.**
  - Scope entirely under `deploy/`, `dashboards/`, `docs/`, `schema/` or `examples/k8s/` →
    stop: "not on a request path"; `go test ./deploy` and `/k8s-manifests` cover manifests.
  - Scope entirely under `internal/learn/`, `learning/` or `cmd/exemplar-learn/` →
    `injection` and `model-trust` only; the report says `authz` and `logic` were skipped
    and why.
  - Scope entirely under `cmd/exemplar/` (the CLI) → `injection` and `logic` only.
  - Anything else, and `all`, runs all four.
  - No request-path rows and nothing from the cartographer's caller trace → no hunters;
    report "not on a request path".
- **Every finding is still validated**; the validator is the only upward correction of a
  hunter's severity. A finding proposed `Low` gets a `model: "sonnet"` validator; one it
  re-rates above `Low` is re-validated on `opus`.
- **Spawn in the foreground**, each phase's agents in one message
  (`run_in_background: false`).
- **Report the cost.** The final report ends with the count of subagents spawned per role.

All output goes under `.security/` (gitignored).

**Previous run.** If `.security/FINDINGS.md` exists, move the whole directory aside first:
`mv .security .security.prev-$(date +%Y%m%d-%H%M%S)` (also gitignored). Do **not**
`rm -rf`: a recursive delete gets denied, and silently skipping it lets stale findings
from the last run get validated and reported as new. Then
`mkdir -p .security/map .security/slices .security/findings`.

**Sandbox.** This repository's Bash sandbox may deny reading the worktree and writing
`.security/`. If a Bash call fails with `Operation not permitted`, re-run it with
`dangerouslyDisableSandbox: true`; expect it and do not treat it as a failure. Subagents
should prefer `Read`, `Grep` and `Glob`, which are not sandboxed, and the entry-point
script and `git` commands are the only shell they need.

## Phase 1 — Preparation (you)

1. **Resolve scope** into `.security/scope.txt`, one path per line:
   - no argument → changed vs the default branch:
     `git diff --name-only origin/main...HEAD`, plus `git diff --name-only HEAD`,
     `--cached`, and `git ls-files --others --exclude-standard`. Deduplicate.
   - `staged` → `git diff --name-only --cached`
   - `pr <n>` → `gh pr diff <n> --name-only`
   - path(s) → `git ls-files <path>`
   - `all` → `git ls-files cmd internal learning/src deploy/docker`
   Drop `*_test.go`, `learning/tests/`, generated code (`internal/store/queries/`,
   `learning/src/exemplar_learning/contracts/`), `docs/`, `examples/`, fixtures and
   lockfiles.
   **Empty scope → stop and say so.** Do not fall back to `all` silently; that is a
   ten-times-larger bill nobody asked for.
2. **Read `docs/security/known-non-issues.md`.** Missing → stop and tell the user;
   hunters without the non-issues list re-report every known false positive.
3. **Write `.security/architecture.md`**, under 60 lines: the binaries under `cmd/` and
   what each listens on or consumes; the inbound paths (webhook HMAC, the API's sign-in
   sources and signed links, `/v1/evaluate`'s service tokens, the gateway and search
   tokens, the setup listener); who holds which credential (gateway: provider keys;
   worker and git-sync: the App key and its token flavors; api: the OAuth client secret
   and session key); the model boundary (what goes into prompts, what tools model output
   can call, what checks model output before it is posted); the stores (Postgres, the
   River queue, the search mirror, workspace checkouts); and the write paths to GitHub
   (comments, check runs, reviews and approvals, SARIF, config and export PRs).
   Summarise from spec §13, `docs/architecture.md` and the `cmd/*/main.go` doc comments;
   do not re-derive the system from scratch.

## Phase 2 — Map (deterministic, then Haiku)

1. Run `.claude/security/entrypoints.sh > .security/entrypoints.tsv`.
   Sandboxed shell (`Operation not permitted`) → re-run with
   `dangerouslyDisableSandbox: true`.
2. **Narrow to scope** unless scope is `all`: keep rows whose file is in `scope.txt`, plus
   rows whose package contains a scoped file, plus every `authz-wiring` and `model-tool`
   row (a change anywhere can be reached through the same front door or the same tool
   call). Overwrite `entrypoints.tsv` with the narrowed set and note the before/after row
   counts.
3. **Zero rows after narrowing** → the change is not on a request path. Spawn ONE
   cartographer (`model: "sonnet"`) with the delta *"no entry points matched; trace
   inbound callers of the files in `.security/scope.txt` to the nearest entry point"*.
4. Otherwise slice rows into **at most 8 slices of roughly equal size**, by binary or
   package (`cmd/exemplar-webhook` + `internal/webhook`, `internal/api` + `internal/web`,
   `internal/setup`, `internal/search/api`, `internal/worker` jobs, `internal/engine`
   tools and model calls, `internal/gh` write sinks, `cmd/exemplar-learn` + `learning/`),
   each written to `.security/slices/<name>.tsv`. Check the slices cover every row exactly
   once. Put the **sink** rows (`gh-write-sink`, `exec-sink`) in their own slice on
   `model: "sonnet"`, traced backwards from the sink to the untrusted input that reaches
   it. Spawn one cartographer per slice, **all in a single message**, `model: "haiku"`
   otherwise; the delta is the slice file and slice name.
5. **Verify every map before trusting it.** A cartographer's "N of N traced" is a claim,
   not a measurement. For each slice, mechanically: the file exists; every slice row's
   `path:line` appears in it; every cited `path:line` resolves to a real line. Missing
   file → re-run once. Missing **handler** rows (`http-route`, `webhook-event`,
   `river-worker`, `model-tool`, `model-call`, `cli-command`, `py-job`) → re-trace them
   as a supplementary slice. Missing wiring rows are acceptable; count them for the header.
6. Concatenate `.security/map/*.md` into `.security/attack-surface.md`, headed by the
   "what this misses" list from the top of `entrypoints.sh` and a count of `UNTRACED`
   blocks. **More than a quarter untraced → say so in the final report**; a thin map means
   thin hunting.

## Phase 3 — Hunt (parallel, top model)

Spawn the hunters in a **single message**, one per class *Budget* leaves in play:
`injection`, `authz`, `logic`, `model-trust`, each `model: "opus"`. Delta is the class
name only.

Hunters over-report by design. Do not filter, comment on, or re-rank what they write.

## Phase 4 — Dedupe (one Sonnet pass)

Zero findings → skip to Phase 6. Otherwise spawn ONE agent, `model: "sonnet"`:
*"Read every file in `.security/findings/`. Two findings are duplicates only when their
`Root cause:` is the same location or the same missing check. Merge duplicates into the
better-evidenced file, keep both `## Attack` sections if they differ, note `Merged from:`,
delete the other. Do not judge validity. Do not re-rate severity; the merged file keeps the
**highest** severity any merged finding proposed. Reply with before/after counts."*

This runs before validation because validation is the expensive per-finding phase.

## Phase 5 — Validate (one agent per finding, parallel)

One validator per remaining finding file, **all in a single message**, `model: "opus"`
(`"sonnet"` for a proposed `Low`; see *Budget*). Delta is the finding's path. More than 12
findings → two batches.

A validator that fails or returns no verdict → re-run it once. Still nothing → that
finding is reported as `UNVALIDATED`, never dropped.

## Phase 6 — Aggregate (you)

Read only each finding's `## Validation` block and title. Then:

- Discard `FALSE POSITIVE`. Discard validated `Low` unless the scope was `all`.
- Rank: severity, then `LIKELY` before `UNLIKELY`.
- Write `.security/FINDINGS.md`:

```
# Security review — <scope> — <date>

Scope: <n> files · Entry points: <n> mapped, <n> untraced
Hunters raised <n> · after dedupe <n> · confirmed <n> · discarded <n>

## Critical
## High
## Medium
### <title> — <LIKELY|UNLIKELY>
Entry: <path:line> · Root cause: <path:line>
Attacker starts as: <...>
<Two-sentence attack summary.>
Fix direction: <...>
Detail: .security/findings/<file>

## Unvalidated
## Suggested known non-issues
<One line each, from validators that flagged `Suggest non-issue: yes`.>
```

Then report to the user: the counts line, each confirmed finding as one line with its
clickable `path:line`, and the suggested non-issues. A high discard rate is the validators
working, not a problem to apologise for.

**Do not fix anything and do not edit `docs/security/known-non-issues.md`.** A confirmed
finding is a named bug: the user decides whether it goes to `/go-service` (test first) or
to `docs/plans/backlog.md`. Non-issue suggestions are the user's call too; that list
suppresses future findings, so nothing gets onto it unreviewed.
