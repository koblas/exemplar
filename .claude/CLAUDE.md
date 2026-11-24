# Go Kubernetes Service

## Scope

This repository is Exemplar, a Go service deployed on Kubernetes. The Go module
is `github.com/koblas/exemplar`. The spec is `docs/product/spec/` and the PRD
`docs/product/prd/`, one file per top-level section; `docs/product/README.md` maps
`§` numbers and PRD sections to files (`FR-…` ids are PRD requirements). A PRD
requirement that is not built ends its id cell with `(not built: #N)`.
`docs/plans/README.md` maps the roadmap and requirement families to both.
`docs/plans/ready-to-install.md` is frozen: don't add sections there. The Phase 0
plan, the head-start logs and the backlog are at commit `3a23e68` under `docs/plans/`. Binaries: `cmd/exemplar` (CLI), `cmd/exemplar-gateway`,
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
are in `deploy/docker`, built with werf (`werf.yaml`) and published to Docker Hub `koblas/exemplar` and `koblas/exemplar-learning` by `images.yml`. `examples/k8s/acme`
is the example installation overlay (CI renders it). `go test ./deploy` checks every build against the code (see
`docs/reference/workloads.md`); keep components resource-only, since it reads them without kustomize. Grafana boards and alerts
are in `dashboards/`; `go test ./dashboards` runs their SQL.
User documentation is a Zensical site (`mkdocs.yml`, which Zensical reads; pages in `docs/` except
`docs/product`, `docs/plans`, `docs/security`), published to GitHub Pages by `docs.yml`.
It follows the Divio system: `docs/tutorials`, `docs/guides/<persona>` (how-to),
`docs/concepts`, `docs/reference`; `/docs` says which quadrant a change goes in.
`make docs` runs `go test ./docs` (reference coverage of env vars, CLI commands and
schema properties; nav; no spec §/FR/phase references on public pages) and builds
strictly. Update the pages, not the README, when behavior changes.
Python (embed, cluster, rank) lives in `learning/`, a uv project on Python 3.13:
`cd learning && uv sync`, then `uv run pytest` and `uv run ruff check .`. Its models
in `contracts/` are generated from `schema/` by `learning/scripts/gen-contracts.sh`.
Path-scoped rules load with the files they cover: `.claude/rules/go-code.md` (Go
conventions, checks, test techniques, time in tests), `.claude/rules/k8s.md`
(manifests and images), `.claude/rules/ux-flows.md` (the `/setup` pages and
the `/ui/` web UI: steps, states, errors, copy) and `.claude/rules/docs-voice.md`
(public docs pages: register per quadrant, sentences, words, UI labels, admonitions).

## Work Tracking

- Track all work in GitHub Issues (`gh issue`, repo `koblas/exemplar`), never in
  markdown TODO lists, checklists or status tables. `docs/product/` stays the
  source of truth for what and why; an issue links to it and does not restate it.
- Issue body, about 500 characters, readable without opening the repository, five
  parts on their own lines: **Why:** what is wrong or missing and for whom, in one
  sentence; **Design:** the stable ids it implements (`spec §6.4.6`, `FR-L1.9`,
  `T-0.3`, `D6.5`; `docs/product/README.md` maps them to files), never a file path
  or anchor, or "Not in the spec" and the code that motivates it; **State:** what
  the code does today, naming the file or test that shows it; **Approach:** the
  change in one sentence; **Done when:** the observable condition a closer checks.
  `/issue` files one. Fix an older body (path links, copied briefs) when you touch it.
- Labels: a type (`feature`, `task`, `bug`, `debt`, `decision`, `security`, `epic`,
  `idea` for unadopted ideas, with a "worth it when" line); a `phase:0`…`phase:6`
  label naming its PRD §14.1 phase (an epic carries its children's phases);
  `order:H1`…`order:H5` for order of work; `lane:direct|light|full` set at triage.
- An epic is a parent issue; work under it is a sub-issue. Order with blocked-by
  dependencies (`gh api repos/koblas/exemplar/issues/<n>/dependencies/blocked_by`).
- At session start list open, unblocked issues (`gh issue list --label order:H1`, or
  the GitHub Project) and claim one by assigning it (`gh issue edit <n> --add-assignee @me`).
  An issue with a blocked-by link or the `blocked` label (waits on something outside the
  repository) is not workable.
- Work found during a task becomes a new issue before the task continues, under the
  right epic, with "Found in #N" in the body. A debt issue says what was deferred,
  why, and what should trigger paying it; a PR that defers anything links it.
- Review findings below major severity are filed as issues, not fixed in the same PR.
- Feature specs for user-visible changes are `docs/specs/<slug>.md` (format in
  `docs/specs/README.md`); a Full-lane feature also gets `<slug>.retro.md`. Run output
  goes to the gitignored `.runs/<slug>/`.
- Pull requests close issues with `Closes #N`; nothing is closed by hand without a
  reason. PR descriptions carry the history: what landed, decisions taken, how it is
  tested, and what is still missing (as issue links).
- Issues up to #285 were migrated from beads on 2026-11-24; an issue's beads id
  (`exemplar-…`, named by older commits) is in the first revision of its body,
  in the issue's edit history; closed beads history is `.beads/issues.jsonl`
  at commit `3a23e68`.

## Environment and Commands

- Use the repository's devenv environment (`devenv shell` or its direnv setup).
- Check tool availability first. Devenv provides Git, Go 1.27.4, golangci-lint v2.15,
  ripgrep, ast-grep, Semgrep, universal-ctags, Python 3.13 and uv, but not
  Postgres (tests run it with testcontainers), kubectl, Helm, or container tooling
  (`gh` comes from the host). Do not silently install tools or change devenv.
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
- `/issue <work or #n>`: file or reshape a GitHub issue (Design, State, Approach,
  Done when, parent epic, phase/order labels, blocked-by). `/pr-close [#n]`: run the gate,
  write the PR description (what landed, decisions, tests with the §16.1 technique
  used or skipped, what is still missing as issues), with `Closes #N`.
- `/docs <behavior change or page>`: document a change in the right Divio quadrant,
  then run `make docs`. `/docs-audit [area]`: read-only report of quadrant mixing,
  coverage gaps, internal references and stale notes.