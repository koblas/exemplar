# How to test a rule before merging

Three ways to see what a rule would post without posting anything. All three run the same engine as a PR review.

## On a real pull request, from the CLI

`exemplar try` loads `exemplar-config` from `--config` with your rule file added (replacing a rule with the same id), checks that your token can read the target repo and that the repo is eligible, runs only that rule on the PR, and prints the findings:

```bash
GITHUB_TOKEN=<token> exemplar try rules/reliability/close-response-body.yaml acme/app#123 --config path/to/exemplar-config
```

The PR can also be given as a URL. The token (from `$GITHUB_TOKEN` by default, or `--token-env`) is used for the access check, the PR's metadata and the clone; `--json` prints the outcome as JSON. For exemplar rules the CLI needs the gateway (`--gateway-url`, `--gateway-token`, or their variables); `--scripted` dry-runs with a model that opens every file and reports nothing.

## On a real pull request, in the browser

Open `/ui/playground`. Paste a draft rule, or start from an existing one with **try in playground** on its page, pick a PR you can read, and run it. The run executes in exemplar-worker and nothing is posted on the PR. The result page shows each finding as the comment would read, with the verifier's verdict.

A finished run can be saved as a draft config PR: a new rule file becomes a candidate (anyone may do this); an edited existing global rule needs its owner or the platform team. Draft PRs are never merged from the playground.

## Against the eval cases

```bash
exemplar eval --config path/to/exemplar-config --rules close-response-body
```

reports precision, recall and F1 for historical and synthetic cases and fails below `eval_thresholds` ([How to write eval cases](eval-cases.md)).

## In shadow

Merge the rule with `status: shadow`. It runs on every PR in scope; its page in the web UI shows what it would have posted and how the verifier judged each finding, with nothing on the PR. Promote it to `active` in a second PR when a week of shadow findings looks right.

## Before activating

For a rule going `active`, config CI's `volume` job runs it alone over a sample of recently merged PRs and reports the share of PRs it would comment on and comments per PR after the budget; `linter-overlap` fails if a configured linter already flags its positive cases. Both need [config CI](config-ci.md) set up. Locally:

```bash
exemplar volume --config . --rules close-response-body --days 30 --per-repo 10
```
