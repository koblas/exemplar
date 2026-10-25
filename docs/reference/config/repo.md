# repos/<owner>/<repo>.yaml

One file per repo Exemplar reviews, at `repos/<owner>/<repo>.yaml` in exemplar-config; the file's path names the repo, and a file at any other depth under `repos/` is rejected as misplaced. It narrows the defaults in [global.yaml](global.md). In the example CODEOWNERS these files are owned by `@platform/exemplar`. `exemplar validate` checks them. Schema file: [repo.schema.json](https://github.com/koblas/exemplar/blob/main/schema/repo.schema.json).

Unknown fields are rejected. Rules and repo config are read from the default branch of exemplar-config, never from a PR head.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | boolean | yes | Kill switch for this repo. A repo is reviewed only when it is opted in, not denylisted (see [eligibility](eligibility.md)), and Exemplar is enabled globally. |
| `review_drafts` | boolean | no | Whether draft PRs are reviewed. Off by default. |
| `shadow` | boolean | no | Review in shadow: every lane runs and results are stored, but nothing is written to the PR (no comments, check runs or approval). For pilots and first installs. |
| `forks` | object | no | Fork PR review. See [`forks`](#forks). |
| `sensitive_paths` | array of glob (non-empty strings) | no | Sensitive path globs. Each must be a valid doublestar pattern. |
| `disabled_global_rules` | array of string | no | Ids of global rules that do not run on this repo. Each must name an existing rule. |
| `linters` | array of string | no | The repo's linter check-run names. Their annotations are used for dedupe: a finding overlapping one (through a rule's `linter_map`) is dropped as a linter duplicate, the review waits `linter_wait_seconds` for them, and config CI's `linter-overlap` check reads them. |
| `bug_lane` | object | no | Narrows the bug lane for this repo. See [`bug_lane`](#bug_lane). |
| `exports` | object | no | Rule exports to coding-agent instruction files. See [`exports`](#exports). |
| `context` | object | no | Repository context for exemplar-search. See [`context`](#context). |
| `auto_approve` | object | no | Per-repo auto-approve settings. See [`auto_approve`](#auto_approve). |
| `rounds` | object | no | `max`: a lower review-round limit for this repo than `global.yaml`'s [`rounds.max`](global.md#rounds); a higher one is rejected. |
| `approval_mode` | `native_approve`, `merge_policy_check` | no | How the repo's branch rules count Exemplar's approval. `native_approve` (default): Exemplar's `APPROVE` counts toward required approvals; the rules must dismiss stale approvals on push. `merge_policy_check`: the ruleset requires the `exemplar/merge-policy` check instead, which passes when a person with write access approved. Set per ruleset from `exemplar sandbox probe-approval`. |

### `forks`

| Field | Type | Required | Description |
|---|---|---|---|
| `review` | boolean | yes | Whether PRs from forks are reviewed. |

### `bug_lane`

| Field | Type | Required | Description |
|---|---|---|---|
| `critical_paths` | array of glob (non-empty strings) | no | Paths where bug classes with `post_on_critical_paths` (see [global.yaml](global.md#bug_laneclasses)) post their findings. |

### `exports`

| Field | Type | Required | Description |
|---|---|---|---|
| `targets` | array of `agents_md`, `claude_md`, `copilot`, `cursor` | no | Instruction files to generate. |
| `auto_merge_generated` | boolean | no | Whether an export PR whose only changes are between the `exemplar` markers merges on its own. |

### `context`

| Field | Type | Required | Description |
|---|---|---|---|
| `extra_repos` | array of string, each `owner/name` (pattern `^[^/]+/[^/]+$`) | no | Additional repos mirrored and indexed in exemplar-search for this repo's reviews; same visibility tier only. |
| `include_roots` | array of string | no | Include roots (for example `src/`) that give C and C++ files their package identity when import patterns are matched. |
| `dependents_depth` | integer, 1 to 2 | no | How many levels of dependents the `dependents` tool follows. |

### `auto_approve`

Approval-check bars can be raised per repo, never lowered; the deterministic gates' size limits can be lowered, never raised. The org-wide `auto_approve` in [global.yaml](global.md) must also be on.

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | boolean | yes | Opts the repo in to auto-approve. |
| `paths` | array of glob (non-empty strings) | no | Paths eligible for auto-approve. Each must be a valid doublestar pattern. |
| `allowed_authors` | array of string | no | Humans (logins or `@org/team`) whose PRs may be auto-approved. Bots go in `bot_authors`. |
| `approvable_owners` | array of string | no | CODEOWNERS owners whose paths may be auto-approved: every touched path's owners must all be listed. Empty approves nothing. Each item is a `@user`, `@org/team`, or an email address (pattern `^(@[A-Za-z0-9][A-Za-z0-9-]*(/[A-Za-z0-9._-]+)?|[^@\s]+@[^@\s]+)$`). |
| `bot_authors` | array of object | no | Bot identities allowed to be auto-approved within paths. See [`auto_approve.bot_authors`](#auto_approvebot_authors). |
| `extra_denylist` | array of glob (non-empty strings) | no | Paths never auto-approved. Each must be a valid doublestar pattern. |
| `title_pattern` | string | no | A regular expression the PR title is matched against; it must compile. Informational only: recorded in the decision, never a gate. |
| `max_files` | integer, minimum 1 | no | File limit: 10 by default; a repo may lower it but not raise it. |
| `max_changed_lines` | integer, minimum 1 | no | Changed-line limit: 1,000 by default; a repo may lower it but not raise it. |
| `min_review_confidence` | number, 0 to 1 | no | The review-confidence bar: 0.8 by default; a repo may raise it but not lower it. |
| `min_merge_safety_score` | number, 0 to 1 | no | The merge-safety-score bar: 0.7 by default; a repo may raise it but not lower it. |

#### `auto_approve.bot_authors`

| Field | Type | Required | Description |
|---|---|---|---|
| `identity` | string, non-empty | yes | The bot's identity. |
| `paths` | array of glob (non-empty strings), at least 1 | yes | Paths within which the bot's PRs may be auto-approved. Each must be a valid doublestar pattern. |

## Example

```yaml
# One repo Exemplar reviews. Copy to repos/<owner>/<repo>.yaml for each
# repo; the file's path names the repo.
enabled: true                 # kill switch for this repo
shadow: false                 # set true for a first install: review and store, post nothing
review_drafts: true
forks:
  review: false
sensitive_paths: ["internal/auth/**"]
approval_mode: native_approve # set per ruleset
```
