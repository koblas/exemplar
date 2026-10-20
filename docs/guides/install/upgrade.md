# How to upgrade

1. Read the release notes ([CHANGELOG.md](https://github.com/koblas/exemplar/blob/main/CHANGELOG.md)). They list behaviour changes that need action; the ones so far are below.
2. Point the overlay at the new release's install file, or move `ref=` and both digests to it ([Releases and images](../../reference/releases.md)).
3. Render, check and apply as in [How to install for production](production.md#5-render-check-and-apply).

**Migrations** run when the new pods start, under a lock, so only one pod migrates. They are forward-only in production.

**Shutdown** is graceful. Each pod's termination grace period outlasts its shutdown deadline, so in-flight reviews finish or are retried by River.

**Keys.** Nothing in an upgrade rotates a key. To rotate one, see [How to rotate keys](rotate-keys.md).

## Changes that need action

### Security fixes of 2026-10-19

- Config CI's Auth0 client needs `evaluate:` permissions for every repo its eval cases reference, not only exemplar-config ([How to set up config CI](../rules/config-ci.md)).
- `native_approve` branches that require approvals must dismiss stale approvals on push, or auto-approve stops ([How to turn on auto-approve](../operate/auto-approve.md)).
- PRs that change a file excluded from review (binary, over 1 MB, deleted, ignored) are no longer auto-approved.
- An overlay that patches the evals CronJob must keep its new `--config` argument and config-sync init container.
- OIDC sessions signed in before the upgrade keep their old, unprefixed login until they expire (8 hours). To end them at once, rotate the session key.

### Security fixes of 2026-10-20

- An overlay that patches the extract CronJob must keep its new `--config` argument and config-sync init container.
- People signed in before the upgrade sign in again the next time they open a feedback link: the link now checks that they can read the finding's repo. With OIDC sign-in, that repo must be in their groups' `EXEMPLAR_OIDC_GROUP_REPOS` mapping, or the link shows "not found".
- If you enabled the compile CronJob, its memory limit is now 6Gi, above the 4 GiB per-engine limit.
