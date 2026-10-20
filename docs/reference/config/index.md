# exemplar-config

`exemplar-config` is the git repository that holds everything Exemplar enforces. The GitHub App is registered on its owner; the worker, gateway, search and learn pods clone it with git-sync and refresh on every push; `exemplar validate` checks a checkout against the schemas in [`schema/`](https://github.com/koblas/exemplar/tree/main/schema) and the cross-file references. [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config) is the starting copy.

## Layout

| Path | Schema | Owner in the example `CODEOWNERS` | What |
|---|---|---|---|
| `global.yaml` | [global.yaml](global.md) | `@platform/exemplar` | Organization-wide defaults and kill switches: providers, budgets, lanes, thresholds, feedback and expiry. |
| `repos/<owner>/<repo>.yaml` | [repos/\<owner\>/\<repo\>.yaml](repo.md) | `@platform/exemplar` | One file per reviewed repository; the path names the repo. Enabled, shadow, drafts, forks, sensitive paths, linters, exports, context, auto-approve. |
| `rules/<category>/<id>.yaml` | [rules/**/*.yaml](rule.md) | `@platform/exemplar`; `rules/security/` `@security/appsec` | One rule per file. |
| `evals/<rule>/cases.yaml` | [evals/\<rule\>/cases.yaml](eval-cases.md) | as the rule | The rule's labelled cases. |
| `evals/_adversarial/*.yaml` | [evals/_adversarial](adversarial-case.md) | `@security/appsec @platform/exemplar` | Prompt-injection cases, run on every config change. |
| `evals/_verifier/*.yaml` | [evals/_verifier](verifier-label.md) | `@platform/exemplar` | Labelled findings for verifier calibration. |
| `evals/_categorizer/*.yaml` | [evals/_categorizer](categorizer-label.md) | `@platform/exemplar` | Labelled PRs for auto-approve categorizer calibration. |
| `platform/eligibility.yaml` | [platform/eligibility.yaml](eligibility.md) | `@security/appsec` | Repositories never reviewed, mined, indexed or sent to a provider. |
| `platform/retention.yaml` | [platform/retention.yaml](retention.md) | `@security/appsec` | Retention periods in days. |
| `CODEOWNERS` | | | The human gate: every change needs an owner's approval. |
| `.github/workflows/` | | `@platform/exemplar @platform/devex` | Config CI: `eval.yml` (validate, eval, volume, linter-overlap, verifier-calibration), `nightly.yml` (nightly calibration), `export-approval.yml` (platform approval for `export:` changes). |

Recorded pull requests for the replay harness use a separate schema, [Recorded PR fixture](fixture.md), and live in this repository's `examples/fixtures/`; the three eval label kinds embed one.

## Cross-file checks

`exemplar validate` and the loader refuse a tree where:

- a file is in the wrong directory for its schema;
- two rules share an `id`;
- a repo file's `disabled_global_rules`, or an eval file's `rule`, names a rule that does not exist;
- a rule with `export.agents: true` has no `export.summary`;
- a rule's `eval_cases` directory has no `cases.yaml`;
- a `compiled` rule has no `matcher`, or another kind has one;
- a repo's `auto_approve` lowers a bar `global.yaml` or the defaults set (`min_review_confidence`, `min_merge_safety_score`) or raises a limit (`max_files`, `max_changed_lines`).

## How changes take effect

A merge to the default branch is picked up by git-sync within seconds; `cache_ttl_seconds` in `global.yaml` is only a backstop. A review runs against the config version current when it started, and is idempotent on its PR head and that version.
