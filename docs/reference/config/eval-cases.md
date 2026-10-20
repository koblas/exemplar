# evals/<rule>/cases.yaml

The labeled eval cases of one rule, at `evals/<rule-id>/cases.yaml` in exemplar-config; the `rule` field must match the directory, and the rule must exist (or be an enabled bug class from [global.yaml](global.md#bug_lane), which has eval sets of its own). Other files under `evals/<rule-id>/` (patches and other case assets) are not validated. In the example CODEOWNERS they are owned by `@platform/exemplar`. `exemplar validate` checks them. Schema file: [eval-cases.schema.json](https://github.com/koblas/exemplar/blob/main/schema/eval-cases.schema.json).

Unknown fields are rejected. Code is referenced by `repo@sha`, never copied. The `eval_thresholds` in [global.yaml](global.md#eval_thresholds) say how many cases a rule needs and what precision and recall it must reach.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `rule` | string, non-empty | yes | The rule id the cases belong to. |
| `cases` | array of object, at least 1 | yes | The cases. See [`cases`](#cases). |

### `cases`

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string, non-empty | yes | The case name. |
| `ref` | string, pattern `^[^/@\s]+/[^/@\s]+@[0-9a-f]{7,40}$` | yes | The commit the case runs against, as `owner/repo@sha`. |
| `synthetic` | boolean | no | Whether the case applies a `patch` on top of `ref`. |
| `patch` | string, non-empty | see constraints | The patch applied for a synthetic case. |
| `paths` | array of non-empty string, up to 20, unique | no | Files the rule runs on. Defaults to the `expect` paths; negatives (cases that expect nothing) need it. |
| `expect` | array of object | yes | The expected findings; empty for a negative case. See [`cases.expect`](#casesexpect). |

Constraints:

- When `synthetic` is `true`, `patch` is required.

#### `cases.expect`

An expected finding overlapping the line range.

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | string, non-empty | yes | The file the finding is in. |
| `lines` | array of exactly 2 integers, each at least 1 | yes | The `[start, end]` lines the finding must overlap. |

## Example

The example config carries no `cases.yaml`; a minimal valid file:

```yaml
rule: propagate-context
cases:
  - name: http-get-in-handler
    ref: exemplar-sandbox/playground@0123abc
    expect:
      - path: handler.go
        lines: [10, 10]
  - name: main-setup-is-fine
    ref: exemplar-sandbox/playground@0123abc
    paths: [main.go]
    expect: []
```
