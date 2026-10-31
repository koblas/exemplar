# Contributing

Build, test and lint commands are in the [Makefile](Makefile) (`make help`);
`make check` is the quick local gate and CI (`.github/workflows/ci.yml`) runs
the rest. A pull request that only touches the beads export (`.beads/`), the
Claude Code configuration (`.claude/`: skills, `CLAUDE.md`, settings), the
user documentation (`docs/` outside its Go tests, `mkdocs.yml`) or the
top-level Markdown files runs just the `commits` job, and the `docs` job when
the site changed; anything else runs every job.

## Commit messages

Every commit that reaches `main` follows [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).
The release tooling reads them to pick the next version and write
[CHANGELOG.md](CHANGELOG.md), so CI's `commits` job rejects a pull request
with a commit that doesn't parse.

```
<type>[(<scope>)][!]: <description>

[body]

[footers]
```

- The subject is at most 100 characters. Scopes are lower-case, usually the
  area touched: `worker`, `search`, `deploy`, `learning`, `lint`.
- `!` after the type or scope, or a `BREAKING CHANGE: <what>` footer, marks a
  breaking change.
- git's own `Revert "…"` subjects are accepted.

| Type | Use for | In the changelog | Version bump |
|---|---|---|---|
| `feat` | New behaviour users or operators see | Features | minor |
| `fix` | A bug fix | Bug Fixes | patch |
| `perf` | A performance improvement | Performance | patch |
| `deps` | A dependency bump that matters to users | Dependencies | patch |
| `revert` | Reverting an earlier commit | Reverts | patch |
| `docs`, `refactor`, `test`, `build`, `ci`, `chore`, `style` | Everything else | hidden | none |

Breaking changes bump the minor version until 1.0.0, then the major.

`make hooks` turns on a `commit-msg` hook that checks each message as you
write it. `make commitlint` checks the commits on your branch
(`COMMIT_RANGE=origin/main..HEAD` by default), as CI does. To fix a branch,
reword the offending commits with `git rebase -i` and force-push.

Pull requests are merged with a merge commit, so each of their commits shows
up in the changelog. Squash fix-ups before merging; merge subjects themselves
are ignored.

## Documentation

The user documentation is the Zensical site in `docs/` (`mkdocs.yml`, which Zensical reads),
published to GitHub Pages. It follows the
[Divio documentation system](https://docs.divio.com/documentation-system/):
every page is one of a tutorial (`docs/tutorials/`), a how-to guide
(`docs/guides/<persona>/`, titled "How to …"), an explanation
(`docs/concepts/`) or reference (`docs/reference/`), for one audience, and
never cites the spec by section, a requirement id or a delivery phase.
`docs/spec`, `docs/plans` and `docs/security` are design documents the site
leaves out.

A behaviour change updates the pages, not the README: usually one how-to and
one reference page. `make docs` runs `go test ./docs` (every `EXEMPLAR_*`
variable, CLI subcommand and schema property has a reference entry; every
page is in the nav; no internal references) and then builds the site
strictly, as CI's `docs` job does. `make docs-serve` previews it. When a page
moves, add its old path to the `redirects` map in `mkdocs.yml`. With Claude
Code, `/docs <change or page>` applies the rules and `/docs-audit` reports
gaps without editing.

## Releasing

Releases are cut by [release-please](https://github.com/googleapis/release-please)
(`.github/workflows/release-please.yml`). Versions are `v<major>.<minor>.<patch>`
tags; the first release is `v0.1.0`.

1. Every push to `main` opens or updates one release PR, "chore(main): release
   x.y.z". It bumps `.release-please-manifest.json` and prepends the new
   CHANGELOG.md entry, built from the `feat`, `fix`, `perf`, `deps` and
   `revert` commits since the last release. With none, there is no release PR.
2. Review the entry and merge the PR when you want to ship. You can edit the
   PR's CHANGELOG.md before merging.
3. The merge tags `vx.y.z` and creates its GitHub release, with the entry as
   its notes. The same workflow then:
   - builds the CLI on four platforms and attaches the archives and
     `SHA256SUMS` to the release (`release.yml`);
   - pushes both images to Docker Hub as `<x.y.z>`, `<x.y>`, `latest`,
     `learning-<x.y.z>`, `learning-<x.y>` and `learning-latest`
     (`images.yml`). Their run summary lists the digests to pin (docs,
     "Releases and images");
   - builds both with werf and pushes them to GHCR as `exemplar-latest` and
     `exemplar-learning-latest` (`werf.yml`);
   - renders `install-tunnel.yaml` and `install-direct.yaml` with those
     digests and attaches them to the release (`manifests.yml`).

To force a version, put a `Release-As: 0.3.0` footer in a commit on `main`.

**Setup** (once, in the repository settings): Settings > Actions > General >
Workflow permissions must allow GitHub Actions to create and approve pull
requests. release-please uses the workflow's `GITHUB_TOKEN`, so the release PR
runs no CI; it only touches CHANGELOG.md and the manifest.

**By hand.** Pushing a `vx.y.z` tag still runs `release.yml` and `images.yml`
directly, for a hotfix release outside release-please. It updates neither
CHANGELOG.md nor the manifest, so record it there afterwards, and run the
`manifests` workflow with the tag once `images.yml` has pushed its images.
To rebuild a release's CLI archives, run the `release` workflow with its
tag; to re-render its install manifests, run `manifests` with it.
