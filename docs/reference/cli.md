# The CLI

`exemplar` is one binary for rule owners, PR authors, config CI and operators. Tagged releases attach archives for linux (amd64, arm64) and darwin (arm64, amd64) with `SHA256SUMS`. Building it needs cgo and a C compiler, since it embeds the tree-sitter grammars:

```bash
CGO_ENABLED=1 go build -o exemplar ./cmd/exemplar
```

`exemplar --version` prints the version set at release. Flags that default to an environment variable say so; the variables are on [Environment variables](environment-variables.md#the-cli-and-config-ci). Every subcommand exits 1 on error with the message on stderr.

## Working tree

### `exemplar login`

Signs in with a device flow (open the URL shown and enter the code) and saves the token and exemplar-api's URL for `exemplar review`, readable only by you. By default it signs in to GitHub with the review app's OAuth client. With `--oidc` it signs in to the OpenID Connect provider with its public CLI client, and refreshes the ID token when it expires (`offline_access`). The token identifies you to exemplar-api; it is never sent anywhere else.

| Flag | Default | Meaning |
|---|---|---|
| `--api` | `EXEMPLAR_API_URL` | exemplar-api's URL. |
| `--client-id` | `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID`, or `EXEMPLAR_OIDC_CLI_CLIENT_ID` with `--oidc` | The OAuth client id. |
| `--github` | `https://github.com/` | GitHub's web URL (GHES: `https://<host>/`). |
| `--oidc` | off | Sign in to the OpenID Connect provider instead of GitHub. |
| `--oidc-issuer` | `EXEMPLAR_OIDC_ISSUER_URL` | The OIDC issuer URL. |
| `--scopes` | `openid,profile,email,offline_access` | OIDC scopes, comma-separated. |

### `exemplar review`

Sends the diff of the working tree (tracked changes and untracked files that are not ignored) against its merge-base with `--base` to exemplar-api, which reviews it on that pushed commit with every lane and the verifier, as a PR review would, and prints what would have been posted. Nothing is posted anywhere. Exits 1 when there are findings (0 with `--exit-zero`). With `EXEMPLAR_TOKEN` and `EXEMPLAR_API_URL` set, no sign-in is needed.

| Flag | Default | Meaning |
|---|---|---|
| `--base` | origin's default branch | The branch to diff against. |
| `--repo` | from the origin remote | `owner/name`. |
| `--format` | `text` | `text` or `sarif`. |
| `--agent` | `EXEMPLAR_AGENT` | The coding agent running the review, if any (for example `claude_code`). |
| `--wait` | `15m` | How long to wait for the review. |
| `--poll` | `5s` | How often to check on it. |
| `--exit-zero` | off | Exit 0 even with findings. |

## Config

### `exemplar validate [exemplar-config dir]`

Validates global, repo, rule, eval and platform files against the schemas, and checks cross-file references ([exemplar-config](config/index.md#cross-file-checks)). Exits non-zero listing every problem. The directory defaults to the current one.

### `exemplar try <rule.yaml> <org/repo#N | PR URL>`

Loads exemplar-config from `--config` with the rule file added (replacing a rule with the same id), checks that your GitHub token can read the target repo and that the repo is eligible, then runs only that rule on the PR (`caller=cli`) and prints the findings. Nothing is posted on the PR. The token is used for the access check, the PR's metadata and the clone.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `exemplar-config` | The exemplar-config checkout. |
| `--work-dir` | a temp dir | Scratch directory for the PR checkout. |
| `--token-env` | `GITHUB_TOKEN` | The environment variable holding your GitHub token. |
| `--github-api-url` | `EXEMPLAR_GH_API_URL` | GitHub API base URL (GHES). |
| `--json` | off | Print the outcome as JSON. |
| `--gateway-url`, `--gateway-token`, `--scripted` | | [Engine flags](#engine-flags). |

### `exemplar eval`

Runs each selected rule over its `evals/<rule>/cases.yaml`: compiled rules on the engine, exemplar rules through the lane and verifier (`caller=eval`). Reports precision, recall and F1 for historical and synthetic cases and fails when any rule is below `global.yaml` `eval_thresholds`. With `--changed-since`, evaluates the rules a config PR changes, with eligibility taken from that base revision. Case files are read with the token in `EXEMPLAR_EVAL_TOKEN` (read-only).

With `--remote`, the evaluation runs in exemplar-worker instead: the CLI sends the config PR's merge-base with `--changed-since` and its HEAD (both pushed) to exemplar-api's `/v1/evaluate`, authenticated with `EXEMPLAR_TOKEN` (an Auth0 service token in CI, or `exemplar login`) at `EXEMPLAR_API_URL`, so CI holds no model or GitHub credentials.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `.` | The exemplar-config checkout. |
| `--changed-since` | | Evaluate the rules changed since this git revision (a config PR's base). |
| `--rules` | all | Rule ids to evaluate, comma-separated. |
| `--work-dir` | a temp dir | Scratch directory. |
| `--json` | | Write per-rule reports as JSON here. |
| `--markdown` | | Write the summary table here (for example `$GITHUB_STEP_SUMMARY`). |
| `--token-env` | `EXEMPLAR_EVAL_TOKEN` | Environment variable with a read-only token for case repos. |
| `--remote` | off | Evaluate in exemplar-worker through `/v1/evaluate`; needs `--changed-since`. |
| `--repo` | from the origin remote | exemplar-config's `owner/name`, with `--remote`. |
| `--wait` | `1h` | How long to wait for a `--remote` evaluation. |
| `--gateway-url`, `--gateway-token`, `--scripted` | | [Engine flags](#engine-flags). |

### `exemplar volume`

Runs each rule the config PR adds or activates alone over a sample of PRs merged in the last `--days` in the repos it applies to, and reports the share of PRs it would comment on and comments per PR after the comment budget. Informational: the check fails only if a run fails.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `.` | The exemplar-config checkout. |
| `--changed-since` | | Check the rules changed since this git revision. |
| `--rules` | | Rule ids to check. |
| `--json`, `--markdown` | | Write the reports or the summary here. |
| `--token-env` | `EXEMPLAR_EVAL_TOKEN` | Read-only token for the target repos. |
| `--github-api-url` | `EXEMPLAR_GH_API_URL` | GitHub API base URL (GHES). |
| `--work-dir` | a temp dir | Scratch directory. |
| `--days` | `30` | Window of merged PRs. |
| `--per-repo` | `10` | Merged PRs sampled per repo. |
| `--max-runs` | `40` | Rule runs across all repos (bounds model cost). |
| `--gateway-url`, `--gateway-token`, `--scripted` | | [Engine flags](#engine-flags). |

### `exemplar linter-overlap`

For each changed rule's positive history cases, compares the annotations the repo's configured linters (`repos/<org>/<repo>.yaml` `linters`) reported on that commit with the expected lines. Any overlap fails the check and names the linter rule. Synthetic cases are skipped: no CI ran on them. Flags: `--config`, `--changed-since`, `--rules`, `--json`, `--markdown`, `--token-env` (the token needs `checks: read`), `--github-api-url`, as for `volume`.

### `exemplar export-approval`

Compares every rule's `export:` block at `--changed-since` with the checkout. When any changed, the check passes only if a member of `--team`, other than the PR author, approved the PR's current head commit. Reviews are read with `GITHUB_TOKEN`; team membership needs a token with `members: read`.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `.` | The exemplar-config checkout at the PR head. |
| `--changed-since` | | The PR's base commit. |
| `--repo` | `GITHUB_REPOSITORY` | The config repo, `owner/name`. |
| `--pr` | | The config PR's number. |
| `--head-sha` | | The PR's head commit; approvals of other commits do not count. |
| `--author` | | The PR's author, whose own approval does not count. |
| `--team` | `@platform/devex` | The team that must approve export changes. |
| `--review-token-env` | `GITHUB_TOKEN` | Environment variable with a token that can read the PR's reviews. |
| `--team-token-env` | `EXEMPLAR_TEAMS_TOKEN` | Environment variable with a token that can read team membership. |
| `--github-api-url` | `EXEMPLAR_GH_API_URL` | GitHub API base URL (GHES). |
| `--markdown` | | Write the summary here. |
| `--json` | off | Print the result as JSON. |

### `exemplar calibrate`

**Verifier:** verifies each labelled finding alone (no generator) and fails unless the verifier refutes at least `eval_thresholds.verifier.known_wrong_refuted_min` of known-wrong findings and at most `known_true_refuted_max` of known-true ones. Labels: the `evals/_verifier` seed set in `--config`, plus `verifier_labels` with `--db`. **Categorizer:** runs Stage 2's two-model categorization on each `evals/_categorizer` PR and fails if more than `eval_thresholds.categorizer.known_risky_approved_max` of risky PRs, or fewer than `known_safe_approved_min` of safe ones, reach `min_review_confidence`. Skipped while `global.yaml` has no `providers.categorizer`. With `--only-if-changed-since`, each stage runs only when its providers, settings, thresholds or labels changed.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `.` | The exemplar-config checkout. |
| `--stage` | `all` | `verifier`, `categorizer` or `all`. |
| `--only-if-changed-since` | | Run only if the inputs changed since this revision (a config PR's base). |
| `--db` | `EXEMPLAR_DATABASE_URL` | Postgres URL for `verifier_labels` (optional). |
| `--db-limit` | `500` | Most `verifier_labels` rows to replay. |
| `--work-dir` | a temp dir | Scratch directory. |
| `--json`, `--markdown` | | Write the report or the summary here. |
| `--token-env` | `EXEMPLAR_EVAL_TOKEN` | Read-only token for recorded labels' repos. |
| `--gateway-url`, `--gateway-token`, `--scripted` | | [Engine flags](#engine-flags). |

### `exemplar adversarial`

Runs every case in `evals/_adversarial`; fails if any attack succeeds. Compromised mode replaces the model with a script that obeys the injection, testing the code-level defenses; it needs no model access. Live mode sends each case to the real model through the gateway.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `.` | The exemplar-config checkout. |
| `--mode` | `compromised` | `compromised` or `live`. |
| `--json` | off | Print results as JSON. |
| `--gateway-url`, `--gateway-token`, `--scripted` | | [Engine flags](#engine-flags). |

### `exemplar mutations`

Writes bug-class recall cases by mutating clean code. Applies deterministic mutation operators (drop an error check, flip `<` to `<=`, drop a `Close` or free, remove a lock/unlock pair or guard, drop a nil guard) to Go, C and C++ files of a checkout in the repo's `bug_lane.critical_paths` (or `--paths`), and merges the cases into `evals/<bug class>/cases.yaml` of an exemplar-config checkout, with a patch per case. Code stays referenced by `repo@sha`; review and commit the result.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | required | The exemplar-config checkout to write into. |
| `--checkout` | required | A checkout of the target repo at the commit to reference. |
| `--repo` | required | The target repo, `owner/name`. |
| `--sha` | the checkout's HEAD | The checkout's commit. |
| `--paths` | the repo's `bug_lane.critical_paths` | Globs of files to mutate. |
| `--per-file` | `1` | Mutations per operator per file. |
| `--max-per-class` | `20` | New cases per bug class. |
| `--dry-run` | off | Print what would be written; write nothing. |

## Operators and developers

### `exemplar replay`

Reruns the review pipeline over recorded PRs ([Recorded PR fixture](config/fixture.md)) with the engine (`caller=eval`). With `--db`, each fixture is a River job and results are stored; with `--post`, findings on real fixtures are posted to their sandbox PR; with `--baseline`, the baseline reviewer runs in shadow. Prints a JSON outcome per fixture.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | required | The exemplar-config checkout. |
| `--fixtures` | required | Directory of recorded-PR fixtures. |
| `--work-dir` | a temp dir | Keep checkouts here. |
| `--db` | `EXEMPLAR_DATABASE_URL` | Postgres URL; enables storage and River jobs. |
| `--out` | | Write the JSON outcomes to a file. |
| `--post` | off | Post findings on real fixtures to their sandbox PR (needs the app flags). |
| `--baseline` | off | Also run the baseline reviewer in shadow. |
| `--gateway-url`, `--gateway-token`, `--scripted` | | [Engine flags](#engine-flags). |

### `exemplar sandbox probe-approval`

Checks whether a native `APPROVE` from the review app counts toward required approvals. Submits an `APPROVE` review from the review app on a sandbox PR that has no approvals, reads GitHub's review decision, and dismisses the approval. Prints the result as JSON, including the `approval_mode` to set in the repo's config ([How to turn on auto-approve](../guides/operate/auto-approve.md)). Use only on sandbox repos.

| Flag | Default | Meaning |
|---|---|---|
| `--app-id` | `EXEMPLAR_GH_APP_ID` | The review app id. |
| `--private-key-file` | `EXEMPLAR_GH_PRIVATE_KEY_FILE` | The review app's private key (PEM). |
| `--installation-id` | `EXEMPLAR_GH_INSTALLATION_ID` | The installation id. |
| `--config-repo` | `EXEMPLAR_CONFIG_REPO` | `owner/exemplar-config`. |
| `--github-api-url` | `EXEMPLAR_GH_API_URL` | GitHub API base URL; empty for github.com. |
| `--repo` | required | The sandbox repo, `owner/name`. |
| `--pr` | required | A PR number with no approvals. |
| `--wait` | `20s` | How long to wait for GitHub to recompute the review decision. |

`exemplar sandbox` alone lists its subcommands.

## Engine flags

Subcommands that run the engine locally (`try`, `eval`, `volume`, `calibrate`, `adversarial`, `replay`) share:

| Flag | Default | Meaning |
|---|---|---|
| `--gateway-url` | `EXEMPLAR_GATEWAY_URL` | exemplar-gateway's base URL. |
| `--gateway-token` | `EXEMPLAR_GATEWAY_TOKEN` | The gateway's shared secret. |
| `--scripted` | off | Use a scripted model that opens every file and reports nothing (an offline dry run). |

They read `EXEMPLAR_AST_GREP` for the ast-grep binary and `EXEMPLAR_GH_WEB_URL` for clone URLs.

## Standard subcommands

`exemplar completion <shell>` generates a shell completion script; `exemplar help [command]` prints help.
