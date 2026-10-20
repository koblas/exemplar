# platform/retention.yaml

How long each kind of record is kept, in days, before it is deleted; at `platform/retention.yaml` in exemplar-config. The file is required. In the example CODEOWNERS `platform/` is owned by `@security/appsec`. `exemplar validate` checks it. Schema file: [retention.schema.json](https://github.com/koblas/exemplar/blob/main/schema/retention.schema.json).

Unknown fields are rejected.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `approval_decisions_days` | integer, minimum 1 | yes | Days approval decisions are kept. |
| `findings_days` | integer, minimum 1 | yes | Days findings are kept. |
| `feedback_events_days` | integer, minimum 1 | yes | Days feedback events are kept. |
| `model_calls_days` | integer, minimum 1 | yes | Days model call records are kept. |
| `transcripts_days` | integer, minimum 1 | yes | Days transcripts are kept. |
| `gh_events_payload_days` | integer, minimum 1 | yes | Days GitHub event payloads are kept. The row stays for idempotency; the payload is nulled. |

## Example

```yaml
# How long each kind of record is kept, in days, before it is deleted.
# Owned by Security (CODEOWNERS).
approval_decisions_days: 2555   # 7 years
findings_days: 730              # 2 years
feedback_events_days: 730       # 2 years
model_calls_days: 90
transcripts_days: 90
gh_events_payload_days: 30      # the row stays for idempotency; the payload is nulled
```
