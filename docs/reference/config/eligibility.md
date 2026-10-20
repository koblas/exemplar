# platform/eligibility.yaml

Repos that are never reviewed, mined, indexed or sent to a model provider, at `platform/eligibility.yaml` in exemplar-config. The file is required. Nothing in a target repo can override it. In the example CODEOWNERS `platform/` is owned by `@security/appsec`. `exemplar validate` checks it. Schema file: [eligibility.schema.json](https://github.com/koblas/exemplar/blob/main/schema/eligibility.schema.json).

Unknown fields are rejected. A repo is reviewed only when it is opted in (its [repo file](repo.md) has `enabled: true`), not denylisted here, and Exemplar is enabled in [global.yaml](global.md).

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `denylist` | array of object | yes | The denylisted repos. A repo listed twice is rejected. See [`denylist`](#denylist). |

### `denylist`

| Field | Type | Required | Description |
|---|---|---|---|
| `repo` | string, pattern `^[^/]+/[^/]+$` | yes | The repo as `owner/name`. |
| `reason` | string, non-empty | yes | Why the repo is denylisted. |

## Example

```yaml
# Repos that are never reviewed, mined, indexed, or sent to a model provider.
# Owned by Security (CODEOWNERS); nothing in a target repo can override it.
denylist: []
```

With an entry:

```yaml
denylist:
  - repo: acme/secrets-vault
    reason: contains customer credentials
```
