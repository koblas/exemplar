# How to enable rule exports

Exports render the active rules' one-line summaries into coding-agent instruction files in each repository that opts in ([Rule lifecycle](../../concepts/rule-lifecycle.md#digest-and-exports)). They are written by a **separate GitHub App** with Contents and Pull requests write, installed only on the repositories that opt in and never on `exemplar-config`, so the review app's token never writes to a product repository.

## 1. Create the exports app

=== "During setup"

    On the setup page (port-forwarded to exemplar-api, [Quickstart step 6](../../tutorials/quickstart.md#6-register-the-github-app)), also create the exports app, install it only on the repos that opt in, and apply the `exemplar-exports-app.yaml` it offers.

=== "After setup"

    Register a GitHub App by hand with **Contents: Read & write**, **Pull requests: Read & write** and no events. Install it on the opting-in repositories. Create the Secret:

    ```bash
    kubectl -n exemplar create secret generic exemplar-exports-app --from-literal=app-id=<id> --from-file=private-key=<pem>
    ```

The learn `export` CronJob skips with a message until the Secret exists.

## 2. Opt repositories in

In each `repos/<org>/<repo>.yaml`:

```yaml
exports:
  targets: [agents_md, claude_md, copilot, cursor]
  auto_merge_generated: true
```

`targets` picks the files (`AGENTS.md`, `CLAUDE.md`, GitHub Copilot's and Cursor's instruction files). With `auto_merge_generated`, an export PR whose only changes are between the markers merges on its own.

## 3. Mark rules for export

A rule is exported when its `export.agents` is `true`; `export.summary` (at most 300 characters) is the line agents read. A change to any rule's `export:` block needs an approval from the platform team as well as the owner, enforced by the `export-approval` workflow in config CI.

## What to expect

Daily, the job opens or updates one `exemplar/exports` branch and PR per repository, only when the rendering changed. Rules go between `BEGIN`/`END exemplar` markers; anything outside is the team's and never touched. A block someone edited by hand is left alone and reported on the shift-left board and in the platform team's digest, which also lists repositories more than two config versions behind.
