# How to set up config CI

The workflows in `exemplar-config` (`.github/workflows/eval.yml`, `nightly.yml`, `export-approval.yml`, copied from [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config)) validate every config PR. A fresh copy starts green: `validate` always runs (schemas, cross-file references, and the adversarial set in compromised mode, which needs no model access), and every other job is skipped until its repository variables exist.

| Job | Turn on with | Needs |
|---|---|---|
| `eval` (changed rules through `/v1/evaluate`) | variables `EXEMPLAR_API_URL`, `EXEMPLAR_AUTH0_DOMAIN`, `EXEMPLAR_AUTH0_AUDIENCE` | secrets `EXEMPLAR_AUTH0_CLIENT_ID` and `EXEMPLAR_AUTH0_CLIENT_SECRET` for an Auth0 client with `evaluate:<org>/exemplar-config` plus `evaluate:<owner>/<repo>` (or `evaluate:<owner>/*`) for every repo the eval cases reference: a case on a repo the client cannot evaluate is reported unavailable; `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` in `exemplar-settings` |
| `volume`, `verifier-calibration` | variable `EXEMPLAR_GATEWAY_URL` | a route from GitHub Actions to exemplar-gateway, which no shipped build exposes, plus secrets `EXEMPLAR_GATEWAY_TOKEN` and `EXEMPLAR_EVAL_TOKEN` |
| `linter-overlap` | variable `EXEMPLAR_LINTER_OVERLAP=true` | secret `EXEMPLAR_EVAL_TOKEN` with `checks: read` on the case repos |
| `export-approval` | always on | secret `EXEMPLAR_TEAMS_TOKEN` with `members: read`, to read the platform team's membership (`GITHUB_TOKEN` cannot) |
| `nightly` (verifier calibration over the seed labels and, with `EXEMPLAR_CALIBRATION_DATABASE_URL`, the labels from author feedback) | a schedule | the gateway secrets as for `volume`, and optionally a read-only database URL |

## 1. Turn on `eval`

1. In exemplar-api's settings, set `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` so service callers are accepted ([How to set up sign-in](../install/sign-in.md#service-callers)).
2. In Auth0, create a machine-to-machine client for that audience with the `evaluate:` permissions above.
3. In the config repository's settings, add the three variables and the two secrets.

The starter rules in `rules/` ship without eval cases, because a case must point at a commit in one of your repositories. Once `eval` is on, a PR that edits a starter rule fails until that rule has `eval_thresholds.min_cases` cases ([How to write eval cases](eval-cases.md)); a PR that leaves them alone is not evaluated.

The job sends the PR's merge-base and head (both pushed) to `/v1/evaluate` and the evaluation runs in exemplar-worker, so CI holds no model or GitHub credentials. It writes the summary to the job summary and uploads the JSON report.

## 2. Turn on the gateway jobs

`volume` and `verifier-calibration` call exemplar-gateway directly. No shipped build exposes it outside the cluster: route to it (a private runner in the cluster, or an exposed Service behind your own controls) before setting `EXEMPLAR_GATEWAY_URL`. `EXEMPLAR_GATEWAY_TOKEN` is the derived gateway token, or its explicit override; `EXEMPLAR_EVAL_TOKEN` is a read-only GitHub token for the case repositories.

`verifier-calibration` runs when a config PR changes what the verifier depends on (`providers.verifier`, `verifier`, `eval_thresholds.verifier`, or `evals/_verifier`), and likewise the auto-approve categorizer (`providers.categorizer`, `eval_thresholds.categorizer`, `evals/_categorizer`; skipped while there is no `providers.categorizer`). Otherwise it reports "not run".

## 3. Turn on `linter-overlap`

Set the variable and give `EXEMPLAR_EVAL_TOKEN` `checks: read` on the case repositories; the job reads the annotations their configured linters reported on the case commits.

## 4. Protect the branch

Require the `validate` and `eval` checks and CODEOWNERS review in a ruleset on `exemplar-config`, so the human gate holds. The platform team owns `.github/` in `CODEOWNERS`, since a PR that edits the workflows could switch the checks off.

## Pin the CLI

Each job builds the `exemplar` CLI from `koblas/exemplar` at `ref: main`. Pin that to a release tag or commit once you rely on it.
