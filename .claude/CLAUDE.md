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
Postgres tests start a pgvector container with testcontainers-go (needs a
container runtime such as Docker or Podman); they skip without one unless
`EXEMPLAR_REQUIRE_DB=1`. Regenerate `internal/store/queries` with sqlc v1.31.1.
Python (embed, cluster, rank) lives in `learning/`, a uv project on Python 3.13:
`cd learning && uv sync`, then `uv run pytest` and `uv run ruff check .`. Its models
in `contracts/` are generated from `schema/` by `learning/scripts/gen-contracts.sh`.
Do not treat the suggested layout below as existing code or scaffold an entire
application unless requested. Update this guidance as the service takes shape.

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
  unadopted ideas are `deferred` with the `idea` label.
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
- Check tool availability first. Devenv provides Git, Go 1.27.3, golangci-lint v2.14,
  ripgrep, ast-grep, Semgrep, universal-ctags, Postgres 16 with pgvector, Python 3.13
  and uv, but not kubectl, Helm, or container tooling. Do not silently install tools or
  change devenv. Set `EXEMPLAR_REQUIRE_ENGINES=1` (as CI does) to make engine-dependent
  tests fail rather than skip; if cgo builds fail with `NIX_BUILD_TOP: unbound variable`,
  set `NIX_ENFORCE_PURITY=0`.
- Once a Go module exists, follow its Go version and existing build/CI commands.
- The root `Makefile` (and `learning/Makefile`) wraps the CI commands; `make help`
  lists targets. `make check` is the quick gate, `make ci` mirrors ci.yml. Keep the
  Makefiles and `.github/workflows/ci.yml` in step when either changes.
- Default Go checks: `go test ./...`, `go test -race ./...`, `golangci-lint run`, and
  `go build ./...`. Start with the touched package's tests, then broaden as needed.
- `golangci-lint run` follows `.golangci.yml` and includes `govet`; there is no
  separate `go vet` step in the Makefile or CI.
- Format touched Go files with `gofmt`; do not reformat unrelated files.
- Use existing manifest tooling. Render Helm or Kustomize locally before checking
  the rendered resources. Do not assume a cluster is available.
- Report missing prerequisites and checks not run; never claim unrun tests passed.
- Commit subjects are Conventional Commits (`feat(worker): …`, `fix: …`, `ci: …`;
  types and bumps in `CONTRIBUTING.md`); CI's `commits` job and `make commitlint`
  enforce them. release-please turns them into `CHANGELOG.md` and `v0.x.y` releases;
  never hand-edit released changelog entries or the release manifest.

## Service Conventions

- Prefer the standard library and existing dependencies. Add dependencies only
  for a concrete requirement; do not assume a router, database, or cloud provider.
- For new code, default to `cmd/<service>/` for entrypoints and `internal/` for
  private packages. Use `deploy/` for manifests unless another layout is established.
- Keep transport, business logic, and infrastructure separate without speculative
  interfaces. Define small interfaces at the consumer when they are needed.
- Propagate `context.Context`, cancellation, and deadlines to downstream calls.
- Return contextual errors, preserving causes with `%w`; avoid panic for ordinary
  failures. Validate configuration at startup and fail clearly on invalid values.
- Use structured logs (default `log/slog`); never log credentials or payload secrets.
- Configure HTTP server/client timeouts, bound request sizes, and shut down gracefully
  on SIGTERM/SIGINT. Stop background workers and close resources within a deadline.
- Keep liveness independent of external dependency outages. Readiness should reflect
  ability to serve traffic and turn false during shutdown. Align probe paths and
  ports with manifests; keep the termination grace period longer than shutdown.
- Add deterministic table-driven tests and `httptest` coverage where appropriate.
  Test cancellation, error paths, and shutdown for changes affecting those behaviors.
- Pick the technique by the change (spec §16.1, "Which technique a change needs"):
  worker state machine → a seeded simulation invariant; review output → a
  metamorphic relation over `replay --scripted`; parser of outside input → a native
  fuzz target (added to `make fuzz` and ci.yml); invariant over an input domain → a
  seeded property test (stdlib `math/rand/v2` with a fixed seed, as
  `TestStage1Invariants`; no new library); new setting → a configuration-matrix row;
  hot path → its benchmark. Don't inject clocks: production code calls `time.Now`,
  and time-dependent tests run in a `testing/synctest` bubble, moving time with
  `time.Sleep` and serving HTTP through an in-memory `RoundTripper`, not `httptest`
  (spec §16.1, "Time in tests"). Seed any randomness. Name a technique you skipped and
  why in the PR description.

## Kubernetes and Safety

- Pin container images to immutable versions or digests; avoid `latest`.
- Run as non-root, drop capabilities, disable privilege escalation, and use
  `RuntimeDefault` seccomp. Prefer a read-only root filesystem with explicit writable
  mounts only where needed. Set justified CPU/memory requests and limits.
- Keep Deployment selectors, pod labels, Service selectors, and ports consistent.
- Use least-privilege RBAC; disable service account token mounting unless needed.
- Keep secrets out of source and rendered output. Reference externally supplied
  Secrets; do not invent credentials or commit kubeconfigs or secret values.
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
- `/docs <behavior change or page>`: document a change in the right Divio quadrant,
  then run `make docs`. `/docs-audit [area]`: read-only report of quadrant mixing,
  coverage gaps, internal references and stale notes.