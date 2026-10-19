# Upgrading

Point the overlay at the new release's install file (or move `ref=` and both digests to it). Then render, check and apply.
- **Migrations** run when the new pods start, under a lock, so only one pod migrates. They are forward-only in production.
- **Shutdown** is graceful. Each pod's termination grace period outlasts its shutdown deadline, so in-flight reviews finish or are retried by River.
- **Release notes** list behavior changes that need action. The security fixes of 2026-10-19 need:
  - Config CI's Auth0 client needs `evaluate:` permissions for every repo its eval cases reference, not only exemplar-config.
  - `native_approve` branches that require approvals must dismiss stale approvals on push, or auto-approve stops.
  - PRs that change a file excluded from review (binary, over 1 MB, deleted, ignored) are no longer auto-approved.
  - An overlay that patches the evals CronJob must keep its new `--config` argument and config-sync init container.
  - OIDC sessions signed in before the upgrade keep their old, unprefixed login until they expire (8 hours). To end them at once, rotate the session key.
- **Keys.** Rotating the root key signs everyone out and changes every pseudonym, which breaks joins with older mined data. To rotate one key alone, set its explicit Secret ([deployment reference](../reference/deployment.md#what-an-installation-supplies)).
