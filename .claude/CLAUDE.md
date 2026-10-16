# Go Kubernetes Service

## Scope

This repository is Exemplar, a Go service deployed on Kubernetes. The Go module
is `github.com/koblas/exemplar`. The spec is `docs/spec/technical-spec.md`
(`§` references point there); the PRD is `docs/spec/prd.md` (`FR-…` requirement
ids); `docs/plans/README.md` maps the roadmap and requirement families to
both. Phase 0 work follows `docs/plans/phase-0.md`
(status in its §9), which also logs every later step; add a section there for
each new piece of work. Binaries: `cmd/exemplar` (CLI), `cmd/exemplar-gateway`,
`cmd/exemplar-search` (git mirror, zoekt index, internal API: search_text,
find_definition, find_references, dependents), `cmd/exemplar-webhook` (GitHub
webhook receiver), `cmd/exemplar-worker` (routes events, reviews live PRs and
`exemplar review` evaluations), `cmd/exemplar-learn` (learning-plane jobs),
`cmd/exemplar-api` (signed feedback links, `/v1/evaluate`, the `/ui/` web UI).
Manifests are plain Kustomize: `deploy/k8s/core` (everything but inbound), inbound
components in `deploy/k8s/components` (`cloudflare-tunnel`, `ingress`), the optional
in-cluster Postgres component (`components/postgres`), and the builds
`deploy/k8s/base` (default: core + tunnel) and `deploy/k8s/direct` (core + Ingress); images
are in `deploy/docker`. `go test ./deploy` checks every build against the code (see
`deploy/README.md`); keep components resource-only, since it reads them without kustomize. Grafana boards and alerts
are in `dashboards/`; `go test ./dashboards` runs their SQL.
Postgres tests start a pgvector container with testcontainers-go (needs a
container runtime such as Docker or Podman); they skip without one unless
`EXEMPLAR_REQUIRE_DB=1`. Regenerate `internal/store/queries` with sqlc v1.31.1.
Python (embed, cluster, rank) lives in `learning/`, a uv project on Python 3.13:
`cd learning && uv sync`, then `uv run pytest` and `uv run ruff check .`. Its models
in `contracts/` are generated from `schema/` by `learning/scripts/gen-contracts.sh`.
Do not treat the suggested layout below as existing code or scaffold an entire
application unless requested. Update this guidance as the service takes shape.

## Environment and Commands

- Use the repository's devenv environment (`devenv shell` or its direnv setup).
- Check tool availability first. Devenv provides Git, Go 1.27.2, golangci-lint v2.14,
  ripgrep, ast-grep, Semgrep, universal-ctags, Postgres 16 with pgvector, Python 3.13
  and uv, but not kubectl, Helm, or container tooling. Do not silently install tools or
  change devenv. Set `EXEMPLAR_REQUIRE_ENGINES=1` (as CI does) to make engine-dependent
  tests fail rather than skip; if cgo builds fail with `NIX_BUILD_TOP: unbound variable`,
  set `NIX_ENFORCE_PURITY=0`.
- Once a Go module exists, follow its Go version and existing build/CI commands.
- The root `Makefile` (and `learning/Makefile`) wraps the CI commands; `make help`
  lists targets. `make check` is the quick gate, `make ci` mirrors ci.yml. Keep the
  Makefiles and `.github/workflows/ci.yml` in step when either changes.
- Default Go checks: `go test ./...`, `go test -race ./...`, `go vet ./...`, and
  `go build ./...`. Start with the touched package's tests, then broaden as needed.
- Use `golangci-lint run` once a Go module exists; follow repository lint configuration.
- Format touched Go files with `gofmt`; do not reformat unrelated files.
- Use existing manifest tooling. Render Helm or Kustomize locally before checking
  the rendered resources. Do not assume a cluster is available.
- Report missing prerequisites and checks not run; never claim unrun tests passed.

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