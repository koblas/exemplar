# Recorded PR fixture

A recorded pull request for the replay harness. It either names real commits (`base_sha`, `head_sha`) in a repository reachable at `clone_url`, or carries a small synthetic repository inline. Standalone fixtures live in files such as `examples/fixtures/<id>.yaml` in the Exemplar repository; the same object is embedded as the `fixture` field of [adversarial cases](adversarial-case.md), [verifier labels](verifier-label.md) and [categorizer labels](categorizer-label.md) in exemplar-config, and is checked there by `exemplar validate`. Schema file: [fixture.schema.json](https://github.com/koblas/exemplar/blob/main/schema/fixture.schema.json).

Unknown fields are rejected.

## Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string, pattern `^[a-z0-9][a-z0-9._-]*$` | yes | The fixture id. |
| `repo` | string, pattern `^[^/\s]+/[^/\s]+$` | yes | The repo as `owner/name`. |
| `pr` | integer, minimum 1 | yes | The PR number. |
| `title` | string | yes | The PR title. |
| `body` | string | no | The PR description. |
| `author` | string | no | The PR author. |
| `draft` | boolean | no | Whether the PR is a draft, so a repo's `review_drafts` applies to the replay. |
| `clone_url` | string, non-empty | no | Where the repository holding `base_sha` and `head_sha` can be cloned. |
| `base_sha` | string, pattern `^[0-9a-f]{7,40}$` | see constraints | The base commit of a recorded PR. |
| `head_sha` | string, pattern `^[0-9a-f]{7,40}$` | see constraints | The head commit of a recorded PR. |
| `synthetic` | object | see constraints | A small repository carried inline. See [`synthetic`](#synthetic). |
| `human_comments` | array of object | no | Review comments a human left on the recorded PR. See [`human_comments`](#human_comments). |

Constraints:

- Exactly one of two shapes: `synthetic` without `base_sha` or `head_sha`, or both `base_sha` and `head_sha` without `synthetic`.

### `synthetic`

| Field | Type | Required | Description |
|---|---|---|---|
| `base` | object of path to file contents (string) | yes | The base commit's files. |
| `head` | object of path to file contents (string) | yes | The files the head commit adds or changes. |
| `deleted` | array of string | no | The paths the head commit deletes. |

### `human_comments`

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | string, non-empty | yes | The file the comment is on. |
| `line` | integer, minimum 1 | yes | The line the comment is on. |
| `side` | `RIGHT`, `LEFT` | no | The diff side. |
| `body` | string | yes | The comment text. |
| `author` | string | no | Who left the comment. |

## Example

```yaml
# Synthetic recorded PR: a request handler starts an HTTP call that ignores
# the caller's context. The human reviewer flagged the http.Get line.
id: ctx-http-get
repo: exemplar-sandbox/playground
pr: 101
title: Fetch inventory in the item handler
body: Calls the inventory service when an item page is requested.
author: dev-a
synthetic:
  base:
    go.mod: |
      module example.com/shop

      go 1.27
    handler.go: |
      package shop

      import (
      	"context"
      	"net/http"
      )

      func ItemHandler(ctx context.Context, w http.ResponseWriter, r *http.Request) {
      	w.WriteHeader(http.StatusOK)
      }
  head:
    handler.go: |
      package shop

      import (
      	"context"
      	"fmt"
      	"io"
      	"net/http"
      )

      func ItemHandler(ctx context.Context, w http.ResponseWriter, r *http.Request) {
      	resp, err := http.Get("https://inventory.internal/items/" + r.URL.Query().Get("id"))
      	if err != nil {
      		http.Error(w, fmt.Sprintf("inventory: %v", err), http.StatusBadGateway)
      		return
      	}
      	defer resp.Body.Close()
      	_, _ = io.Copy(w, resp.Body)
      }
human_comments:
  - path: handler.go
    line: 11
    body: This ignores ctx, so a cancelled request keeps the inventory call running. Use http.NewRequestWithContext.
    author: reviewer-b
```
