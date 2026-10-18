# Upgrading

Point the overlay at the new release's install file (or move `ref=` and both digests to it). Then render, check and apply.
- **Migrations** run when the new pods start, under a lock, so only one pod migrates. They are forward-only in production.
- **Shutdown** is graceful. Each pod's termination grace period outlasts its shutdown deadline, so in-flight reviews finish or are retried by River.
- **Keys.** Rotating the root key signs everyone out and changes every pseudonym, which breaks joins with older mined data. To rotate one key alone, set its explicit Secret ([deployment reference](../reference/deployment.md#what-an-installation-supplies)).
