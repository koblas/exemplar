# How to set up the development environment

Use the repository's devenv environment: `devenv shell`, or its direnv setup. It provides:

- Go 1.27.3, golangci-lint, ast-grep, Semgrep and universal-ctags;
- Postgres 16 with pgvector;
- Python 3.13 with uv.

It does not provide Docker, kubectl, Helm or Kustomize.

## Run the checks

The `Makefile` wraps the usual commands; `make help` lists every target. Each mirrors a step in `.github/workflows/ci.yml`.

```bash
make check       # build, vet, lint, format check, tests
```

```bash
make test-race
```

```bash
make ci          # everything ci.yml runs except the image and manifest jobs
```

**Postgres tests** start a pgvector container with testcontainers-go, so they need Docker or Podman. They skip without one unless `EXEMPLAR_REQUIRE_DB=1`. With `EXEMPLAR_REQUIRE_ENGINES=1` (as in CI), engine-dependent tests fail instead of skipping. If cgo builds fail with `NIX_BUILD_TOP: unbound variable`, set `NIX_ENFORCE_PURITY=0` (the Makefile does).

**Python** lives in `learning/`:

```bash
make py-check    # or: make -C learning check
```

## Regenerate code

After changing `db/queries`, regenerate `internal/store/queries` with sqlc v1.31.1 (run through `go run`, so nothing to install):

```bash
make sqlc
```

After changing `schema/`, regenerate the Python contract models:

```bash
make contracts
```

## Build images and manifests

`make images` and `make manifests` mirror CI's images job; they need Docker and kubectl. Outputs (binaries from `make bins`, the replay result, rendered manifests) go to `out/`. [How to check manifests locally](check-manifests.md) has the details.

## Layout

- `cmd/`: the binaries;
- `internal/`: the packages;
- `db/`: migrations and queries;
- `schema/`: the JSON Schemas for exemplar-config;
- `deploy/`: images and manifests;
- `dashboards/`: Grafana;
- `examples/exemplar-config/`: a starting config;
- `docs/`: this site's pages (`mkdocs.yml`; `make docs`, `make docs-serve`), and the design documents (`docs/spec`, `docs/plans`, `docs/security`), which the site leaves out.

Commit subjects are Conventional Commits; [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md) has the types and the release process.
