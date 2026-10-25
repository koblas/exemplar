# rules/**/*.yaml

One code-review rule per file. Global rules live at `rules/<category>/<id>.yaml` in exemplar-config (the directory is the lower-cased `category`, the file name is the `id`; any other placement is rejected as misplaced); a target repo may also carry its own at `.exemplar/rules/<id>.yaml`, and a repo rule that reuses a global rule id is rejected. In the example CODEOWNERS `rules/security/` is owned by `@security/appsec` and the rest by `@platform/exemplar`. `exemplar validate` checks them. Schema file: [rule.schema.json](https://github.com/koblas/exemplar/blob/main/schema/rule.schema.json).

Unknown fields are rejected. Each finding records the git blob id of the rule file that produced it.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string, pattern `^[a-z0-9][a-z0-9.-]{1,79}$` | yes | The rule id; must match the file name (`<id>.yaml`) and be unique across the config. |
| `kind` | `exemplar`, `compiled`, `bug_class` | yes | The rule kind. |
| `category` | string, 1 to 40 characters | yes | The category; its lower-cased form is the rule's directory under `rules/`. |
| `severity` | `blocking`, `important`, `suggestion` | yes | The severity. Blocking and security rules start on the `high` generator tier. |
| `owner` | string, pattern `^@[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)?$` | yes | The owning user or team. |
| `status` | `candidate`, `shadow`, `active`, `killed` | yes | Only active rules post; shadow rules run and are stored; killed and candidate rules never run. |
| `review_by` | string, pattern `^\d{4}-\d{2}-\d{2}$` | no | The date the rule is due for review (see `expiry` in [global.yaml](global.md#expiry)). |
| `file_patterns` | array of non-empty string, at least 1 | yes | Globs the rule runs on; a `!` prefix excludes (for example `!**/*_test.go`). Patterns must compile. |
| `description` | string, 1 to 4000 characters | yes | What the rule is about. |
| `preferred_approach` | string, up to 4000 characters | no | The approach the rule prefers. |
| `what_to_flag` | string, up to 4000 characters | no | What the rule flags, and what it does not. |
| `matcher` | object | see constraints | The deterministic matcher of a compiled rule. See [`matcher`](#matcher). |
| `fix_policy` | `none`, `describe`, `suggest` | no | Default `none` when unset. |
| `citation` | object | no | Provenance attached to every finding of the rule. See [`citation`](#citation). |
| `export` | object | no | What the rule contributes to coding-agent instruction files. See [`export`](#export). |
| `eval_cases` | string | no | The rule's eval directory, for example `evals/<id>/`. When set, `evals/<id>/cases.yaml` must exist (see [eval cases](eval-cases.md)). |
| `model_tier` | `standard`, `high` | no | The generator tier the rule runs on. Default `standard` when unset. |
| `verify` | `single`, `quorum` | no | `quorum` sends the rule's findings to the verifier quorum (`verifier.quorum` in [`global.yaml`](global.md#verifier)) instead of one verifier, at `n` times the verifier cost. Default `single`. Bug-lane findings always use the quorum. |
| `context` | `full`, `text-only` | no | What the rule's agent may read: `full` (the default) gives it the repository tools through exemplar-search; `text-only` limits it to the diff, which sends less code to the provider. |
| `eval` | `historical`, `synthetic` | no | Accepted by the schema; not read by the engine or the eval runner yet. |
| `linter_map` | object of non-empty string to array of non-empty string (at least 1) | no | Linter check run name (repo config `linters`) to the linter rule ids that report the same issue as this rule; a finding overlapping one of their annotations is a `linter_duplicate`. |

Constraints:

- `matcher` is required when `kind` is `compiled`, and not allowed otherwise.

### `matcher`

| Field | Type | Required | Description |
|---|---|---|---|
| `engine` | `ast-grep`, `semgrep` | yes | The matcher engine. |
| `rule` | string, non-empty | yes | The engine's rule text. |

### `citation`

| Field | Type | Required | Description |
|---|---|---|---|
| `doc` | string, URI | no | A document the rule comes from. |
| `comments` | array of string, each a URI | no | Review comments the rule comes from. |
| `prs` | array of string, each a URI | no | Pull requests the rule comes from. |

### `export`

| Field | Type | Required | Description |
|---|---|---|---|
| `agents` | boolean | yes | Whether the rule is exported to coding-agent instruction files. When `true`, `summary` must be non-empty. |
| `summary` | string, up to 300 characters | no | The exported line; it is linted when present. |

## Example

```yaml
id: propagate-context
kind: exemplar
category: Reliability
severity: important
owner: "@platform/exemplar"
status: active
review_by: 2027-10-01
file_patterns: ["**/*.go", "!**/*_test.go"]
description: |
  Code on a request path must pass the caller's context.Context to every
  downstream call that does I/O (HTTP, database, RPC, queue), so cancellation
  and deadlines reach them.
preferred_approach: |
  Accept ctx as the first parameter and pass it through. Use
  http.NewRequestWithContext, db.QueryContext, and similar context-aware APIs.
what_to_flag: |
  - context.Background() or context.TODO() created inside a function that
    already has a ctx parameter or is called from a request handler.
  - I/O calls that have a context-aware variant but use the plain one
    (http.Get, db.Query, db.Exec) on a request path.
  Do not flag main(), tests, or one-off CLI setup code.
fix_policy: suggest
citation:
  doc: https://go.dev/blog/context
export:
  agents: true
  summary: "Pass the caller's context.Context to every I/O call on a request path."
```

A compiled rule carries a `matcher`:

```yaml
id: no-raw-sql-concat
kind: compiled
category: Security
severity: blocking
owner: "@security/appsec"
status: active
review_by: 2027-10-01
file_patterns: ["**/*.go", "!**/*_test.go"]
matcher:
  engine: ast-grep
  rule: |
    id: no-raw-sql-concat
    language: go
    utils:
      built-query:
        any:
          - kind: binary_expression
            has: { field: operator, regex: ^\+$ }
          - pattern: { context: "func _() { fmt.Sprintf($$$) }", selector: call_expression }
    rule:
      kind: call_expression
      any:
        # The query is the first argument of Query, QueryRow, Exec, Prepare...
        - all:
            - has: { field: function, kind: selector_expression, has: { field: field, regex: ^(Query|QueryRow|Exec|Prepare)$ } }
            - has: { field: arguments, has: { nthChild: 1, matches: built-query } }
        # ...and the second of their Context variants.
        - all:
            - has: { field: function, kind: selector_expression, has: { field: field, regex: ^(Query|QueryRow|Exec|Prepare)Context$ } }
            - has: { field: arguments, has: { nthChild: 2, matches: built-query } }
description: |
  Build SQL with placeholders, not string concatenation or fmt.Sprintf, to
  avoid injection. untrusted-input-to-sink covers other languages and sinks.
fix_policy: describe
citation:
  doc: https://cheatsheetseries.owasp.org/cheatsheets/Query_Parameterization_Cheat_Sheet.html
export:
  agents: true
  summary: "Never build SQL by string concatenation; use placeholders."
```
