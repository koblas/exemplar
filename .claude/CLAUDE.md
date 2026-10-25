# Go Kubernetes Service

## Scope

This repository is Exemplar, a Go service deployed on Kubernetes. The Go module
is `github.com/koblas/exemplar`. The spec is `docs/spec/technical-spec.md`
(`§` references point there); the PRD is `docs/spec/prd.md` (`FR-…` requirement
ids); `docs/plans/README.md` maps the roadmap and requirement families to
both. Current work follows `docs/plans/ready-to-install.md`; its §7 log ends on
2026-10-24: don't add sections there (history goes in `bd close --reason` and
the PR description, see Work Tracking). `docs/plans/phase-0.md` is
the Phase 0 plan (status in its §9) and the log of head starts up to 2026-10-17;
don't add new sections there. Binaries: `cmd/exemplar` (CLI), `cmd/exemplar-gateway`,
`cmd/exemplar-search` (git mirror, zoekt index, internal API: search_text,
find_definition, find_references, dependents), `cmd/exemplar-webhook` (GitHub
webhook receiver), `cmd/exemplar-worker` (routes events, reviews live PRs and
`exemplar review` evaluations), `cmd/exemplar-learn` (learning-plane jobs),
`cmd/exemplar-api` (signed feedback links, `/v1/evaluate`, the `/ui/` web UI; without
`EXEMPLAR_GH_APP_ID` it runs setup mode, `internal/setup`). Settings checks and the
root-key derivation shared by the binaries are in `internal/envcfg`.
Manifests are plain Kustomize: `deploy/k8s/core` (everything but inbound), inbound
components in `deploy/k8s/components` (`cloudflare-tunnel`, `ingress`), the optional
in-cluster Postgres component (`components/postgres`), and the builds
`deploy/k8s/base` (default: core + tunnel) and `deploy/k8s/direct` (core + Ingress); images
are in `deploy/docker`, published to Docker Hub `koblas/exemplar` by `images.yml`. `examples/k8s/acme`
is the example installation overlay (CI renders it). `go test ./deploy` checks every build against the code (see
`docs/reference/workloads.md`); keep components resource-only, since it reads them without kustomize. Grafana boards and alerts
are in `dashboards/`; `go test ./dashboards` runs their SQL.
User documentation is a Zensical site (`mkdocs.yml`, which Zensical reads; pages in `docs/` except
`docs/spec`, `docs/plans`, `docs/security`), published to GitHub Pages by `docs.yml`.
It follows the Divio system: `docs/tutorials`, `docs/guides/<persona>` (how-to),
`docs/concepts`, `docs/reference`; `/docs` says which quadrant a change goes in.
`make docs` runs `go test ./docs` (reference coverage of env vars, CLI commands and
schema properties; nav; no spec §/FR/phase references on public pages) and builds
strictly. Update the pages, not the README, when behavior changes.
Python (embed, cluster, rank) lives in `learning/`, a uv project on Python 3.13:
`cd learning && uv sync`, then `uv run pytest` and `uv run ruff check .`. Its models
in `contracts/` are generated from `schema/` by `learning/scripts/gen-contracts.sh`.
Path-scoped rules load with the files they cover: `.claude/rules/go-code.md` (Go
conventions, checks, test techniques, time in tests) and `.claude/rules/k8s.md`
(manifests and images).

## Work Tracking

- Track all work in beads (`bd`, Dolt-backed, prefix `exemplar-`), never in
  markdown TODO lists, checklists or status tables. `docs/spec/` stays the
  source of truth for what and why; an issue carries a backlink (doc path plus
  heading anchor, e.g. `docs/spec/technical-spec.md#646-running-without-exemplar-search`)
  and its state, and does not restate the spec. The briefs in
  `docs/plans/backlog.md` stay as design text that issues link to.
- At session start run `bd ready`. Claim with `bd update <id> --status in_progress`.
- File newly found work as its own issue under the right epic, linked to the
  issue you were on: `bd create "<title>" -d "<backlink>" --parent <epic> --deps discovered-from:<id>`.
  Order work with `blocks` dependencies and `order:H1`…`order:H5` labels;
  every issue carries a `phase:0`…`phase:6` label naming its PRD §14.1 phase
  (an epic carries the phases of its children); unadopted ideas are `deferred`
  with the `idea` label.
- Close with `bd close <id> --reason "<what landed, PR or commit>"`.
- At session end export and commit the data: `bd export -o .beads/issues.jsonl`
  from the checkout root, then commit `.beads/issues.jsonl` with the work (ask
  before committing, as for any commit). Close reasons and PR descriptions
  carry the history: what landed, decisions taken, how it is tested, and what is
  still missing (file the missing pieces as issues). Only `.beads/` config and that export
  are committed; the Dolt database is ignored.
- Worktrees share the main checkout's database (`bd context` shows it). `no-push`
  is set: never push Dolt data (`bd sync`, `bd dolt push`) without asking.
- Recovery: in the checkout `bd context` names, move a corrupt database aside
  (`mv .beads/embeddeddolt .beads/embeddeddolt.corrupt`), restore the tracked
  files (`git checkout -- .beads`) and run `bd bootstrap --yes`, which imports
  `.beads/issues.jsonl` when origin has no Dolt data. Ask before deleting the
  moved-aside copy.

## Environment and Commands

- Use the repository's devenv environment (`devenv shell` or its direnv setup).
- Check tool availability first. Devenv provides Git, Go 1.27.3, golangci-lint v2.15,
  ripgrep, ast-grep, Semgrep, universal-ctags, Python 3.13 and uv, but not
  Postgres (tests run it with testcontainers), kubectl, Helm, or container tooling
  (`gh` and `bd` come from the host). Do not silently install tools or change devenv.
- The root `Makefile` (and `learning/Makefile`) wraps the CI commands; `make help`
  lists targets. `make check` is the quick gate, `make ci` mirrors ci.yml. Keep the
  Makefiles and `.github/workflows/ci.yml` in step when either changes.
- Report missing prerequisites and checks not run; never claim unrun tests passed.
- Commit subjects are Conventional Commits (`feat(worker): …`, `fix: …`, `ci: …`;
  types and bumps in `CONTRIBUTING.md`); CI's `commits` job and `make commitlint`
  enforce them. release-please turns them into `CHANGELOG.md` and `v0.x.y` releases;
  never hand-edit released changelog entries or the release manifest.

## Safety

- Ask before cluster access, deployment, image publishing, dependency changes,
  destructive operations, commits, or pushes. Specify context and namespace for
  approved cluster operations; never assume the current context is safe.
- Preserve unrelated user changes. Keep edits small and fix root causes.

## Skills

- `/go-service <task>`: implement or debug a service change with focused tests.
- `/k8s-manifests <task>`: author or validate deployment configuration locally.
- `/service-review <scope>`: review Go/Kubernetes changes without modifying files.
- `/security-hunt [scope|all]`: six-phase security review with subagents (map, hunt by
  class, dedupe, validate, report) into gitignored `.security/`; reads
  `docs/security/known-non-issues.md`. Expensive; the user runs it, CI never does.
- `/bd-ticket <work or id>`: file or reshape a beads issue (backlink, State, Approach,
  Done when, parent epic, phase/order labels, deps). `/pr-close [ids]`: run the gate,
  write the PR description (what landed, decisions, tests with the §16.1 technique
  used or skipped, what is still missing as issues), `bd close --reason`, export.
- `/docs <behavior change or page>`: document a change in the right Divio quadrant,
  then run `make docs`. `/docs-audit [area]`: read-only report of quadrant mixing,
  coverage gaps, internal references and stale notes.