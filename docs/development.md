# Developing

Use the repository's devenv environment: `devenv shell`, or its direnv setup. It provides:
- Go 1.27.2, golangci-lint, ast-grep, Semgrep and universal-ctags;
- Postgres 16 with pgvector;
- Python 3.13 with uv.

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

**Postgres tests** start a pgvector container with testcontainers-go, so they need Docker or Podman. They skip without one unless `EXEMPLAR_REQUIRE_DB=1`. With `EXEMPLAR_REQUIRE_ENGINES=1` (as in CI), engine-dependent tests fail instead of skipping.

**Python** lives in `learning/`:

```bash
make py-check    # or: make -C learning check
```

**Generated query code.** After changing `db/queries`, regenerate `internal/store/queries` with sqlc v1.31.1 (run through `go run`, so nothing to install):

```bash
make sqlc
```

`make images` and `make manifests` mirror CI's images job; they need Docker and kubectl, which devenv does not provide. Outputs (binaries from `make bins`, the replay result, rendered manifests) go to `out/`.

**Layout:**
- `cmd/`: the binaries;
- `internal/`: the packages;
- `db/`: migrations and queries;
- `schema/`: the JSON Schemas for exemplar-config;
- `deploy/`: images and manifests;
- `dashboards/`: Grafana;
- `examples/exemplar-config/`: a starting config;
- `docs/`: this site's pages (`mkdocs.yml`; `make docs`, `make docs-serve`), and the spec, the PRD and the plans, which the site leaves out.
