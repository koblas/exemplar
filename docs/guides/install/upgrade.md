# How to upgrade

1. Read the release notes ([CHANGELOG.md](https://github.com/koblas/exemplar/blob/main/CHANGELOG.md)). They list behaviour changes that need action; the ones so far are below.
2. Point the overlay at the new release's install file, or move `ref=` and both digests to it ([Releases and images](../../reference/releases.md)).
3. Render, check and apply as in [How to install for production](production.md#5-render-check-and-apply).

**Migrations** run when the new pods start, under a lock, so only one pod migrates. They are forward-only in production.

**Shutdown** is graceful. Each pod's termination grace period outlasts its shutdown deadline, so in-flight reviews finish or are retried by River.

**Keys.** Nothing in an upgrade rotates a key. To rotate one, see [How to rotate keys](rotate-keys.md).

## Changes that need action

### Runners without the App key (2026-10-24)

- The `exemplar-runner` PodTemplate no longer mounts `exemplar-github-app` or sets `EXEMPLAR_GH_APP_ID` and `EXEMPLAR_GH_PRIVATE_KEY_FILE`. An overlay that patched them back in must drop them; `go test ./deploy` fails on them.
- The worker mints each runner's GitHub tokens into a claim sealed with a key derived from the root key. If you set `EXEMPLAR_CLAIM_KEY_FILE`, set the same file on the worker and the runner template.
- `exemplar review` evaluations now time out after 50 minutes instead of an hour, inside the tokens' one-hour life.

### Reviews run as Jobs (2026-10-24)

- The core manifests now run each review, `exemplar review` evaluation and playground run as a Kubernetes Job (`--launcher=kubernetes`). They add `rbac.yaml` (a Role and RoleBinding for `exemplar-worker`), the `exemplar-runner` ServiceAccount, PodTemplate and NetworkPolicy, and mount a service account token in the worker container. Your cluster must allow the namespace's Role and the Jobs it creates; to keep reviews inside the worker pod, patch `--launcher=in-process` as the quickstart overlays do.
- An overlay that patches the worker's args must keep `--launcher` at `args/7` and `--runner-quota` at `args/8` (the quickstart patches both; `--runner-quota=0` with `--launcher=in-process`, which refuses a quota).
- A NetworkPolicy of your own around the worker must let it reach the Kubernetes API, and the in-cluster Postgres, gateway and search must admit `app.kubernetes.io/name: exemplar-runner` pods (the shipped policies do).
- A review's `config_version` is now the `exemplar-config` commit instead of the checkout's path, so a head reviewed before the upgrade and reviewed again after it is stored as a new review rather than reusing the old row.

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

### Open items from the security hunt (2026-10-20)

- Auto-approve's built-in denylist now also covers files that change what is built, installed or run: `Dockerfile` and `Containerfile` variants, Compose files, `setup.py`, `setup.cfg`, `Gemfile`, `*.gemspec`, `Rakefile`, `.npmrc`, `.yarnrc*`, `.pre-commit-config.yaml`, `Justfile`, `Taskfile`, `build.sbt`, `meson.build`, `.devcontainer/**` and `.husky/**`. PRs that touch them are no longer auto-approved.
- Review comments mined before ingest checked the commenter's relation to the repo still feed candidates. To recheck them, run `exemplar-learn ingest --recheck-authors` once (a Job from the ingest CronJob with that argument added). It re-pages each repo from the start, which costs a full backfill's GraphQL points, and when a repo's pass finishes, marks its earlier comments from people outside the repo untrusted. Those are no longer extracted or clustered, and the next rank run retires candidates that only they supported.
