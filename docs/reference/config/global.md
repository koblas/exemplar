# global.yaml

The organization-wide defaults, at `global.yaml` in the root of the exemplar-config repository; each repo's file under [`repos/`](repo.md) can narrow them. In the example CODEOWNERS it is owned by `@platform/exemplar`. `exemplar validate` checks it against the schema, and the worker reloads it on a push to exemplar-config (`cache_ttl_seconds` is only a backstop). Schema file: [global.schema.json](https://github.com/koblas/exemplar/blob/main/schema/global.schema.json).

Unknown fields are rejected.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | boolean | yes | Master switch. `false` stops Exemplar from reviewing any repo. |
| `auto_approve` | boolean | yes | Lets Exemplar approve low-risk PRs on its own. Repos must also opt in (see [`auto_approve`](repo.md#auto_approve) in the repo file); `false` is the org-wide kill switch. |
| `cache_ttl_seconds` | integer, minimum 0 | no | How long workers cache this config. A push to the config repo refreshes it immediately; the TTL is only a backstop. |
| `budgets` | object | yes | Per-review model spend limits. See [`budgets`](#budgets). |
| `degrade` | object | yes | Degraded-operation settings. See [`degrade`](#degrade). |
| `max_exemplar_rules` | integer, minimum 1 | yes | Most rules run against one PR. When more apply, the lowest-precision ones are skipped and listed in the check run as "not run on this review". |
| `queues` | object | yes | Per-repo review concurrency. See [`queues`](#queues). |
| `concurrency` | object | yes | Agent fan-out within a review. See [`concurrency`](#concurrency). |
| `providers` | object | yes | The ordered failover list of model providers for each gateway stage. See [`providers`](#providers). |
| `merge_safety` | object | no | Weights of the deterministic merge-safety score. See [`merge_safety`](#merge_safety). |
| `verifier` | object | yes | The verification stage. See [`verifier`](#verifier). |
| `comment_budget` | object | yes | Caps on posted comments. See [`comment_budget`](#comment_budget). |
| `linter_wait_seconds` | integer, minimum 0 | no | How long a review waits for the repo's linters to report before posting. Default 120 when unset. |
| `bug_lane` | object | no | Bug hunting: agents look for common classes of bugs, independent of any rule. See [`bug_lane`](#bug_lane). |
| `baseline` | object | yes | The sampled, rule-free baseline reviewer. See [`baseline`](#baseline). |
| `eval_thresholds` | object | yes | Bars a rule or prompt change must clear in eval CI before it merges. See [`eval_thresholds`](#eval_thresholds). |
| `feedback` | object | no | Feedback weighting and demotion. See [`feedback`](#feedback). |
| `expiry` | object | no | Rule `review_by` handling. See [`expiry`](#expiry). |

### `budgets`

| Field | Type | Required | Description |
|---|---|---|---|
| `soft_ceiling_usd` | number, greater than 0 | yes | Model spend (USD) for one review at which Exemplar starts dropping optional work to stay under it. A runaway guard, not a target; cost is otherwise informational. |

### `degrade`

| Field | Type | Required | Description |
|---|---|---|---|
| `stage_timeout_seconds` | integer, minimum 1 | yes | A review step slower than this is treated as degraded, and optional work is dropped to keep the review moving. |

### `queues`

| Field | Type | Required | Description |
|---|---|---|---|
| `per_repo_concurrency` | integer, minimum 1 | yes | Reviews that may run at once for one repo. A fairness limit so one busy repo cannot starve the rest. |
| `large_repos` | array of string | no | Repos as `owner/name` (pattern `^[^/]+/[^/]+$`) that get a higher per-repo concurrency (32) instead of `per_repo_concurrency`: monorepos whose PR volume would otherwise queue behind the fairness limit. |

### `concurrency`

| Field | Type | Required | Description |
|---|---|---|---|
| `agents_per_review` | integer, 1 to 3 | yes | Model agents running in parallel within a single review, shared between rule checks and bug hunting. |

### `providers`

Each list is tried in order: when a provider times out, errors or is rate limited, the call moves to the next one. Providers must be registered in `internal/llm` (`claude`, `bedrock`). The gateway builds every provider listed and skips one whose credentials are missing, as long as each stage keeps a provider that works.

| Field | Type | Required | Description |
|---|---|---|---|
| `generator` | array of provider entry, at least 1 | yes | Writes review findings. Rules start on the `standard` model and move to `high` if their eval precision falls short; blocking and security rules and bug hunting start on `high`. |
| `verifier` | array of provider entry, at least 1 | yes | Independently checks each finding before it is posted. Meant to be a different model family from the generator; when both are the same family every finding is flagged `same_family`, which blocks auto-approve. |
| `light` | array of provider entry, at least 1 | yes | Cheap, quick calls such as scoring how much a finding matters. |
| `categorizer` | object | no | The auto-approve risk categorizer's pair of independent providers. See [`providers.categorizer`](#providerscategorizer). |

#### Provider entry

| Field | Type | Required | Description |
|---|---|---|---|
| `provider` | string, non-empty | yes | A registered provider name (`claude`, `bedrock`). |
| `model` | string, non-empty | see constraints | A single model id. |
| `standard` | string, non-empty | see constraints | The standard-tier model id. |
| `high` | string, non-empty | see constraints | The high-tier model id. |

Constraints:

- An entry names either `model`, or both `standard` and `high`. Generator entries name a model per tier; other stages name one model.
- Bedrock model ids take the `anthropic.` prefix; some regions need a cross-region inference profile id.

#### `providers.categorizer`

| Field | Type | Required | Description |
|---|---|---|---|
| `a` | provider entry | yes | The first of the two independent providers. |
| `b` | provider entry | yes | The second of the two independent providers. |

### `merge_safety`

| Field | Type | Required | Description |
|---|---|---|---|
| `weights` | object | yes | Penalties per term. A term left out keeps its default; `0` disables one. See [`merge_safety.weights`](#merge_safetyweights). |

#### `merge_safety.weights`

Every value is a number from 0 to 1. No other keys are allowed.

| Field | Type | Required | Description |
|---|---|---|---|
| `incident_180d` | number, 0 to 1 | no | Penalty for an incident linked to the touched paths in the last 180 days. |
| `revert_180d` | number, 0 to 1 | no | Penalty for a revert touching the paths in the last 180 days. |
| `churn_30d` | number, 0 to 1 | no | Penalty for high churn on the paths in the last 30 days. |
| `no_tests` | number, 0 to 1 | no | Penalty when the PR changes no tests. |
| `sensitive_path` | number, 0 to 1 | no | Penalty when the PR touches one of the repo's `sensitive_paths`. |
| `new_author` | number, 0 to 1 | no | Penalty when the author has little history in the repo. |
| `force_push` | number, 0 to 1 | no | Penalty when the PR was force-pushed. |

### `verifier`

| Field | Type | Required | Description |
|---|---|---|---|
| `importance_min` | number, 0 to 1 | yes | Findings the verifier scores below this importance are stored but not posted. |
| `max_candidates_per_call` | integer, minimum 1 | yes | Findings checked together in one verifier call. |

### `comment_budget`

Most comments Exemplar posts, per PR and per file, so reviews stay readable. Security and blocking findings are always posted; if they alone exceed the limit, they are grouped into one comment per file.

| Field | Type | Required | Description |
|---|---|---|---|
| `per_pr` | integer, minimum 1 | yes | Most comments per PR. |
| `per_file` | integer, minimum 1 | yes | Most comments per file. |

### `bug_lane`

Bug hunting runs on risky PRs (critical paths, at least `min_changed_lines` changed non-test lines, a low merge-safety score, or an incident or revert in the last 90 days) and always on PRs being considered for auto-approve.

| Field | Type | Required | Description |
|---|---|---|---|
| `min_changed_lines` | integer, minimum 0 | yes | Changed non-test lines that make a PR large enough to hunt for bugs. |
| `importance_min` | number, 0 to 1 | yes | Stricter than `verifier.importance_min`: bug findings must matter more. |
| `classes` | object | yes | One entry per bug class, keyed by class id. See [`bug_lane.classes`](#bug_laneclasses). |

#### `bug_lane.classes`

Keys match `^bug\.[a-z0-9-]+$` and must name an allowlisted bug class (the example lists `bug.nil-deref`, `bug.error-handling`, `bug.concurrency`, `bug.resource-leak`, `bug.off-by-one`, `bug.contract`). A class enabled without the service it needs (`bug.contract` needs `find_references` from exemplar-search) is skipped on every run, which blocks auto-approve.

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | boolean | yes | Whether the class runs. |
| `post_on_critical_paths` | boolean | no | Post this class's findings in a repo's `bug_lane.critical_paths`; elsewhere, and by default, it stays in shadow. |

### `baseline`

Share of PRs that also get a generic, rule-free review for comparison. Its findings are stored, never posted.

| Field | Type | Required | Description |
|---|---|---|---|
| `sample_rate` | number, 0 to 1 | yes | Share of PRs sampled. |
| `strata` | array of `repo`, `size_bucket` | no | Sampling strata. Size buckets are up to 50, up to 300, and over 300 changed lines. |

### `eval_thresholds`

| Field | Type | Required | Description |
|---|---|---|---|
| `min_cases` | integer, minimum 1 | yes | Labeled cases a rule needs before its scores count. |
| `precision` | number, 0 to 1 | yes | Precision bar. |
| `recall` | number, 0 to 1 | yes | Recall bar. |
| `verifier` | object | no | Verifier calibration bar. See [`eval_thresholds.verifier`](#eval_thresholdsverifier). |
| `categorizer` | object | no | Auto-approve categorizer calibration bar. See [`eval_thresholds.categorizer`](#eval_thresholdscategorizer). |

#### `eval_thresholds.verifier`

| Field | Type | Required | Description |
|---|---|---|---|
| `known_wrong_refuted_min` | number, 0 to 1 | yes | Least share of known-wrong findings (the [verifier labels](verifier-label.md)) the verifier must refute. |
| `known_true_refuted_max` | number, 0 to 1 | yes | Most share of known-true findings the verifier may refute. |

#### `eval_thresholds.categorizer`

The categorizer's calibration against the [categorizer labels](categorizer-label.md): the share of labeled risky PRs it would approve, and of safe ones.

| Field | Type | Required | Description |
|---|---|---|---|
| `known_risky_approved_max` | number, 0 to 1 | yes | Most share of labeled risky PRs it may approve. |
| `known_safe_approved_min` | number, 0 to 1 | yes | Least share of labeled safe PRs it must approve. |
| `min_review_confidence` | number, 0 to 1 | no | The bar a PR must reach to count as approved: 0.8 by default, and never lower. |

### `feedback`

| Field | Type | Required | Description |
|---|---|---|---|
| `efp_bar` | number, 0 to 1 | yes | The weighted effective false-positive rate an active rule must stay under; above it, with the other conditions, the rule is demoted. |
| `role_weights` | object of string to number (minimum 0) | yes | Weight of feedback per actor role: `author`, `reviewer`, `owner`, `observer` (the example: 1, 2, 4, 0). |
| `reaction_weight` | number, minimum 0 | yes | Weight of a 👎 reaction against acceptance. Reactions never count toward eFP. |
| `demotion_wilson_floor` | number, 0 to 1 | yes | The Wilson lower bound on unweighted precision over a rule's last 30 judged findings below which it may be demoted; the weighted eFP must also exceed `efp_bar`. |
| `demotion_requires` | `owner_adjudication_or_two_non_author_actors` | yes | The only value: a rule's negatives must include an owner adjudication or two non-author people before it is demoted. |
| `owner_response_days` | integer, minimum 1 | yes | How long a dismissal waits for an owner's adjudication before the demotion logic counts it without one. |

### `expiry`

| Field | Type | Required | Description |
|---|---|---|---|
| `default_months` | integer, minimum 1 | yes | The `review_by` horizon for a rule without one, in months from its activation. |
| `auto_demote_after_days` | integer, minimum 1 | yes | How long an expiry job's shadow PR waits for a person before it merges on its own. |

## Example

```yaml
# Organization-wide defaults. Each repo's file under repos/ can narrow these.
enabled: true
auto_approve: false
cache_ttl_seconds: 900
budgets:
  soft_ceiling_usd: 10
degrade:
  stage_timeout_seconds: 300
max_exemplar_rules: 40
queues:
  per_repo_concurrency: 4
concurrency:
  agents_per_review: 3
providers:
  # Each list is tried in order: when a provider times out, errors or is
  # rate limited, the call moves to the next one.
  generator:
    - { provider: claude,  standard: claude-sonnet-5-5,           high: claude-opus-5-5 }
    # - { provider: bedrock, standard: anthropic.claude-sonnet-5-5, high: anthropic.claude-opus-5-5 }
  verifier:
    - { provider: claude,  model: claude-opus-5-5 }
    # - { provider: bedrock, model: anthropic.claude-opus-5-5 }
  light:
    - { provider: claude,  model: claude-haiku-5-5 }
    # - { provider: bedrock, model: anthropic.claude-haiku-5-5 }
verifier:
  importance_min: 0.5
  max_candidates_per_call: 8
bug_lane:
  min_changed_lines: 80
  importance_min: 0.6
  classes:
    bug.nil-deref:      { enabled: true }
    bug.error-handling: { enabled: true }
    bug.concurrency:    { enabled: true }
    bug.resource-leak:  { enabled: false }
    bug.off-by-one:     { enabled: false }
    bug.contract:       { enabled: false }
comment_budget:
  per_pr: 7
  per_file: 3
baseline:
  sample_rate: 0.10
  strata: [repo, size_bucket]
eval_thresholds:
  min_cases: 10
  precision: 0.80
  recall: 0.50
  verifier: { known_wrong_refuted_min: 0.90, known_true_refuted_max: 0.10 }
  categorizer: { known_risky_approved_max: 0.05, known_safe_approved_min: 0.50 }
```
