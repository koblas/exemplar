## 14. Technology stack

Pin exact versions at implementation time (Go modules via `go.mod`; Python via `uv.lock`).

**Go (services, CLI, GitHub-touching jobs)**

| Purpose | Package |
|---|---|
| Go toolchain | Current stable Go release (range-over-func, per-iteration loop variables, `net/http` pattern routing) |
| GitHub App JWT + installation tokens | `github.com/bradleyfalzon/ghinstallation/v2` ; down-scoped tokens (specific repo + permissions) minted via go-github's create-installation-token call |
| GitHub REST + webhook validation | `github.com/google/go-github` (latest major) — `ValidatePayload` , `ParseWebHook` , typed events |
| GitHub GraphQL | `github.com/shurcooL/githubv4` ; `github.com/Khan/genqlient` for large typed ingest queries |
| GitHub rate limits | `github.com/gofri/go-github-ratelimit` (secondary), `golang.org/x/time/rate` (shared per-installation limiter) |
| Anthropic API | `github.com/anthropics/anthropic-sdk-go` (Messages, tool use, streaming, Message Batches, prompt caching) |
| Postgres | `github.com/jackc/pgx/v5` (+ `pgxpool` ), `sqlc` for typed queries, `github.com/pgvector/pgvector-go` |
| Migrations | `github.com/pressly/goose/v3` |
| Job queue | `github.com/riverqueue/river` (+ `riverpgxv5` driver, River UI) |
| HTTP | stdlib `net/http` |
| Concurrency | `golang.org/x/sync/errgroup` , `golang.org/x/sync/semaphore` |
| JSON Schema | `github.com/invopop/jsonschema` (struct → schema for model output), `github.com/santhosh-tekuri/jsonschema/v6` (validate rule YAML and contracts) |
| YAML | `github.com/goccy/go-yaml` |
| Globs | `github.com/bmatcuk/doublestar/v4` |
| Unified diff parsing | `github.com/bluekeyes/go-gitdiff` |
| CODEOWNERS | `github.com/hmarr/codeowners` (GitHub semantics: last match wins) |
| Git operations | `git` binary via `os/exec` (shallow blobless sparse clones in workers; bare mirror in `exemplar-search` ) |
| Code search | `github.com/sourcegraph/zoekt` embedded in `exemplar-search` (Apache-2.0); `rg` for the PR overlay in workers; `git grep` for degraded mode |
| Symbols | `universal-ctags` binary in the `exemplar-search` image (used by zoekt at index time) |
| AuthN | `github.com/coreos/go-oidc/v3` , `github.com/golang-jwt/jwt/v5` |
| Telemetry | `go.opentelemetry.io/otel` , `otelhttp` , OTLP exporter; `log/slog` |
| CLI + release | `github.com/spf13/cobra` ; GoReleaser |
| Web UI (Phase 6) | `github.com/a-h/templ` + htmx |
| MCP server (deferred, Appendix C) | `github.com/modelcontextprotocol/go-sdk` — not in the initial build |
| Second provider SDK | The chosen provider's official Go SDK, behind the gateway's provider interface |
| SARIF | `github.com/owenrumney/go-sarif/v2` |
| Secret scanning | `github.com/zricethezav/gitleaks/v8` detect package, embedded in worker, gateway and learning ingest |
| Parse checks, mutation operators | `github.com/tree-sitter/go-tree-sitter` (+ grammars per language in use; cgo), `go/parser` and `go/ast` for Go |
| Jira (linked tickets) | `github.com/andygrunwald/go-jira` (read-only service account) |
| Git | git ≥ 2.40 in worker and `exemplar-search` images (sparse-checkout cone mode, partial clone) |
| Tests | stdlib `testing` + native fuzzing (anchor validation, glob scope, diff parsing), `github.com/stretchr/testify` , `github.com/testcontainers/testcontainers-go` (real Postgres + pgvector), `gopkg.in/dnaeon/go-vcr.v4` (recorded GitHub and Anthropic traffic) |
| Lint | `golangci-lint` , `govulncheck` |

**Python (embedding, clustering, ranking only)**

| Purpose | Package |
|---|---|
| Embeddings | `voyageai` |
| Vectors / clustering | `numpy` , `faiss-cpu` , `scikit-learn` , `networkx` |
| Data | `polars` , `psycopg[binary]` , `pgvector` |
| Contracts | `pydantic` models generated from `schema/` via `datamodel-code-generator` |
| Telemetry | `opentelemetry-sdk` |

**Systems**

| System | Choice |
|---|---|
| Compute | Kubernetes 1.29+ (native sidecars); one image, one Deployment per binary, a StatefulSet for search, CronJobs for learning jobs, and a **Job per review** once the control/runner split ships. **Recommended platform: GKE Autopilot**; EKS Auto Mode second; own nodes (k3s, on-prem) for GHES and air-gapped installs; a laptop cluster behind a tunnel for trials (§2.4) |
| Way in | Cloudflare Tunnel by default (nothing public in the cluster; WAF rule and Access at the edge), or an Ingress controller, or both |
| Database | PostgreSQL 16 with `pgvector` : managed (Cloud SQL / AlloyDB, RDS / Aurora) with one read replica for dashboards and the playground and a pooler in front for runners; the in-cluster component for trials |
| Object storage | S3 / GCS |
| Secrets | External secret store (Cloud KMS + External Secrets / Vault); one root key derives the internal tokens |
| Model access | Anthropic API or Bedrock (provider A) and provider B, both through `exemplar-gateway` |
| Gateway | `exemplar-gateway` (in-house Go reverse proxy, two providers), scaled on request rate |
| Repo context | `exemplar-search` StatefulSet (our Go binary embedding zoekt), 2 replicas with local SSD, read replicas as load grows — **optional**; without it, references and history come from the checkout (§6.4.6) |
| Rule sandbox | `unshare --net` + `prlimit`; GKE Sandbox (gVisor) or a Kata pool where available; Cloudflare Sandboxes only for compile and the playground, after a data-handling review (§2.4) |
| Lane 1 engines | `ast-grep` CLI (MIT) and Semgrep Community Edition (LGPL-2.1 engine) in the worker image |
| Observability | OTEL collector → existing APM; Grafana |
| CI for config | GitHub Actions (eval workflow), OIDC to `exemplar-api` |

---
