# How to turn on auto-approve

Auto-approve lets Exemplar submit a native `APPROVE` on a narrow class of low-risk pull requests once every deterministic gate and a two-model categorisation pass ([How a review works](../../concepts/how-a-review-works.md#auto-approve)). It stays off until you turn it on in `global.yaml` **and** in each repository.

!!! note "Not before the counter-metrics are in place"
    Turn it on only once `exemplar-search` is running (without it every review has a skipped bug class and is never approved; [How to run without exemplar-search](../install/without-search.md)), the Grafana auto-approve and counter-metrics boards and their alerts are provisioned ([How to set up Grafana](grafana.md)), and after a shadow period with zero approvals from degraded runs. The design's exit criteria for enabling it are in the [technical specification](https://github.com/koblas/exemplar/blob/main/docs/spec/technical-spec.md).

## 1. Choose the repository's approval mode

Run the probe once per ruleset, on a sandbox PR with no approvals; it submits an `APPROVE` from the App, reads GitHub's review decision, and dismisses it again:

```bash
exemplar sandbox probe-approval --repo <org>/<sandbox-repo> --pr <number> --app-id <id> --installation-id <id> --private-key-file <pem> --config-repo <org>/exemplar-config
```

Its JSON output names the `approval_mode` to set in the repo file:

=== "native_approve"

    Exemplar's `APPROVE` counts toward the ruleset's required approvals. The branch rules **must dismiss stale approvals on push** (classic protection's *Dismiss stale pull request approvals*, or a ruleset's `dismiss_stale_reviews_on_push`); otherwise the gate fails with `stale_approvals_not_dismissed_on_push` and Exemplar does not approve.

=== "merge_policy_check"

    For repositories whose rulesets require code-owner review. The ruleset requires the `exemplar/merge-policy` check, restricted to the review app as its source, in place of the code-owner review requirement, and keeps a break-glass bypass actor for incidents. Exemplar never sets that actor. The check counts an approval only from someone with write access to the repo.

## 2. Configure the repository

In `repos/<org>/<repo>.yaml` ([reference](../../reference/config/repo.md)):

```yaml
approval_mode: native_approve
auto_approve:
  enabled: true
  paths: ["docs/**", "**/*.md", "config/feature-flags/**"]
  allowed_authors: ["@acme/engine-team"]
  approvable_owners: ["@acme/docs", "@acme/engine-team"]
  bot_authors:
    - identity: "renovate[bot]"
      paths: ["config/feature-flags/**"]
  extra_denylist: ["config/feature-flags/prod/**"]
  max_files: 10
  max_changed_lines: 1000
```

- `paths` is the allow list; the hard-coded denylist and `extra_denylist` always win. The denylist covers CODEOWNERS, CI workflows, `.exemplar/**`, `.gitattributes` and `.gitmodules` in any directory, manifests and lockfiles, and files that change what is built, installed or run (`Dockerfile`, Compose files, `setup.py`, `Gemfile`, `.npmrc`, `.pre-commit-config.yaml`, `.devcontainer/**`, git hooks and the like).
- `approvable_owners`: every touched path's CODEOWNERS owners must all be listed, or the PR is ineligible. Empty approves nothing.
- `bot_authors` names the bot identities that may ever be auto-approved, each with its own narrower paths.
- A repo may lower `max_files` and `max_changed_lines` and raise `min_review_confidence` and `min_merge_safety_score`, never the reverse.

A PR that changes a file Exemplar excludes from review (binary, over 1 MB, deleted, or ignored by `.gitattributes` or `.exemplarignore`) is never auto-approved.

## 3. Turn it on globally

In `global.yaml`, set `auto_approve: true` and, for Stage 2, `providers.categorizer` with two models from different families. Without a categorizer the two-model check cannot run and nothing is approved. With both the generator and the verifier on one family, every finding is flagged `same_family`, which also blocks approval.

## 4. Watch it

The audit page (`/ui/audit`) lists every decision with its Stage 1 reasons, merge-safety score and Stage 2 result, linked to the PR, and each repository's kill switch. The `auto-approve` and `counter-metrics` boards show reverts and incidents on auto-approved merges against human-approved ones. The platform team, or a member of one of the repo's `approvable_owners`, can turn a repo off with one click; turning it back on is a config PR.
