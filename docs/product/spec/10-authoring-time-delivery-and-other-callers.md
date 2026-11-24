## 10. Authoring-time delivery and other callers

### 10.1 Local CLI

- Single static Go binary (GoReleaser: Homebrew tap, internal package mirror, GitHub release for CI use).
  - **As built:** the CLI embeds the tree-sitter grammars (cgo), so it's built natively per platform rather than cross-compiled by GoReleaser.
  - **Release:** `.github/workflows/release.yml` runs on a `v*` tag. It builds linux amd64/arm64 and darwin arm64/amd64 with `-X main.version`, and attaches the archives and `SHA256SUMS` to the tag's GitHub release.
  - **Not built yet:** the Homebrew tap and the internal mirror.
- `exemplar review` — diff of the working tree against the merge-base, sent to `exemplar-api` (all lanes, same verifier), printed in the terminal or as SARIF.
- `exemplar try <rule.yaml> <pr-url>`, `exemplar validate`, `exemplar eval`, `exemplar transcript <review-id>`.
- No model keys on laptops: SSO device flow to the gateway; per-user daily budget.
- **As implemented** (`exemplar login`, `exemplar review`, exemplar-api `/v1/evaluate`, migration 00023):
  - **Sign-in.** `exemplar login` uses GitHub's device flow with the review app's OAuth client, so the app must have device flow enabled. It saves the user token and the API URL, readable only by the user. `EXEMPLAR_TOKEN` and `EXEMPLAR_API_URL` override them in CI. Org SSO stays at the ingress, as for the feedback links, so no OIDC library is used.
  - **What is sent.** `exemplar review` diffs the working tree against its merge-base with `--base` (default origin's default branch). The diff covers tracked changes and untracked, non-ignored files, up to 1 MiB. It is posted with the repo and base SHA, plus an optional `--agent`.
  - **Checks in exemplar-api:**
    - the token is a user's;
    - that user can read the repo;
    - a per-caller rate limit, 6 new evaluations a minute by default (polls get ten times that);
    - a daily budget per caller, by default $5 and 50 runs per UTC day, set by flags until it moves to config.
    - It then stores the request and enqueues an `evaluate` job in one transaction.
  - **The worker:**
    - It refuses ineligible repos before fetching anything.
    - It finds the installation from the repo's webhook deliveries and checks out the base, which must be pushed.
    - It applies the diff with `git apply --index`, which refuses paths outside the tree, and commits it.
    - It runs the full review with `caller=cli`.
  - **The answer.**
    - It is what would have been posted: findings in the posted state, with validated suggestions, plus coverage and incompleteness.
    - Nothing is posted or stored as PR findings.
    - The diff is cleared when the job ends.
    - Only the requester can read the result.
    - Results follow the findings retention period. A diff whose job never ran is cleared after a day.
  - **Output.** Text, or SARIF with `--format sarif`. The command exits 1 when there are findings, unless `--exit-zero`.
  - **Service callers** (IN-5): the identity provider is Auth0.
    - With `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` set, a bearer JWT is verified as an Auth0 access token for that API (`internal/auth0`, standard library only).
    - The checks: RS256 against the tenant's JWKS, refetched at most once a minute for an unknown key; issuer `https://<domain>/`; audience; and `exp`/`nbf` with a minute of leeway.
    - The repos a service may evaluate come from `evaluate:<owner>/<repo>`, `evaluate:<owner>/*` or `evaluate:*`, in its `permissions` (Auth0 RBAC) or `scope` (machine-to-machine grants).
    - It is recorded as `caller=agent` under `svc:<subject>`, with the same rate limit and budget as a person.
    - GitHub user tokens are not JWTs and still go to GitHub.
  - **Mode eval** (config CI, §8.9) is the same endpoint with `mode: eval`.
  - **Not built:**
    - budgets in config;
    - the shift-left board's per-user and per-agent CLI panel.

### 10.2 Rule exports to coding-agent instruction files

A River job in `exemplar-learn export` runs **weekly** and on every rule **promotion or kill** (PRD FR-S6). It renders active rules with `export.agents: true` into each opted-in repo, then opens or updates **one PR per repo** via the `exemplar-exports` app.

| Target | Files written | Scoping |
|---|---|---|
| Generic agents | `AGENTS.md` at repo root and nested per directory where rules are path-specific | Nearest-file semantics |
| Claude Code | `CLAUDE.md` containing the same generated block | Nested files |
| GitHub Copilot | `.github/copilot-instructions.md` (global rules only) and `.github/instructions/<area>.instructions.md` with `applyTo:` globs | Path-specific files keep each under the 4,000-character read limit for code review |
| Cursor | `.cursor/rules/exemplar-<area>.mdc` with `globs:` frontmatter | Per-area files |

- Content is the rule's `export.summary` (one line) plus the rule id, grouped by directory; never the full detection prompt.
- Generated content sits between `<!-- BEGIN exemplar … -->` / `<!-- END exemplar -->` markers; anything outside the markers is the team's and is never touched. If a team edits inside the markers, the next export PR shows the diff and the job stops updating that file until resolved.
- **Merging:** an export PR whose diff touches only text between the markers is set to **GitHub native auto-merge**, which still requires the repo's branch protection to be satisfied (status checks and, where configured, a review). Repos that want unattended merges add a ruleset exception scoped to the generated file paths only, never a repo-wide bypass actor: a bypass actor can push anything to the default branch, and the exports app must not be able to. Any diff touching hand-written text requires the repo's owners. Teams can opt out per file type.
- **Who can change what agents are told.** Export content is rendered only from `export.summary` in merged rules, and every config PR that adds or edits an `export:` block needs a `@platform/devex` approval in addition to the rule owner (§8.9). Summaries are also linted: ≤ 200 characters, one sentence, no URLs, no imperative instructions beyond the rule itself (a denylist of instruction verbs such as "ignore", "always run", "fetch"). Without this, any rule-owning team could ship instructions to every coding agent in the org through auto-merged exports.
- **Drift:** `exports.config_sha` records the config each repo's exports reflect; the shift-left board shows it against the current config SHA, and repos more than two config versions behind appear in the platform team's digest.
- Exported files are on the auto-approve path denylist (§6.13).
- Size budget per file (default 3,000 characters for Copilot files, 8,000 elsewhere); rules are ordered by severity and acceptance, and the overflow is dropped with a note.

**As implemented** (`exemplar-learn export`, `internal/exports`):
- **Schedule.** A daily CronJob stands in for "weekly and on every promotion or kill". It opens a PR only when a repo's rendering changed, so a promotion or kill reaches repos within a day, and a quiet week writes nothing.
- **Scope.**
  - It runs for repos with `exports.targets` that pass the eligibility check; denylisted or disabled repos are skipped.
  - Only global rules are exported. A repo's own `.exemplar/rules` are not, because the job reads no target-repo rules.
- **Areas.**
  - A rule's area is the literal directory its include patterns share: `src/net/**` gives `src/net`.
  - Rules with no shared directory go to the root files.
  - Cursor root rules go to `.cursor/rules/exemplar.mdc` with `alwaysApply: true`.
- **Order.** Rules are ordered by severity, then id. Acceptance is not used yet.
- **Branch and PR.**
  - Each repo has one branch, `exemplar/exports`, rebuilt from the default branch on every change, and one PR.
  - An open PR that already carries the current rendering is left alone.
  - When `exports.auto_merge_generated` is set, native auto-merge (squash) is enabled. If GitHub refuses, the PR stays open for the owners.
  - The job writes only between the markers, or files it creates whole, so every export diff is generated text.
- **Hand edits.**
  - The `exports` table records each file's proposed block hash, the block it replaced, `config_sha`, the PR, and a state: proposed, current or edited.
  - A block on the default branch that matches neither hash was edited by hand. It is marked `edited` and left alone.
  - Such a file is listed in the next export PR's body, if one opens, and in the job's output.
- **Credentials.**
  - The job writes with the separate exports app (`EXEMPLAR_EXPORTS_APP_ID`, `EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE`).
  - Each token is scoped to contents and pull requests write on exactly one repo. Minting one for exemplar-config is refused.
- **Drift.**
  - `export_runs` records each config SHA the job first rendered. A file is 0 versions behind while it matches; otherwise it counts the versions exported since it last matched, or, if never merged, since it was first proposed (that version included).
  - Config commits between two daily runs count as one version.
  - The `export_drift` view gives each repo's furthest-behind file. It feeds the shift-left board (`dashboards/shift-left.json`).
  - Repos more than two versions behind go into a weekly "Export drift" issue for `exemplar-learn digest --platform-team` (default `@platform/devex`).
- **Not built:** a per-file opt-out beyond choosing targets.

### 10.3 Pre-PR checks for coding agents (initial scope)

In the initial specification, coding agents get Exemplar's rules through the exported instruction files (§10.2) and can run a pre-PR check through the CLI (`exemplar review`, §10.1), which most agents can invoke as a shell command. A dedicated MCP server is **deferred** to Appendix C; it adds no engine capability, only a protocol surface, and it brings its own auth, budget and client-identity work that the initial phases do not need. PRD FR-S3 and the MCP parts of FR-S7 are therefore satisfied in a later draft, not this one; the `/v1/evaluate` access-control requirements of FR-S7 still apply to the CLI and service callers now.

### 10.4 Upstream effectiveness (PRD FR-S5)

For each exported rule, compare violation rates (findings per in-scope PR) between PRs whose head commits carry agent co-author trailers or PR labels indicating an agent that received the export, and PRs that don't, plus the rate before vs. after the export landed in that repo. Rules with no measurable drop get their `export.summary` flagged for rewrite in the digest.

**As implemented** (`internal/upstream`, migration 00021):
- **Agent.** Each production review records the PR's coding agent: a bot author, a `Co-authored-by` trailer, or a label (`copilot`, `agent:cursor`). Only the agent's name is stored, never the commit messages or labels.
  - The agent's export targets are fixed in code:
    - Claude Code reads CLAUDE.md;
    - Copilot and Cursor read their own files and AGENTS.md;
    - Codex, Devin, Gemini and agents known only by a label read AGENTS.md.
  - A PR **received** a rule's export if its agent reads a target that had landed in that repo by its first review. Landed means the file first matched its rendering (`exports.landed_at`).
- **Counting.**
  - An in-scope PR is one with a production rule run that has scoped files.
  - Violations are its distinct findings, excluding those held back as refuted, uncertain, unimportant or badly anchored. Findings withheld only as duplicates count.
  - The `upstream_pr_rule` view gives one row per exported rule and PR. The shift-left board shows it.
- **Rewrite.** Over 90 days, a summary is flagged for rewrite when every measurable comparison shows less than a 20% drop. The comparisons are agent-received against the rest, and after landing against before in exporting repos. A comparison is measurable with at least 20 PRs on each side and some violations to drop. The flag goes into the owner's digest under "Exported summaries to rewrite".
- **Not built:** configurable agent markers and per-agent targets (schema fields); confidence intervals on the rates.

### 10.5 Other callers

Other agents call `exemplar-api` with a service JWT (`caller=agent`). The baseline reviewer is internal (`caller=baseline`).

---
