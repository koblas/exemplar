# evals/_verifier/*.yaml

One labeled finding per file for verifier calibration, under `evals/_verifier/` in exemplar-config: a PR, a candidate finding on it, and whether the finding is true or wrong. These files are the seed set; labels from author feedback live in the `verifier_labels` table. The bar the verifier must clear against them is `eval_thresholds.verifier` in [global.yaml](global.md#eval_thresholdsverifier) (in the example, refute at least 90% of known-wrong findings and at most 10% of known-true ones). In the example CODEOWNERS the directory is owned by `@platform/exemplar`. `exemplar validate` checks them; ids must be unique across the directory, `candidate.rule` must name an existing rule, and `end_line` may not be before `start_line`. Schema file: [verifier-label.schema.json](https://github.com/koblas/exemplar/blob/main/schema/verifier-label.schema.json).

Unknown fields are rejected.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string, pattern `^[a-z0-9][a-z0-9._-]*$` | yes | The label id. |
| `description` | string, non-empty | yes | What the case shows. |
| `label` | `true`, `wrong` | yes | `true`: the finding is correct; `wrong`: it is not. Quoted in YAML (`"true"`) so it stays a string. |
| `fixture` | [Recorded PR fixture](fixture.md) | yes | The PR the finding is on. |
| `candidate` | object | yes | The finding the label is about. See [`candidate`](#candidate). |

### `candidate`

| Field | Type | Required | Description |
|---|---|---|---|
| `rule` | string, non-empty | yes | The rule the finding is for; its severity and category frame the claim. |
| `path` | string, non-empty | yes | The file the finding is in. |
| `side` | `RIGHT`, `LEFT` | no | The diff side. |
| `start_line` | integer, minimum 1 | yes | The first line of the finding. |
| `end_line` | integer, minimum 1 | yes | The last line of the finding; not before `start_line`. |
| `claim` | string, 1 to 2000 characters | yes | The finding's message, as the generator wrote it. |

## Example

```yaml
id: wrong-context-already-passed
description: The finding claims ctx is ignored, but the request is built with http.NewRequestWithContext(ctx, ...).
label: wrong
fixture:
  id: wrong-context-already-passed
  repo: exemplar-sandbox/playground
  pr: 952
  title: Fetch prices with the caller's context
  synthetic:
    base:
      prices.go: |
        package shop
    head:
      prices.go: |
        package shop

        import (
        	"context"
        	"net/http"
        )

        func Prices(ctx context.Context, c *http.Client) error {
        	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://prices.internal/", nil)
        	if err != nil {
        		return err
        	}
        	resp, err := c.Do(req)
        	if err != nil {
        		return err
        	}
        	return resp.Body.Close()
        }
candidate:
  rule: propagate-context
  path: prices.go
  start_line: 13
  end_line: 13
  claim: c.Do(req) does not pass ctx, so the call cannot be cancelled.
```
