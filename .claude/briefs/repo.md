# Brief: Exemplar for pipeline agents

The repo's notes for the pipeline plugin's agents (the plugin's `briefs/core.md` → *The repo's
own briefs*). Everything here is Exemplar's, not the pipeline's; read the section for your
role. Surfaces and the commands that check and gate them are in `.claude/pipeline.json`. The
repository map, work tracking and the ask-before rules are in `.claude/CLAUDE.md`.

## The repo

Exemplar is a Go service deployed on Kubernetes that learns a codebase's conventions and reviews
pull requests against them. One Go module at the repo root (`github.com/koblas/exemplar`):

- `cmd/<binary>/` — entrypoints: `exemplar` (CLI), `exemplar-gateway`, `exemplar-search`,
  `exemplar-webhook`, `exemplar-worker`, `exemplar-learn`, `exemplar-api` (also serves `/setup`
  and the `/ui/` web UI).
- `internal/<pkg>/` — private packages. Not to be confused with this plugin:
  `internal/pipeline` is Exemplar's own review pipeline.
- `db/migrations/`, `db/queries/` → `internal/store/queries/` (sqlc output, **tracked but
  generated**: never hand-edit; a finding there goes against `db/queries/*.sql`). Regenerate
  with the config's `sqlc` request (`make sqlc`); `make sqlc-check` proves it is current.
- `deploy/` (Kustomize, werf images; `go test ./deploy` checks builds against the code),
  `dashboards/` (`go test ./dashboards`), `schema/`, `docs/` (Zensical site; `go test ./docs`).
- `learning/` — a Python (uv) project. **Not a pipeline surface**: the plugin supports Go and
  TypeScript only, so pipeline work there gets no checks or gate. Verify it by hand
  (`make py-check`) and say so in the report.

Product docs: `docs/product/spec/` (the technical spec, `§` numbers) and `docs/product/prd/`
(`FR-…` ids); `docs/product/README.md` maps both to files. Feature specs are
`docs/specs/<slug>.md` (format in `docs/specs/README.md`); run output goes to `.runs/<slug>/`.
Work is tracked in GitHub Issues (`.claude/CLAUDE.md` → *Work Tracking*): the pipeline's
feature issue and sub-issues follow that body format and carry `phase:` / `order:` / `lane:`
labels.

**Where commands run:** every Go command from the repo root. `make check` is the quick gate
(lint, format check, tests); `make ci` mirrors CI.

**Toolchain:** devenv (Go, golangci-lint). The session hook loads it. If a direct cgo build
fails with `NIX_BUILD_TOP: unbound variable`, rerun with `NIX_ENFORCE_PURITY=0` (the Makefile
exports it). Postgres tests use testcontainers and **skip** without a container runtime; engine
tests skip without engines. Count skips before leaning on a package, and set
`EXEMPLAR_REQUIRE_DB=1` / `EXEMPLAR_REQUIRE_ENGINES=1` when a claim depends on them.

## Briefs, skills and rules here

| File | When |
| --- | --- |
| `.claude/rules/go-code.md` | any Go code or test: checks, service conventions, the §16.1 test technique per change, *Time and clocks* (no injected clocks, `synctest`, `// wallclock:`) |
| `.claude/rules/ux-flows.md` | `internal/setup/**`, `internal/web/**`: steps, states, errors, copy — product-vision and the developer on those pages |
| `.claude/rules/k8s.md` | `deploy/**`, images |
| `.claude/rules/docs-voice.md` | public docs pages |
| `.claude/skills/go-service/SKILL.md` | Go production code: service shape, lifecycle, focused tests (the repo's architecture skill) |
| `.claude/skills/docs/SKILL.md` | a behaviour change that the user docs describe |
| `.claude/skills/issue/SKILL.md` | filing a follow-up issue |

The repo has no Go test skill of its own: the test rules are `go-code.md` → *Tests* and
*Time and clocks*, plus the plugin's `tdd` skill. The repo's own reviewer:
`correctness-reviewer`.

## For triage

Blast radius map: `cmd/<binary>/` (which binary wires it), `internal/<pkg>/` (owning package),
`db/migrations` + `db/queries` (schema and SQL), `internal/setup` / `internal/web` (pages),
`deploy/**` (env vars, probes, manifests), `docs/reference/**` (env vars, CLI commands, schema
properties `go test ./docs` checks), `schema/` (config schema, Python contracts). A changed
setting, CLI command or schema property needs its reference page; a changed env var its
manifests. Name the spec `§` / `FR-` ids the change touches.

## For architect and developer

- Pick the test technique by the change (`go-code.md` → *Tests*, spec §16.1): worker state
  machine → seeded simulation invariant (`internal/worker/sim_test.go`); review output →
  metamorphic relation over `replay --scripted`; parser of outside input → native fuzz target
  added to `make fuzz` and `ci.yml`; invariant over an input domain → seeded property test; new
  setting → configuration-matrix row; hot path → benchmark. Name a skipped technique and why.
- **The mandatory test-first set** (the plugin's `briefs/build.md` → *Build cadence*) maps here:
  security guards = webhook signature checks, signed feedback links, service auth, OIDC/Auth0,
  root-key derivation in `internal/envcfg`, secret redaction (`internal/redact`); atomic store
  adapters = Postgres writes with a uniqueness or conditional claim (`ON CONFLICT`, job locks).
- Keep transport, business logic and infrastructure separate; small interfaces at the
  consumer. Propagate context; timeouts on every server and client; graceful shutdown.
- New SQL: edit `db/queries/*.sql` (and a migration), run the `sqlc` request, never edit
  `internal/store/queries`.
- Behaviour change visible to users → update the docs pages, not the README (`/docs`).
- Findings below MAJOR become issues, not same-PR fixes.

## For product-vision

Exemplar's consumers are: the **operator** installing and configuring it (`/setup`, env vars,
Kustomize overlays, `docs/guides/`), the **developer whose PR is reviewed** (review comments,
check runs, signed feedback links), the **`/ui/` web UI** user, and the **CLI** user
(`exemplar review`, evaluations). The product authority is `docs/product/prd/` and
`docs/product/spec/`; read the sections bearing on the change. Rule pages and copy per
`.claude/rules/ux-flows.md`; a label the docs quote is a docs change too. Where the plugin agent
speaks of a generated API client, Exemplar has none: its programmatic consumers are the GitHub
App surface, `/v1/evaluate` and the CLI's output.
