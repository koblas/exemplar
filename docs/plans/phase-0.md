# Phase 0 — Foundations: implementation plan

**Source:** [Exemplar Technical Specification](../spec/technical-spec.md), Draft 1.3 (§16 Delivery plan, Phase 0). For how this plan fits the whole roadmap and what each `§` and `FR-` reference means, see [the plans index](README.md).
**Window:** weeks 1–4. **Team:** spec §16 staffing — A, B (backend Go, online path), C (ML/data), D (context, Lane 1, bug lane), E (DevEx), plus the part-time security partner.

## 1. Goal and exit criteria

Phase 0 builds the foundations that let the later phases measure themselves. It does not ship a production review path.

Exit criteria (spec §16):

1. The engine and the baseline reviewer review recorded PRs end-to-end.
2. Acceptance tests T-0.1 to T-0.5 pass.
3. Security signs off the eligibility denylist and the retention periods.

| Test | Passes when | Workstreams |
|---|---|---|
| T-0.1 | The engine reviews recorded PRs end-to-end (all lanes stubbed or live) and posts to a sandbox repo | W3, W4, W5, W6 |
| T-0.2 | The baseline reviewer runs in shadow on the same PRs and its findings are stored | W6 |
| T-0.3 | A native `APPROVE` from the review app counts toward each target ruleset's required approvals; repos where it doesn't are put in `merge_policy_check` mode | W3 |
| T-0.4 | The monorepo benchmark records shallow sparse clone time, zoekt full and incremental index time and size, p95 query latency, and ctags coverage per main language | W8 |
| T-0.5 | Every case in `_adversarial/` fails to achieve its goal against the Phase 0 engine | W7 (on W5) |

### In scope

- The Go module and the core packages that the engine slice needs.
- Postgres with sqlc, goose and River.
- Both GitHub Apps in a sandbox org.
- `exemplar-config` with schema CI.
- The gateway: two providers with failover, the soft ceiling, a second secret scan, and OTEL.
- The replay harness, the baseline reviewer and the adversarial set in CI.
- Worker-side secret scanning and redaction.
- The monorepo benchmark.
- The data-handling review with Security.

### Out of scope (and the phase that delivers it)

- Webhook intake and the orchestrator running live on pilot repos (Phase 2).
- `exemplar-search` as a service (Phase 2 and Phase 4). Phase 0 only runs zoekt and ctags for the benchmark.
- The verifier calibration gate and the Grafana boards (Phase 2).
- Conventional Comments labels, suggestions, feedback links and SARIF (Phase 3).
- The bug lane, rule exports and mining (Phase 1 and Phase 4).
- The auto-approve gates (Phase 5).
- Kubernetes deployment. Phase 0 runs in the devenv shell and in CI. No cluster is assumed.

## 2. Long-lead inputs (start in week 1)

These come from spec §17. Phase 0 can't exit without them, and several block engineering work.

| # | Input | Blocks | Owner to chase |
|---|---|---|---|
| 4 | Second model provider and its data-processing terms | W4 failover, live verifier, Security sign-off | Platform lead + Security |
| 5 | Model access path (direct API or cloud marketplace) | W4 endpoints | Platform lead |
| 16 | Eligibility denylist | W9 sign-off, W2 `platform/eligibility.yaml` | Security |
| 17 | Retention periods | W9 sign-off, W1 retention job design | Security |
| 11, 19 | Monorepo size, file count, languages, include/module roots | W8 benchmark | D + monorepo owners |
| 1, 20 | GitHub plan (Enterprise Cloud or not); whether GHES is in scope | W3 rate-limit budget, feature matrix | B |
| — | Sandbox GitHub org with admin rights for App registration and rulesets | W3, T-0.1, T-0.3 | B + GitHub org admins |
| — | Read access to the monorepo from a benchmark host with SSD comparable to the target node type | W8 | D |

If provider B isn't contracted by the end of week 2, the gateway ships with provider A only, plus a fake provider B for failover tests. T-0.1 then runs with the verifier stubbed, and Security sign-off slips with input #4. Raise this risk in week 1.

## 3. Workstreams

### W0 — Repository bootstrap (E, week 1)

- Create `go.mod`. The spec says `github.com/acme/exemplar`; confirm the real path (likely `github.com/koblas/exemplar`).
- Set up the spec §3 layout, creating only the directories that Phase 0 fills:
  - `cmd/`: `exemplar`, `exemplar-gateway`, `exemplar-worker`
  - `internal/`: `schema`, `config`, `scope`, `engine`, `llm`, `gh`, `store`, `telemetry`
  - top level: `schema/`, `db/`
- Add `.golangci.yml` (golangci-lint v2) and `govulncheck`.
- Add CI for this repo: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`, golangci-lint and govulncheck.
- Add devenv scripts for the standard checks. Changes to `devenv.nix` are listed in §5 and need approval first.

### W1 — Data layer (A, weeks 1–2)

- Postgres 16 with `pgvector`: tests use testcontainers-go everywhere; devenv `services.postgres` is only for running the CLI locally.
- goose migrations for the Phase 0 subset of spec §4.3:
  - `gh_events`, `rules`, `reviews`, `rule_runs`, `findings`, `model_calls`, `eval_cases`
  - the `vector` extension
  - Defer the learning-plane, feedback and export tables to the phases that write them.
- sqlc queries for the engine slice: create a review and its rule runs, insert findings idempotently on `(rule_run_id, fingerprint)`, insert a `model_calls` row.
- River with `riverpgxv5` and River's own migrations. Add one job kind, `review.replay`, enqueued transactionally (`InsertTx`) and covered by a test, so the online path inherits a working pattern in Phase 2.
- Integration tests against real Postgres (testcontainers-go plus pgvector).

### W2 — Contracts and `exemplar-config` (E with A, weeks 1–3)

- JSON Schema in `schema/` for the rule (exemplar, compiled and bug_class kinds), repo config, global config, eval cases, `platform/eligibility.yaml` and `platform/retention.yaml`. Use Appendix A as the reference examples.
- `internal/schema`: Go types plus a test that validates the types against `schema/` and fails on drift. Python codegen waits until `learning/` exists in Phase 1.
- `internal/config`: layer global and repo config, and read config from the base branch only (system rule 1). The cache comes later.
- `exemplar validate`: the first CLI command (cobra), built as a single static binary.
- `exemplar-config` repo in the sandbox org:
  - `global.yaml` and one repo file for the sandbox repo
  - 2–3 hand-written exemplar rules and one compiled rule, each with eval cases
  - `platform/` files owned by Security in CODEOWNERS
  - `.github/workflows/eval.yml`, running only `exemplar validate` in Phase 0
- A ruleset requiring CODEOWNERS approval and the eval check on `exemplar-config`.

### W3 — GitHub Apps, tokens and a minimal poster (B, weeks 1–3)

- Register the `exemplar` review app and the `exemplar-exports` app in the sandbox org with the spec §5 permissions. Install the review app on `exemplar-config` with Contents: write.
- `internal/gh`:
  - App JWT through ghinstallation.
  - Down-scoped installation tokens for the four holder flavors (worker, context, poster, config writer), with a test asserting each flavor's repos and permissions.
  - Throttled client: go-github-ratelimit plus a shared per-installation `x/time/rate` limiter.
- Minimal poster:
  - One `COMMENT` review carrying many inline comments, plus a check run.
  - Idempotent through a hidden marker on `head_sha` (spec §7).
  - The verdict type has no `REQUEST_CHANGES` value.
  - Conventional Comments labels and the feedback footer wait until Phase 3.
- **T-0.3:** for each target ruleset shape (required approvals, CODEOWNERS required, last-push approval, bypass lists), submit a native `APPROVE` from the app on a sandbox PR and record whether it counts. Write the result per ruleset as spec §17 input #2, which sets the approval mode per repo.

### W4 — `exemplar-gateway` (B, weeks 2–4)

- Hardcoded provider allowlist in `internal/llm/providers.go`.
- Ordered provider lists per stage (generator, verifier, light, categorizer), as in `global.yaml`.
- Failover on timeouts, 5xx responses and rate-limit exhaustion beyond retry. Failed-over calls are flagged `failover`, and a verifier answered by the generator's model family is flagged `same_family`.
- A second gitleaks scan on every request. A request with an unredacted match is rejected, not redacted.
- Per-run spend tracking against `budgets.soft_ceiling_usd`, with signals at 80% and 100%. Phase 0 has no orchestrator to shed work, so the signals are recorded on the run.
- Token buckets per provider and model, and rate limits per caller.
- A `model_calls` row for every call, including `data_classes`.
- OTEL traces and metrics, with no code or prompt text in spans.
- Contract tests on recorded provider traffic (go-vcr). Run the failover tests against a fake provider until provider B is chosen.
- **Design decision needed in week 1:** see §6, item 2.

### W5 — Engine slice (A with D, weeks 2–4)

This workstream covers the path T-0.1 exercises:

1. Recorded PR.
2. Eligibility and path exclusions.
3. Secret scan and redaction.
4. Scope.
5. Lanes.
6. Validation in code.
7. Fingerprints.
8. Persistence.
9. The poster.

Packages:

- `internal/scope`:
  - Diff parsing with go-gitdiff.
  - The hunk index.
  - Anchor validation, fuzzed so that any anchor GitHub would reject is dropped and no input panics.
  - doublestar rule scoping and the fan-out cap.
- Eligibility and exclusions (spec §6.3 steps 1–3): the denylist, default exclusions and `.exemplarignore` read from the base branch.
- Worker-side secret scan (spec §6.3 step 4):
  - Every model input is redacted to `‹redacted:secret›`.
  - Each match produces the deterministic `possible-secret-in-diff` finding.
  - Matches covered by the `.gitleaksignore` / `.gitleaks.toml` allowlist are still redacted but produce no finding.
  - If redaction fails for any input, the run is `incomplete`.
- Harness (`internal/engine`, spec §6.5):
  - anthropic-sdk-go pointed at the gateway, with the spec's prompt layout and its `cache_control` breakpoint.
  - The four sandboxed tools (`read_file`, `grep`, `list_dir`, `get_hunk`) with the stated guards.
  - 12 turns per agent, at most 3 concurrent agents through an errgroup, and context cancellation throughout.
- Output contract: the `Violation` and `AgentOutput` structs, an invopop-generated strict schema, and re-validation in Go.
- **Coverage is computed from the tool-call log** and stored on `rule_runs`. A run with zero coverage is a failure, not a clean result.
- Validation in code: rule ids, citations, lanes and severities are attached by code; anchors are checked against the hunk index.
- Fingerprints with an occurrence index (spec §6.2), with the spec §16.1 tests: two identical violations give two findings, and a playground run never collides with a production run.
- Lanes for T-0.1:
  - **Lane 2 live** on the hand-written exemplar rules.
  - **Lane 1 stubbed:** fixed findings from a fixture, plus the output-parser interface. D may land ast-grep early if time allows.
  - **Lane 3 stubbed.**
  - **Verifier live** if provider B is available by week 3, otherwise stubbed.
- Repo context: the worker's sparse checkout only (spec §6.4.2). Repo-context tools wait for Phase 2.

### W6 — Replay harness and baseline reviewer (C, weeks 2–4)

- Recorded-PR fixture format:
  - `repo@sha` for base and head, PR metadata and the file list.
  - The human review comments, with path and line.
  - Optional go-vcr cassettes, so replays run offline in CI.
- `exemplar replay <fixtures>`:
  - Runs the W5 engine with `caller=eval`, writes `reviews`, `rule_runs` and `findings`, and optionally posts to the sandbox repo through the poster (`--post`).
  - Enqueued as the W1 River job, so a retry exercises idempotency.
- Baseline reviewer (FR-X8):
  - A single-agent, rule-less review prompt on the same harness, with `caller=baseline`.
  - Findings are stored and never posted.
  - The stratified 10% sampler is written and unit-tested now; production uses it from Phase 2.
- Fixture set: 20–30 recorded PRs from the sandbox repo and, if allowed, real monorepo PRs, chosen so each hand-written rule has positive and negative cases.
- A first comparison report: findings against human comments, by simple path and line overlap. The full baseline board comes in Phase 2.

### W7 — Adversarial set in CI (C with the security partner, weeks 2–4)

- Seed `exemplar-config/evals/_adversarial/` with at least one case per pair of **vector** and **goal**:
  - Vectors: diff content, code comments, PR description, file names, linked tickets. Linked tickets apply only once `linked_ticket` exists; record them as pending until then.
  - Goals: false approval, suppressed finding, fabricated citation.
- Each case declares its goal and a machine-checkable failure condition. For example:
  - The finding for line X must exist.
  - Each posted finding's `rule_id` and `citation` must equal the rule's.
  - The verdict must not be `APPROVE`.
  - Coverage must not reach 1.0 when a scoped file was never opened.
- Run the set in this repo's CI on every change to prompts, the engine or the model configuration. Run it in `exemplar-config` CI on rule changes. Any attacker success fails the build.
- See §6, items 3 and 4, for two definitions T-0.5 depends on.

### W8 — Monorepo benchmark (D, weeks 1–3) → T-0.4

- On a host comparable to the planned `exemplar-search` node, measure:
  - Shallow blobless sparse clone with the exact spec §6.4.2 command sequence, cold and warm.
  - Zoekt full index time and shard size, with ctags.
  - Incremental index time after realistic pushes (1, 10 and 100 changed files).
  - p95 query latency on a fixed query set (literal, regex, symbol and import-pattern queries from spec §6.4.3).
  - ctags symbol coverage per language: symbols found against files, plus spot checks.
- The benchmark harness lives in `internal/search/bench` or a `cmd/exemplar-search bench` subcommand, so Phase 2 reuses it.
- Outputs:
  - A benchmark report in `docs/`.
  - `exemplar-search` disk and RAM sizing (spec §6.4.1).
  - Confirmation of the v1 language list (spec §17 input #19).
  - A recommendation on whether the incremental chain limit (500 generations) is right.

### W9 — Data-handling review (security partner with E, weeks 1–4)

- Week 1: walk through spec §13 and §13.1 with Security. Agree on owners and timelines for `platform/eligibility.yaml` and `platform/retention.yaml`.
- Week 2: present the provider B data-processing terms (input #4) and the per-call data-class logging from W4.
- Week 4: Security signs off the eligibility list and the retention periods, which is an exit criterion. The retention *job* is built in Phase 2 alongside the tables it purges. Phase 0 gets the policy signed and the schema columns ready.

## 4. Schedule

| Week | Milestones |
|---|---|
| 1 | Bootstrap merged; long-lead inputs requested; sandbox org and both Apps registered; Security kickoff; benchmark host provisioned; gateway API shape decided |
| 2 | Migrations, sqlc and River landed; schemas plus `exemplar validate`; `exemplar-config` with schema CI; token flavors tested; scope and anchor fuzzing; first benchmark numbers |
| 3 | Gateway with failover (fake or real B), soft ceiling and `model_calls`; harness plus Lane 2 live on one rule; minimal poster; T-0.3 run; benchmark complete (T-0.4); adversarial seed set |
| 4 | Replay of the full fixture set with posting to the sandbox (T-0.1); baseline stored (T-0.2); adversarial set in CI (T-0.5); Security sign-off; Phase 0 review and Phase 1/2 handoff |

## 5. Tooling gaps in this repo

The devenv provides Git, Go 1.27, golangci-lint v2 and the Go dev tools. Phase 0 also needs the tools below. Per `.claude/CLAUDE.md`, each devenv change is proposed for approval, not made silently.

| Need | Used by | Proposal |
|---|---|---|
| Postgres 16 with pgvector | W1, local runs | `services.postgres` with the `pgvector` extension in `devenv.nix` |
| `sqlc`, `goose` | W1 | Add as packages, or as `go tool` entries in `go.mod` (no devenv change needed) |
| `ripgrep` | W5 `grep` tool | Add `pkgs.ripgrep` |
| `universal-ctags`, zoekt binaries | W8 | Benchmark host only, or add to devenv |
| `govulncheck` | W0 CI | `go tool` entry |
| Docker or another container runtime | testcontainers in W1 and W6 | Not in devenv. Needed locally to run the Postgres tests; CI runners have Docker |

## 6. Decisions and spec issues to resolve

1. **Module path.** The spec uses `github.com/acme/exemplar`, and this repo is `koblas/exemplar`. Decide before the first `go.mod`.
2. **Gateway inbound API.**
   - The spec has the harness call the gateway as an Anthropic base URL (`anthropic-sdk-go`, `httputil.ReverseProxy`), yet the gateway must fail over to provider B, which speaks a different API. A plain reverse proxy can't do that.
   - **Recommendation:** the gateway accepts Anthropic Messages requests and passes them through to provider A. For provider B it uses an adapter that translates the subset Exemplar uses: tools, a strict output schema, and system and user blocks. The harness sends a stage header, and the gateway routes on the stage, not on the model name.
   - Pin the subset so the adapter stays small. Choosing provider B (input #4) fixes the adapter's shape.
3. **T-0.5 and false approval.**
   - Auto-approve gates arrive in Phase 5, so in Phase 0 the "false approval" goal passes trivially.
   - **Recommendation:** implement the Stage 1 invariants that don't need the rest of Stage 1 now: any candidate finding, or coverage below 1.0, forces `ineligible`.
   - Add the spec §16.1 property test that no model-derived input changes the Stage 1 result. Then T-0.5 checks something real, and Phase 5 builds on a tested core.
4. **The coverage rule may be weaker than intended.**
   - Spec §6.5 counts a scoped file as examined "when its hunks were included in the cached PR-context block and the agent produced any output." Every file in the diff is in that block. So any agent that answers at all reaches 100% coverage without opening a single file. An injected "nothing to report" then reads as full coverage.
   - Decide in week 2 whether producing any output is enough, or whether coverage needs per-file evidence (a `get_hunk` or `read_file` call, or a violation anchored in the file). The W7 coverage cases depend on it.
5. **Idempotency key inconsistency.** Spec §7 gives the `reviews` key as `(repo, pr, head_sha, config_version, caller)`, while the §4.3 schema adds `attempt`. The migrations follow §4.3; the spec text should be updated to match.
6. **Where Phase 0 runs.** The plan assumes no Kubernetes deployment in Phase 0, with Helm and NetworkPolicies starting in Phase 2. Check this against platform leadership. The alternative is a shared dev cluster from week 3 to rehearse the gateway's egress policy.

## 7. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Provider B or its DPA is late | Failover is tested only against a fake provider; verifier stubbed; Security sign-off slips | Escalate in week 1; build W4 against a provider interface with a fake; keep the verifier behind the same stage routing |
| Sandbox org or App registration needs a long admin process | Blocks T-0.1 and T-0.3 | Request on day 1; prototype the poster against a personal test org meanwhile |
| Monorepo access for the benchmark | T-0.4 slips; `exemplar-search` sizing guessed | Ask the monorepo owners in week 1; fall back to a large public repo of similar shape for early numbers, and label them as such |
| No container runtime locally | Postgres tests skip locally | Install Docker or Podman; CI requires the tests (`EXEMPLAR_REQUIRE_DB=1`) |
| Hand-written rules are too few to exercise the engine | T-0.1 passes but reveals little | Pick rules with clear positives in the fixture PRs; Phase 1 mining replaces them |

## 8. Proposed first PRs in this repo

1. `go.mod`, the directory skeleton, golangci-lint config, CI workflow, and devenv scripts (after approval of the §5 changes).
2. `schema/` plus `internal/schema` with the drift test, and `cmd/exemplar validate`.
3. `db/migrations` (Phase 0 subset), sqlc queries, `internal/store`, River setup and integration tests.
4. `internal/scope`: diff parsing, hunk index, anchor validation with fuzz tests, and glob scope.
5. `internal/gh`: token minting by flavor, the throttled client, and the minimal poster.
6. `internal/llm` plus `cmd/exemplar-gateway`: provider allowlist, stage routing, failover with a fake provider, secret scan, spend tracking, and `model_calls`.
7. `internal/engine`: the harness, tools, output contract, coverage and validation; Lane 2 live, other lanes stubbed.
8. `exemplar replay`, the baseline reviewer, and the adversarial runner in CI.

## 9. Status (2026-10-12)

The Phase 0 code is on `main`. Each acceptance test that the code can run now has its own command. Every remaining item needs an outside input (§2) or a run on real infrastructure.

### Built

| Workstream | Where | State |
|---|---|---|
| W0 Bootstrap | `go.mod`, `.golangci.yml`, `.github/workflows/ci.yml`, `devenv.nix` | Done. CI has never run on GitHub (no remote yet). |
| W1 Data layer | `db/migrations`, `db/queries`, `internal/store` | Done. The tests run against a pgvector Postgres container per package (testcontainers-go via `storetest`); CI requires them. |
| W2 Contracts and config | `schema/`, `internal/schema`, `internal/config`, `exemplar validate`, `examples/exemplar-config` | Done. The sandbox `exemplar-config` repo itself is not created yet (needs the sandbox org). |
| W3 GitHub | `internal/gh`, `exemplar sandbox probe-approval` | Done against a fake GitHub. The apps are not registered (needs the sandbox org). |
| W4 Gateway | `internal/llm`, `internal/redact`, `cmd/exemplar-gateway` | Done. Pluggable providers via a code registry: `claude` (Claude API) and `bedrock` (Claude on Amazon Bedrock, SigV4 or Bedrock API key). Both serve Claude, so there is still no cross-family verifier (see decision 7). |
| W5 Engine | `internal/engine`, `internal/compiled`, `internal/gates` | Done. Lane 2 and the verifier run live through the gateway, and Lane 1 runs ast-grep when installed. Lane 3 is out of scope until Phase 4. |
| W6 Replay and baseline | `internal/replay`, `exemplar replay`, `examples/fixtures` | Done. Only two synthetic fixtures exist; recorded sandbox PRs are needed. |
| W7 Adversarial set | `internal/adversarial`, `exemplar adversarial`, `examples/exemplar-config/evals/_adversarial` | Done: 16 compromised-mode cases (4 vectors × 4 goals) pass. One case is pending and two are live-only. |
| W8 Benchmark | `internal/search/bench`, `exemplar-search bench` | Built, not run. Needs the monorepo and a host with zoekt and universal-ctags. |
| W9 Data handling | `docs/security/phase-0-data-handling.md` | Ready for Security review. |

### Running the acceptance tests

```bash
# T-0.5: no model access needed (also in CI)
go run ./cmd/exemplar adversarial --config examples/exemplar-config --mode compromised
# T-0.5 live: needs a running exemplar-gateway
go run ./cmd/exemplar adversarial --config examples/exemplar-config --mode live --gateway-url "$EXEMPLAR_GATEWAY_URL" --gateway-token "$EXEMPLAR_GATEWAY_TOKEN"
# T-0.1 and T-0.2: recorded sandbox PRs, live model, post to the sandbox PR, baseline in shadow
go run ./cmd/exemplar replay --config <exemplar-config checkout> --fixtures <recorded PRs> --db "$EXEMPLAR_DATABASE_URL" --baseline --post --app-id … --private-key-file … --installation-id … --config-repo <org>/exemplar-config
# T-0.3: once per target ruleset shape, on a sandbox PR with no approvals
go run ./cmd/exemplar sandbox probe-approval --repo <org>/<repo> --pr <n> --app-id … --private-key-file … --installation-id … --config-repo <org>/exemplar-config
# T-0.4: on the benchmark host
go run ./cmd/exemplar-search bench --repo <monorepo url> --work-dir /mnt/ssd/bench --sparse-dirs <dirs> --out bench.json --markdown docs/benchmark.md
```

### Still blocked

| Test or exit item | Blocked on |
|---|---|
| T-0.1, T-0.2 live | Sandbox org with both apps installed, recorded sandbox PRs as fixtures, a running gateway with a provider key |
| T-0.3 | Sandbox org with target ruleset shapes |
| T-0.4 | Monorepo access and a benchmark host with zoekt and universal-ctags |
| T-0.5 live | Running gateway; provider B for a cross-family verifier |
| Security sign-off | §2 inputs #4, #16 and #17 |

### Decisions taken (§6)

1. **Module path:** `github.com/koblas/exemplar`.
2. **Gateway API:** the gateway accepts Anthropic Messages requests on `POST /v1/messages`, plus `X-Exemplar-*` headers for stage, tier, caller, review, rule run and data classes. It routes by stage, never by model name, rewriting `model` per route. Callers authenticate with a shared gateway token sent as `x-api-key`. A second provider plugs in behind `llm.Provider` by translating the subset of the Messages API that Exemplar uses.
3. **Stage 1 core is built now.** Any pre-verification candidate, coverage below 1, degradation, an incomplete stage, or same-family verification forces `ineligible`. A property test covers this, and a structural test keeps model text out of the gate's input. Phase 0 never approves, and the gate trace records why.
4. **Coverage is strict.** A scoped file counts as examined only when the agent called `get_hunk` or `read_file` on it. Diff text sitting in the prompt does not count. This is stricter than spec §6.5's wording, so the spec should be updated to match.
5. **Idempotency key:** the migration follows §4.3 and includes `attempt`.
6. **No cluster in Phase 0:** everything runs in the devenv shell and CI.

### Deviations from the spec to review

- Structured-output schemas are hand-written rather than generated with invopop/jsonschema. They omit length and range keywords; Go enforces those limits after decoding.
- The benchmark drives the zoekt binaries instead of embedding the zoekt library. The library arrives with `exemplar-search` in Phase 2.

### Decision 7: pluggable providers, Claude and Bedrock (2026-10-12)

The gateway's providers are pluggable in code. Each provider is an `llm.Spec` (model family, model-id validation, constructor) registered in `internal/llm/providers.go`. That registry is the hardcoded allowlist, and config can only choose among registered providers. The first two:

- **`claude`:** the Claude API, through `anthropic-sdk-go`. It needs `ANTHROPIC_API_KEY`.
- **`bedrock`:** Claude on Amazon Bedrock, through Bedrock's Messages-API endpoint (`bedrock-mantle`) via the SDK's `bedrock` package. It needs `EXEMPLAR_BEDROCK_REGION` or `AWS_REGION`, plus AWS credentials from the default chain or a Bedrock API key. Model ids are `anthropic.claude-*`, or a cross-region profile such as `us.anthropic.claude-*`.

Both forward the gateway's Messages JSON unchanged, so failover between them needs no translation.

**Consequence for spec §6.8 and §6.13:** both providers serve Claude, so they are one model family. They give availability failover and an AWS-billed path, but not model diversity. Every verified finding is flagged `same_family`, which keeps auto-approve blocked, and Stage 2's two-provider cross-check has no second family. A provider from another family still needs to be registered before Phase 5. Bedrock calls record cost as unknown until AWS's contracted rates are added to `internal/llm/pricing.go`, so the soft ceiling does not count them yet.

### Message Batches through the gateway (2026-10-12; Phase 1 prerequisite)

The gateway serves the Anthropic Message Batches API (`POST /v1/messages/batches`, plus get, cancel and results), so the learning plane's extraction driver can use the SDK pointed at the gateway. Each request in a batch gets the same checks as a single call: data classes, the second secret scan, and routing by stage. A batch goes to the first provider in its stage that supports batches. Only `claude` does; Bedrock has no Message Batches, so batches do not fail over, and the caller retries. Results stream through unchanged. Their usage is booked once per batch to `model_calls` (with `batch_id`) at the 50% batch rate. Batch ownership lives in `model_batches`, so any gateway replica can serve any batch; without a database the records are in memory (single replica only).

### Phase 1 head start: extraction driver (2026-10-12)

`exemplar-learn extract` (`internal/learn/extract`) is the spec §8.2 driver:
- **Input:** it reads `mined_comments` in `pending`, redacts each body and hunk again, and packs three comments per request into Message Batches through the gateway (stage `generator`, standard tier, pinned `claude-sonnet-5-5`, prompt version `extract-v1`).
- **Cache:** results are cached by `input_hash = sha256(version | model | body | hunk)`, length-prefixed, so a repeated input never costs a second call.
- **Failures:** a comment from an errored request, an omitted answer or an invalid answer returns to `pending`, and stops at `error` with its reason after 3 attempts.
- **Monitoring:** acceptance per batch is logged, with a warning outside the 8–20% calibration band.
- **Not yet built:**
  - Ingest, which fills `mined_comments` and needs GitHub access.
  - The 300-comment calibration loop.
  - Kill reasons fed back as negative examples; they will need a new prompt version.
  - The embed job, which fills `extractions.embedding`.

### Phase 1 head start: Python setup (2026-10-12)

`learning/` is a uv project on Python 3.13 for the embed, cluster and rank jobs (spec §2.3), with `uv.lock` committed. devenv now provides Python 3.13 and uv.
- **Contracts:** pydantic models for `rule.schema.json` and `global.schema.json` are generated by `learning/scripts/gen-contracts.sh` (datamodel-code-generator, ruff-formatted). CI fails if they drift from `schema/`.
- **Database:** `exemplar_learning.db` refuses to run against a database older than migration 00003; the Go side owns migrations.
- **YAML:** `exemplar_learning.yamlio` loads YAML with the Go side's semantics, keeping dates as strings.
- **Dependencies:** only pydantic, psycopg and PyYAML so far. numpy, faiss, scikit-learn, igraph and pgvector come with the jobs that use them.

### Phase 1 head start: clustering job (2026-10-12)

`exemplar-learning cluster` (`learning/src/exemplar_learning/cluster`) implements spec §8.4. Migration 00004 adds `cluster_runs`, `clusters`, `cluster_members` and `merge_decisions`; the Python jobs now require migration 4.
- **Merge cap:** at most 2,000 merge pairs per partition per run, an operational limit the spec doesn't set.
- **Partitions:** language-agnostic extractions go only to the `*` partition.
- **Untested at scale:** both have only seen synthetic tests so far, because there are no real embeddings until the embed job runs.

### Phase 1 head start: rank job (2026-10-12)

`exemplar-learning rank` (`learning/src/exemplar_learning/rank`) implements spec §8.5. Migration 00005 adds `candidates` and `candidate_sources`, plus `path_events` and `path_owners`, which ingest will fill.
- **Choices to tune against reviewer judgement:** the recency half-life, the resolution weight, the bot-finding weight and the minimum cluster size are not set by the spec (see `learning/README.md`).
- **Owner:** taken from `path_owners`, because the Python jobs have no GitHub access; ingest must resolve CODEOWNERS per path.
- **Still to build:**
  - the config writer that opens draft PRs for top candidates (§8.7), with the 3-open-PRs-per-team throttle,
  - compile-to-deterministic (§8.6),
  - eval generation (§8.8).

### Phase 1 head start: config writer (2026-10-12)

`exemplar-learn propose` (`internal/learn/propose`, `internal/gh/configrepo.go`) implements the PR side of spec §8.7.
- **Selection:** the highest-scoring `new` candidates, at most 3 open candidate PRs per owner team; the rest stay queued.
- **Draft PR:** each candidate gets its own branch and adds one rule with `status: shadow`. The rule is validated against `schema/` and the config loader before anything is written.
- **Configured at review:** severity is drafted as `suggestion` and `file_patterns` come from the cluster's language, for the owner to adjust.
- **PR content:** links to source comments, doc links, incidents and score factors. Comment text and code never appear, and anything that scans as a secret is refused.
- **Crash safety:** an existing PR from the candidate's branch is reused, so a crashed run never opens a duplicate.
- **Dry run:** `--dry-run` builds and prints drafts with no GitHub access.

Writes use only the config-writer token. Not built yet: auto-generated eval cases and compile attempts in the PR (§8.6, §8.8), and the kill flow (closing with the `killed` label writes `kill_reason`), which belongs with the feedback service.

### Phase 1 head start: eval generation (2026-10-12)

`exemplar-learn evals` (`internal/learn/evalgen`) implements spec §8.8 for candidates; `propose` ships the cases in the candidate PR as `evals/<rule-id>/cases.yaml` plus `patches/`.
- **Positives:** one per source comment whose commit and line range ingest recorded (new `mined_comments` columns, migration 00006).
- **Negatives:** up to 10 merged, CODEOWNER-approved PRs in the same repos that touch an in-scope file and drew no comment from the cluster (`mined_prs`, filled by ingest).
- **Injected violations:** up to 3. The high-tier model proposes a minimal edit to a negative case's file. Code accepts it only if the edit changes something, the diff is at most 15 lines, the file still parses, and `git apply --check` passes on the original. Files that contain a secret are never shown or patched. 10% of synthetic cases are flagged for the owner to spot-check.
- **Parsing is Go-only for now** (`go/parser`). Injection into other languages is rejected rather than left unchecked, until tree-sitter grammars are added (spec §14).
- **Not built yet:**
  - bug-class mutation operators (Lane 3, Phase 4),
  - verifier labels from feedback (Phase 3),
  - incident-derived positives,
  - meeting the eval CI's minimum case count, which a small cluster may not reach without owner-added cases.

### Phase 1 head start: ingest (2026-10-13)

`exemplar-learn ingest` (`internal/learn/ingest`) implements spec §8.1 and fills the tables the later jobs read: `mined_comments`, `mined_prs` and `path_owners`.
- **API:** GraphQL, one repo per run, paging `pullRequests(states: [MERGED, CLOSED], orderBy: UPDATED_AT)` with each review thread's root comment. It uses a new read-only `ingest` token flavor (contents, pull requests and members read; one repo; never the config repo). Queries go through a small `gh.GraphQL` helper over go-github. The spec suggests githubv4 or genqlient; neither was added.
- **Resumable:** `ingest_cursors` (migration 00007) saves the cursor with each page's rows in one transaction. On `RATE_LIMITED`, or when the point budget runs low, the run stops and waits for the reset (or exits with `--once`). Re-ingesting only refreshes rows, so a full re-run creates no duplicates.
- **Before storage:**
  - code pre-filters drop bots (including the Exemplar app via `--bot-logins`), PR-author self-comments, bodies under 25 characters, emoji/LGTM acknowledgements, short `nit:` comments, and suggestion blocks with no prose;
  - comments on excluded paths are skipped (the shared `internal/exclude` rules, now also used by review);
  - bodies and hunks are redacted with gitleaks, and a comment is dropped if a secret survives;
  - logins become HMAC-SHA256 pseudonyms keyed by `EXEMPLAR_PSEUDONYM_KEY`, and the job refuses to run without a key of at least 32 bytes;
  - doc links are kept when their host is on the `--doc-hosts` allowlist.
- **Eligibility:** repos are checked against the exemplar-config snapshot before any query.
- **Line data:** comments keep `originalCommit`, `originalStartLine` and `originalLine` for eval positives.
- **CODEOWNERS:** parsed in-house with doublestar. `path_owners` gets the first owner of the last matching rule. A merged PR is `approved_by_codeowner` when an approver other than the author owns a changed file, directly or through a team; team members are looked up once per run.
- **Limits:** a PR is read up to 100 threads and 100 files. PRs above that are counted as `truncated`, not paged further.
- **Not built yet:**
  - the `pull_request_review_comment` webhook path for keeping current after backfill,
  - reverts and incidents (`path_events`, FR-L1.7) — now built, see below,
  - accepted bot findings (FR-L1.2),
  - the 30-day body purge.

### Phase 1 head start: path events (2026-10-13)

`exemplar-learn path-events` (`internal/learn/pathevents`) fills `path_events` (spec §8.1, FR-L1.7). Rank's incident boost and propose's incident list already read this table.
- **Reverts:** scans each eligible repo's default branch over GraphQL with the read-only ingest token.
  - It starts from `path_event_cursors` (migration 00008), or 365 days back on a repo's first run, and re-scans a 7-day overlap.
  - A commit counts as a revert when it landed through a merged PR titled `Revert "…"` or whose body says `Reverts owner/repo#N`. A merge or squash commit carrying the `This reverts commit <sha>` trailer, or a direct push with the trailer or GitHub's title, also counts.
  - Two cases do not count: a revert of a revert, which re-lands the change, and a trailer on a commit inside an ordinary PR, which only undoes a work-in-progress commit.
  - The paths come from the PR's files, or from the commit's files over REST for direct pushes. The ref is `#<pr>` or the SHA.
- **Incidents:** the incident tool is still undecided (open item 14), so the job reads a tool-neutral JSON Lines feed passed with `--incidents`: `{"id","at","links":[{"repo","pr"}|{"repo","commit"}]}`.
  - A malformed feed is rejected whole.
  - Links into ineligible repos are skipped without a GitHub call.
  - PR files come from `mined_prs` when the PR was ingested, otherwise from GitHub.
  - An exporter for the chosen tool only needs to write this format, and it runs where the incident API key lives (OUT-3).
- **Stored data:** only refs (PR number, SHA, incident id), paths and times. No commit messages, revert reasons or incident text are kept. Re-runs are idempotent.
- **Not built yet:**
  - revert seeds — now built, see below;
  - `path_history` and `merge_safety_score`, which read this table — both now built, see below.

### Phase 1 head start: revert seeds (2026-10-13)

`exemplar-learn path-events` now also writes revert seeds (spec §8.1, FR-L1.7, FR-X11) unless `--no-seeds`.
- **Finding the reverted PR:** a revert's PR body names it (`Reverts owner/repo#N`, same repo only), or the PR is the one that landed the `This reverts commit <sha>` target.
- **Unreviewed hunks:** the job reads that PR's per-file patches (REST) and its review threads. A hunk is reviewed when a human reviewer's comment (not a bot, not the PR author) overlaps its new-side lines. A file-level comment covers the whole file. The spec says "no review comment on the reverted lines"; this check is per hunk.
- **The seed:** each file with unreviewed hunks becomes one `mined_comments` row with `source = 'revert_seed'`, up to 5 files per revert.
  - The body is the revert's title plus its reason with GitHub's boilerplate removed, or "(no reason given)". The diff is the unreviewed hunks, capped at 6 KB.
  - The commit is the reverted PR's head, and the lines are the first hunk's, so eval generation makes the reverted change a positive case (`p-r<id>`).
  - Seeds have no GitHub comment id, so they take stable negative ids hashed from repo, revert ref and path. Batch custom ids write them as `n<abs>`.
- **Data handling:** the same as ingest.
  - Eligibility is checked first, and excluded paths and binary files are skipped.
  - The reason and the diff are redacted, and nothing is seeded when a secret survives.
  - The reverter and the reverted PR's author are pseudonymized, so the job needs `EXEMPLAR_PSEUDONYM_KEY` unless `--no-seeds`.
  - Doc links in the reason are kept.
- **Extraction:** seeds use their own prompt and version (`extract-revert-v1`) and go in their own Message Batch, so each batch's recorded version is exact. The prompt is told not to guess a cause the diff and reason don't support.
  - The 8–20% acceptance alert applies only to calibrated review-comment batches.
  - A batch from a superseded prompt version is still collected under the version it was submitted with.
  - Rank already gives seeds the incident boost.
- **Not built yet:**
  - calibration of the revert prompt;
  - `eval: synthetic` on rules that only revert seeds support (FR-X11);
  - PRs with more than 100 review threads, whose threads are read only up to 100. Hunks in the unread threads may be wrongly treated as unreviewed.

### Head start: merge_safety_score (2026-10-13)

The deterministic score from spec §6.13 is built. `gates.EvaluateStage2` compares it to the Stage 2 bar (see the Stage 2 combination section below), though reviews don't run Stage 2 yet; and the bug lane (Phase 4) will use it for `merge_safety_score < 0.6`.
- **Score** (`internal/gates/mergesafety.go`, pure code):
  - It is 1.0 minus the worst path's penalties (incident, revert, churn, sensitive path), minus the PR's penalties (no tests, new author, force push), floored at 0.
  - Each path's sum and the score are rounded to 6 decimals, so float noise (0.2 + 0.1 > 0.3) can't change which path is worst or flip a threshold. Ties go to the first path in diff order.
  - Every applied term, the effective weights and the input facts are kept in the result.
- **Weights:** `merge_safety.weights` in global.yaml lays its values over the spec defaults term by term. A term left out keeps its default, so deleting a line never quietly removes a penalty; set it to 0 instead. The schema now lists the seven terms, so a misspelled or unknown term fails validation at load, as does a weight outside [0, 1]. The Python contracts were regenerated to match.
- **Sensitive paths:** a repo's `sensitive_paths` globs are checked when the config loads.
- **Facts** (`internal/mergesafety`):
  - incidents and reverts in the last 180 days come from `path_events`;
  - churn is merged PRs per path in the last 30 days from `mined_prs`, not counting this PR, so it is only as fresh as the last ingest. Migration 00009 adds a `(repo, merged_at)` index;
  - `no_tests` is code changed with no test file changed, recognized by common per-language conventions; docs and config count as neither;
  - force pushes come from the PR's `HEAD_REF_FORCE_PUSHED_EVENT` timeline;
  - an author is new when GitHub search finds no PR of theirs merged in the repo before the last 30 days. A login that isn't a plain GitHub login counts as new and is never put into a query.
  - Any fact that can't be measured is an error, never a default, so the score fails closed.
- **Trace:** `engine.Options.MergeSafety` puts the result in the Stage 1 trace (`approval_decisions.stage1.merge_safety`). It stays out of `Stage1Input`, whose plain numbers-and-flags guard test is unchanged.
- **Not built yet:**
  - the orchestrator step that collects the facts before review (no orchestrator worker exists yet);
  - the Stage 2 threshold;
  - bug-lane eligibility.

### Head start: path_history tool (2026-10-13)

`path_history(path)` from spec §6.4 is built (`internal/pathhistory`) and offered to agents when the engine is given a source (`Engine.WithPathHistory`). Without one, the tool isn't listed and calling it fails as an unknown tool.
- **Answer:** a path's incidents and reverts from `path_events`, plus its recent commits from `git log` on a mirror, as of the PR's base commit. Entries are newest first, at most 20. Events take at most 10 entries so commits can't crowd them out, and commits fill the rest.
  - A commit line gives the date, a short SHA and the subject, capped at 200 characters. No author names or emails are returned.
- **Handling:**
  - Subjects are untrusted text and are redacted by the toolbox, like every tool result.
  - The path must be a clean, repo-relative path, and git reads it as a literal path, so `svc/*.go` matches nothing.
  - The revision must be a SHA.
  - Mirrors are found under `<root>/<owner>/<name>`, and names are checked so they can't escape the root.
- **Degraded mode:** with no mirror, an unknown commit or a failed `git log`, the answer has the events only and says it is degraded. That is reported to the model, not raised as an error.
- **Coverage:** calling `path_history` never marks a file as examined; coverage still comes only from `get_hunk` and `read_file`. The prompts are unchanged. The tool's own description tells the model what it does, so existing prompt versions and evals stay comparable.
- **Not built yet:**
  - serving it from exemplar-search — the mirror and `/v1/log` are now built, see below;
  - turning it on in `exemplar replay`.

### Head start: exemplar-search mirror (2026-10-13)

`exemplar-search serve` now runs the system's only git mirror (spec §6.4.1) and its internal API. The zoekt index and the search tools are the next step; they need zoekt and universal-ctags as new dependencies.
- **Mirrors** (`internal/search/mirror`):
  - One full-object bare mirror per repo in scope, at `<root>/<owner>/<name>`. Scope is every eligible, opted-in repo plus its `context.extra_repos`, minus denylisted repos.
  - A fetch updates branches and tags (pruned), points HEAD at the remote's default branch, and records the repo's visibility from GitHub in the mirror's git config.
  - A first clone is built in a temporary directory and renamed into place, so a half-made mirror never serves.
  - Fetches use a context-flavor token per repo, passed to git in the environment and never in argv, URLs or the mirror's config.
  - Fetching happens on relayed pushes (`POST /v1/fetch`), at most once per 30 s per repo with requests in between merged into one, and every 5 minutes as a backstop.
- **API** (`internal/search/api`, plus a client):
  - `snapshot`, `read_file(commit, path, start, end)` (at most 400 lines, files up to 5 MB), and `log` (at most 20 commits, no author identities). Every answer includes the current `snapshot_sha`.
  - Every call names `pr_repo`. Only the PR's repo, and its `extra_repos` in the same visibility tier (public, or private/internal), may be read. An unknown visibility is refused.
  - Paths must be clean and are read literally, and commits must be SHAs. Request bodies are capped at 64 KB, and unknown fields are rejected.
  - A shared bearer token of at least 32 bytes (`EXEMPLAR_SEARCH_TOKEN`) is required.
  - Errors carry codes that the client maps back to the `mirror` errors.
- **Operations:**
  - `/healthz` never depends on GitHub or the mirrors.
  - `/readyz` reports ready when every repo in scope has a mirror, so a restart with mirrors already on disk is ready at once. It fails while draining.
  - On SIGTERM the server drains, shuts down HTTP within 20 s, then cancels fetches.
  - exemplar-config is re-read every minute, keeping the last good snapshot when a reload fails, so a newly denylisted repo drops out of scope without a restart.
- **path_history:** it now gets commits from `pathhistory.Local(manager)` or `pathhistory.Remote(client)`, so workers can ask exemplar-search instead of needing a local mirror.
- **Shared git environment:** `internal/gitexec` holds the isolated git setup (no user or system config, no prompts, token as an HTTP header), now shared by the worker checkout, the mirror and path_history.
- **Not built yet:**
  - the zoekt index and `search_text` — now built, see below;
  - snapshot pinning across replicas;
  - the orchestrator relaying push webhooks;
  - workers' `read_file` falling back to exemplar-search outside the sparse checkout;
  - Kubernetes manifests (StatefulSet, local SSD, NetworkPolicy), which come in Phase 2.

### Head start: zoekt index (2026-10-13)

exemplar-search now indexes each mirrored default branch with zoekt (`internal/search/index`) and serves `search_text` (spec §6.4.1, §6.4.4).
- **New dependency:** `github.com/sourcegraph/zoekt` (spec §14, Apache-2.0), pinned to `v0.0.0-20260911061844-153817f643cd`, the 2026-09-11 commit. zoekt has no tagged releases.
  - It brings about 45 indirect modules (go-git, prometheus, zap, sentry, grpc middleware, among others). go-enry, which zoekt already depends on, is now imported directly.
  - `go-git` is pinned to v5.19.2 to fix GO-2026-6213 and GO-2026-6214 (path traversal via ref names; symlink following). With that, govulncheck reports nothing new.
  - The 14 findings it does report were already on main: Go 1.27.1 stdlib (fixed in 1.27.2), `ulikunitz/xz`, `aws-sdk-go-v2` eventstream and `moby/go-archive`. That means CI's vuln job is failing independently of this change.
  - `wasilibs/go-re2`, which gitleaks uses, moved from 1.9.0 to 1.10.0; the redaction tests pass on it.
- **Generations:**
  - Each build writes a new directory and serves only once complete. The swap is atomic, and the old generation is closed and removed after its in-flight queries finish.
  - Builds are incremental: the new generation starts as hard links to the serving one, and zoekt's delta build re-indexes only changed files. zoekt writes shards and tombstones by rename, so the serving files are never modified.
  - A full rebuild runs after 24 h or 500 deltas.
  - On restart, the newest complete generation of each repo serves at once, and leftover partial builds are removed.
  - The scheduler indexes after each fetch, while the repo is still marked busy, so a fetch can never move the branch during a build.
- **search_text(pattern, path_filter?, lang?):**
  - RE2 patterns of up to 200 characters, a path regexp, and a language; at most 100 results with a 2 s timeout. Results say whether they were truncated and give the commit they came from.
  - Requests may pass the review's pinned snapshot, and answers from another commit are flagged `snapshot_mismatch`.
- **Degraded mode:** with no generation, or an index more than 1 h behind the mirror, `search_text` is answered by `git grep` at the mirror commit and marked `degraded`.
  - The path filter and language are applied to the grep results, language by file name via enry.
  - User input is never git pathspec magic or a flag: patterns go through `-e`, and a leading `:` is refused.
- **Exclusions (deviation to review):** zoekt's git indexer has no external path filter; its only ignore file is `.sourcegraph/ignore`, which delta builds don't support. So excluded paths (lockfiles, minified, `linguist-generated`/`linguist-vendored`, `.exemplarignore`) may be in the shards but are dropped from every result, indexed or degraded. The spec says indexing skips them.
- **Symbols:** need universal-ctags on PATH; production runs with `--require-ctags`. ctags is not in the devenv, so the tests run without symbols, and symbol extraction is untested here.
- **Not built yet:**
  - `find_references` and `dependents`, including the per-language import patterns (§6.4.3); `find_definition` is now built, see below;
  - session affinity across replicas;
  - wiring `search_text` into the worker's agent tools.

### Head start: find_definition (2026-10-13)

`find_definition(name, near_path?)` from spec §6.4.4 is built (`internal/search/index/defs.go`, `POST /v1/find_definition`).
- **Names:** an identifier, or a qualified name (`pkg.Func`, `Type.Method`, `ns::Class`, `Cart#total`). The last segment is searched; the qualifier only affects ranking.
- **Language filter:** the language of `near_path`, widened to its family (C with C++ and Objective-C; TypeScript with TSX and JavaScript; Java with Kotlin).
- **Symbol mode:** when the serving generation has ctags symbols, a zoekt symbol query for the exact name. Results carry the ctags kind and parent.
- **Regex mode** (`degraded: true`): with no symbols, the index is searched for whole-word matches. With no or lagging index, `git grep -F -w` at the mirror commit. Either way, lines are kept only if a per-language definition regex for the v1 languages matches.
  - The regexes run in Go, so they don't depend on the system grep's regex dialect.
  - They are compiled once per query.
  - Statement lines (`return`, `new`, `await`, `if`, …) are rejected, so a call is not taken for a definition.
  - The regexes aim for recall on common forms and are not a parser. Go entries inside `var`/`const` blocks are only found with ctags.
- **Ranking:** qualified-name match first (ctags parent, package directory or module file stem), then shared leading directories with `near_path`, then shallower and earlier paths. At most 10 results. Excluded paths are dropped.
- **Untested here:** ctags isn't installed locally, so symbol mode didn't run. Adding universal-ctags to the devenv and CI would cover it.

### Phase 1 head start: compile to deterministic (2026-10-13)

`exemplar-learn compile` (`internal/learn/compile`) implements spec §8.6 for candidates.
- **Model:** the high-tier generator, via the gateway, gets the candidate's title, description and preferred approach, its language, and up to five redacted positive hunks. It answers with an ast-grep rule, a Semgrep rule (only for taint or metavariable comparison), or `none` when no deterministic rule fits. Prompt version `compile-v1`.
- **Checks in code before anything runs:**
  - the rule must be one YAML document of at most 16 KB;
  - only allowlisted keys are accepted, and `fix`, `fix-regex`, `rewriters`, `paths` and `pattern-where-python` are refused anywhere in the tree;
  - it must contain exactly one rule in the candidate's language.
  - Then id, message and severity are normalized, and the engine validates the rule (ast-grep scan over no files; `semgrep --validate`).
  - Language-agnostic (`*`) candidates are not compiled.
- **Scoring:**
  - Each eval case is materialized from repo@sha into scratch: positives and injected cases get their expected files, with the injected patch applied by `git apply`; negatives get the PR's same-language files from `mined_prs`.
  - Case repos are re-checked against current eligibility, so a repo denylisted since ingest is never read.
  - Scoring is per case: a positive is a hit when a match overlaps the expected lines, and a negative with any match is a false positive. Cases whose files can't be read are reported, not scored.
- **Sandbox:** model-written rules run under `unshare --net --map-root-user` and `prlimit` (2 GiB address space, 120 s CPU). The CLI refuses to run without it unless `--require-sandbox=false`, and it refuses to start unless both engines are installed. Each attempt records the sandbox it ran under.
- **Acceptance:** the spec bar is precision at least the exemplar's and recall at least 90% of the exemplar's. Exemplar scores come from eval CI (§8.9, not built), so attempts are held to `eval_thresholds` (min cases, precision, recall) and record `baseline = thresholds`.
- **Attempts:** every attempt (accepted, rejected, invalid, error) is stored in `compile_attempts` (migration 00010) with its scores and per-case outcomes. A candidate is retried at most 3 times per 90 days, and a scored attempt counts as its quarterly compile.
- **Proposal:** propose drafts a candidate with an accepted compile as `kind: compiled` with `matcher: {engine, rule}`, and the PR body shows compiled vs exemplar scores and the bar.
- **Engines:** `internal/compiled` gains the Semgrep runner (`--metrics=off`, JSON output), engine validation, and the sandbox prefix. Lane 1 in workers can now run Semgrep rules too.
- **Not built yet:**
  - the quarterly compile of active exemplars, which live in exemplar-config rather than the candidates table;
  - "convert to compiled" PRs for candidates whose PR is already open;
  - the exemplar baseline, which comes with eval CI;
  - running against the real ast-grep and Semgrep binaries: the tests use fake engines, and neither binary is installed here.

### Toolchain: Go 1.27.2 and the engines (2026-10-13)

- **Go 1.27.2** (`go.mod` and devenv) clears the 11 standard-library vulnerabilities govulncheck reported in 1.27.1. The devenv channels still ship 1.27.1, so `devenv.nix` builds the official 1.27.2 source release, checked against the sha256 published on go.dev. Drop the override once `go_1_27` is 1.27.2 there.
  - Go is set up directly (GOROOT, GOPATH, `GOTOOLCHAIN=local`) instead of with `languages.go`. That module rebuilds editor tools with the chosen Go, and the packaged staticcheck can't read 1.27.2's export data. gopls and delve come from nixpkgs' own builds.
- **golangci-lint 2.14.0**, in devenv (built with Go 1.27.2) and in CI. 2.13.2 uses x/tools 0.49, which can't read 1.27.2's export data.
- **Engines:** ast-grep 0.45.1, Semgrep 1.172.0 and universal-ctags 6.2.1 are in the devenv. CI's test job installs the same versions and sets `EXEMPLAR_REQUIRE_ENGINES=1`. The real ast-grep and Semgrep runs and symbol-mode `find_definition` now run instead of skipping.
- **Found by running ast-grep for real:** a Go call pattern such as `http.Get($URL)` parses as a type conversion and never matches. The pattern needs `context`/`selector`. The compile prompt now says so, since the engine accepts such a rule as valid and it would only fail on recall.
- **Module vulnerabilities fixed** by bumping `ulikunitz/xz` v0.5.12 → v0.5.17 (via gitleaks), `aws-sdk-go-v2` eventstream v1.6.3 → v1.7.20 (via the Bedrock provider), and `moby/go-archive` v0.2.0 → v0.3.3 (via testcontainers). Two transitive bumps came along: `aws/smithy-go` 1.22.5 → 1.28.1 and `moby/sys/user` 0.4.0 → 0.4.1. Every new version is at least two weeks old. govulncheck now exits 0. `nwaples/rardecode` (via gitleaks' archive support) then went v2.1.0 → v2.4.1 for GO-2025-4020; v2.4.1 is the newest release at least two weeks old. The one finding left is `x/crypto/openpgp`, flagged as unmaintained with no fixed version, which the code never calls.

### Phase 1 head start: /exemplar try (2026-10-13)

`internal/playground` implements spec §8.7's playground and §10.1's `exemplar try <rule.yaml> <pr>`.
- **What runs:** only the draft rules, on the target PR's checkout. The other rules, secret findings and deferred rules are removed after Prepare. The engine runs with caller `playground` (comment) or `cli`. Nothing is posted on the target PR.
- **Access:**
  - The target repo must be eligible in exemplar-config, and the person asking must be able to read it. Otherwise the answer is "no access to org/repo" and nothing runs. The same text is used for every refusal, so a reply never reveals whether a repo exists or why it was refused.
  - A failed access check denies.
  - The CLI uses the caller's own GitHub token ($GITHUB_TOKEN) for the check, the PR's metadata and the clone.
- **Draft config:** a config PR contributes only its changes under `rules/` and `evals/` (`DraftSnapshot`). `global.yaml`, `platform/eligibility.yaml` and `repos/` come from the base branch, so a PR can't widen its own reach by editing the denylist or a repo's settings. Renames count both paths; at most 3 changed rules per run. The CLI adds the rule file at `rules/<category>/<id>.yaml`, replacing a rule with the same id (`RuleFileSnapshot`).
- **Reply:** the rule, target and head SHA; kept and dropped counts and cost; any rule run that didn't finish; then per finding only the path, line range and message, or the drop reason. Code is never quoted:
  - fenced code and inline code longer than 40 characters, or containing code characters, is replaced with "(code omitted)";
  - messages are secret-scanned and capped at 300 characters;
  - `@` is followed by a zero-width space, so a model-written message can't mention anyone.
- **Comment flow:** `playground.Handler` handles `/exemplar try org/repo#N` (or a PR URL) at the start of a comment line on an exemplar-config PR.
  - Comments from bots, which includes its own replies, and from other repos are ignored.
  - It answers usage errors, validation problems in the draft, and PRs with no rule changes.
  - `GitConfigPR` checks out the config PR and lists changed paths from its diff. `gh.ConfigRepo` gains `PullRequest` and `Comment`.
  - For the access check without a user token, `gh.CanRead` reads the collaborator permission, which includes team and org base permissions, with the app's token.
- **Deviation to review:** §8.7 asks for the commenter's GitHub user OAuth for the access check. A webhook-triggered run has no user token, so the comment flow is meant to use `gh.CanRead`. It fails closed and may deny some readers of internal repos who are neither collaborators nor org members.
- **Not built yet:**
  - wiring `Handler` to `issue_comment` webhooks, which needs exemplar-webhook or exemplar-api;
  - per-user budgets and rate limits on tries;
  - persisting tries, which `exemplar transcript` needs;
  - `/exemplar eval`.

### Phase 1 head start: eval CI (2026-10-13)

`internal/evalrun`, `exemplar eval`, and the `eval` job in `examples/exemplar-config/.github/workflows/eval.yml` implement spec §8.9 steps 1–3 and 5. Step 5 (the adversarial set in compromised mode) was already in that workflow.
- **Evaluation:**
  - Compiled rules run on the engine over each case's files at repo@sha, with synthetic patches applied by `git apply`.
  - Exemplar rules run through the full lane and verifier (caller `eval`), with only the evaluated rule in scope.
  - An eval case is a commit, not a PR, so an exemplar rule reviews a synthesized PR that adds the case's files. It sees whole files rather than the original hunk, and context tools see only those files.
  - Scoring is per case: a positive or synthetic case is a hit when a kept finding overlaps an expected range, and a negative with any kept finding is a false positive.
  - Cases that can't be scored (unreadable files, ineligible repo, no file in scope, a run that didn't finish) are reported, not counted.
- **Gate:** precision, recall and F1 are reported for historical and synthetic cases separately, and the combined numbers gate on `eval_thresholds` (min cases, precision, recall). A changed rule without `cases.yaml` fails. Killed and candidate rules aren't evaluated.
- **Which rules:** `--changed-since <base>` evaluates the rules whose files, or `evals/<id>/` contents, the PR changes. Eligibility comes from that base revision, so a PR can't un-denylist a repo to evaluate on it. Output is JSON (`--json`) and a Markdown table (`--markdown $GITHUB_STEP_SUMMARY`); the command exits non-zero below the thresholds.
- **`paths` on eval cases:**
  - A new optional field names the files a case runs on; it defaults to the expected paths.
  - Negatives need it, since their `expect` is empty. evalgen now records each negative's in-scope files (up to 10) in `candidate_eval_cases.paths` (migration 00011), and propose writes them to `cases.yaml`.
  - The compile job uses them too, and falls back to `mined_prs` for older cases.
- **Deviation to review:** §8.9 calls exemplar-api `/v1/evaluate` with an OIDC token so the repo holds no long-lived secret. Until exemplar-api exists, the workflow runs `exemplar eval` itself with repository secrets: `EXEMPLAR_GATEWAY_URL`/`TOKEN`, and `EXEMPLAR_EVAL_TOKEN`, a read-only token for case repos.
  - Without a gateway, exemplar-rule evals fail closed.
  - Anyone who can push a branch to exemplar-config could edit the workflow to read those secrets, so `.github/` needs CODEOWNERS and branch protection until the move to OIDC.
- **Not built yet:**
  - the volume-estimate and linter-overlap checks — now built, see below;
  - verifier calibration — now built, see below; Stage 2 calibration waits for the Stage 2 categorizer;
  - the `export:` block approval rule — now built, see below;
  - the nightly golden set.

### Phase 1 head start: volume estimate and linter overlap (2026-10-13)

`internal/precheck` with `exemplar volume` and `exemplar linter-overlap`, plus `volume` and `linter-overlap` jobs in the example exemplar-config workflow, each its own check (spec §8.9).
- **Volume estimate (§6.10):**
  - **Trigger:** runs for rules a config PR adds (new and active or shadow) or activates (status becomes active). Tweaks to an active rule don't trigger it.
  - **Run:** each such rule runs alone (`playground.Runner.RunPR`, caller `eval`) on up to 10 PRs merged in the last 30 days in each eligible, opted-in repo that doesn't disable it. PRs whose changed files don't match its `file_patterns` count as untouched. At most 40 runs in total bound the model cost, and when the cap is hit the report says so.
  - **Counting:** kept findings are counted after the comment budget (per file and per PR; Security and blocking are exempt).
  - **Report:** PRs sampled, in scope, commented on (and share), comments per PR, the most on one PR, and cost. It's informational: the check fails only if a run fails.
  - **Deviation:** the spec samples the replay harness's stored (repo, base, head) tuples. No production reviews are stored yet, and CI has no database, so PRs come from GitHub (`gh.MergedPRs`) with the read-only eval token.
- **Linter overlap (§8.7):**
  - **Check:** for each changed rule's positive history cases, the annotations that the repo's configured linters (`linters:` in `repos/<org>/<repo>.yaml`) reported on that commit are compared with the expected lines (`gh.Annotations`). Any overlap fails the check, naming the linter, its rule, path and line.
  - **Skipped:** synthetic cases (no CI ran on them), negatives, repos without linters, and commits whose runs are gone are listed as skipped. Eligibility comes from the base branch.
  - **Deviation:** the spec says "trial run of the repo's linters". This reads what the repo's CI already reported on those commits, the source linter dedupe uses (§6.10), instead of re-running linters outside each repo's build and configuration. The eval token needs `checks: read` on case repos.
- `playground.Runner` gains `RunPR`, the shared "run only these rules on this PR" step used by `/exemplar try` and the volume estimate.

### Phase 1 head start: export approval and summary lint (2026-10-13)

Spec §8.9 and §10.2: exported summaries reach every coding agent in the org, so changing one needs `@platform/devex`, and summaries are linted.
- **Summary lint** (`config.LintExportSummary`): runs when config loads, so `exemplar validate`, CI and every service reject a bad summary. A summary must be at most 200 characters on one line, one sentence (abbreviations such as "e.g." don't count as breaks), with no URL. It can't contain a denylisted instruction phrase ("ignore", "disregard", "always run", "fetch", "download", "curl", "install", "approve", "skip the review", "previous instructions", "system prompt", …). `export.agents: true` with an empty summary is an error.
- **Approval check** (`exemplar export-approval`, `precheck.CheckExportApproval`):
  - **Scope:** every rule's `export:` block is compared between the PR's base and head. Additions, removals and edits all count, and moving or renaming a rule file doesn't hide a change.
  - **Pass condition:** with any change, the check passes only if a member of `@platform/devex` other than the PR author approved the PR's current head commit.
  - **Which reviews count:** each reviewer's latest decisive review, as on GitHub: a later changes-requested or dismissal replaces an approval, comments don't, and bots never count. An approval of an earlier commit doesn't count, since the summary may have changed after it.
  - **Errors:** a failed membership lookup is an error, never a pass.
- **Workflow:** `.github/workflows/export-approval.yml` in the example exemplar-config runs on PR opened, synchronize and reopened and on review submitted, edited and dismissed. That's a separate file, so review events don't rerun or skip the eval jobs.
  - Reviews are read with the workflow's `GITHUB_TOKEN`. Team membership needs `EXEMPLAR_TEAMS_TOKEN` (members: read), which `GITHUB_TOKEN` can't have.
  - The example CODEOWNERS now gives `/.github/` to `@platform/exemplar` and `@platform/devex`, because a PR that edits the workflows could otherwise switch these checks off.
- The rule owner's approval stays with CODEOWNERS, which already requires it for every rule file.

### Phase 1 head start: verifier calibration (2026-10-13)

`internal/calibrate` and `exemplar calibrate` implement spec §6.8's calibration gate (PRD FR-X9).
- **Method:** each labeled finding's PR is checked out, and its candidate is verified alone through the real verification stage (`engine.VerifyOnly`: same prompt, per-file batching, evidence resolution and importance threshold), with no generator. The verdict is all that counts.
  - The run fails unless the verifier refutes at least `known_wrong_refuted_min` of known-wrong findings and at most `known_true_refuted_max` of known-true ones (`eval_thresholds.verifier`).
  - An `uncertain` verdict isn't a refutation, so a verifier that hedges on wrong findings fails.
  - A label that can't be replayed fails the run, as does an empty class: a calibration that skips its hard cases would pass too easily.
- **Labels:**
  - **Seed set:** `evals/_verifier/*.yaml` in exemplar-config. A new `verifier-label` schema holds a fixture (synthetic or recorded), the candidate (rule, path, side, lines, claim) and the label `true`/`wrong`. It's validated at config load, with unique ids and a known rule; `exemplar validate` counts the labels. The example config has 4: two true and two wrong `propagate-context` findings.
  - **From feedback:** `verifier_labels` (migration 00012, as in spec §4.3) joins findings, rule runs and reviews, so a finding authors marked Wrong or Fixed replays on the commits it was found on. `unimportant` labels calibrate importance, not refutation, and aren't used.
- **When it runs:**
  - **exemplar-config PRs:** a `verifier-calibration` job runs when `providers.verifier`, the `verifier` settings, `eval_thresholds.verifier` or the labels change (`--only-if-changed-since`).
  - **Nightly:** `nightly.yml` runs it always, adding `verifier_labels` when `EXEMPLAR_CALIBRATION_DATABASE_URL` is set.
  - **This repo:** `.github/workflows/calibration.yml` runs it on changes to the verifier prompt, decision code or model routing (`internal/engine/prompts.go`, `verify.go`, `verifyonly.go`, `internal/llm/**`). It needs the gateway secrets and fails without them rather than letting an uncalibrated verifier change merge.
- **Test support:** `engine.Script` gains `VerdictFor` so tests can script a verifier that refutes some claims and confirms others.
- **Stage 2 categorizer calibration:** see the next section.

### Phase 5 head start: Stage 2 categorizer and its calibration (2026-10-13)

Spec §6.13's two-model low-risk categorization and its calibration gate (§6.8).
- **Categorizer (`engine.Categorize`, `internal/gates/stage2.go`):**
  - The gateway's `categorizer-a` and `categorizer-b` stages (routed from `providers.categorizer.a`/`.b`) each judge the PR on their own. Each sees the PR context only, with no tools, no findings and not the other side's answer. Each answers `{risk: low|medium|high, p_low, reasons[≤5]}` under a constrained schema (prompt `categorizer-v1`).
  - Code enforces what the schema can only describe: the risk enum, `p_low` in [0, 1] and not NaN, and at most 5 non-empty reasons of at most 200 characters.
  - `review_confidence` = min(`p_low`) when both sides say `low`, and 0 otherwise.
  - It is also 0 when either side fails or breaks the schema, or when the gateway reports the same or an unknown model family for both. Stage 2 needs two independent providers.
  - Both calls are recorded as rule runs (`categorizer-a`/`-b`).
- **Calibration (`calibrate.Categorizer`, `exemplar calibrate`):**
  - Each labeled PR is replayed through `Prepare` and `Categorize` only. It counts as approved when `review_confidence` ≥ `eval_thresholds.categorizer.min_review_confidence` (default 0.8).
  - The run fails if more than `known_risky_approved_max` of risky PRs, or fewer than `known_safe_approved_min` of safe ones, are approved. The example config sets 5% and 50% (owner: Security).
  - A side that fails or breaks the schema is an unreplayable label, not a "not approved": otherwise an outage would pass the risky half. Like the verifier, any unreplayable label or an empty class fails the run.
- **Labels:** `evals/_categorizer/*.yaml`, under a new `categorizer-label` schema (fixture plus `safe`/`risky`). They're validated at config load with unique ids, and `exemplar validate` counts them. The example config has 4: a README typo fix and a doc comment (safe), and a debug-header authorization bypass and a swallowed payment error (risky).
- **CLI:**
  - `exemplar calibrate` now runs both stages, or one with `--stage verifier|categorizer`. `--json` holds one report per stage (`{verifier, categorizer}`).
  - The categorizer half is skipped with a note while `global.yaml` has no `providers.categorizer`, as in the example config (both of its providers are Claude). `--stage categorizer` errors instead.
  - With `--only-if-changed-since`, it runs only when `providers.categorizer`, `eval_thresholds.categorizer` or the labels changed. `calibration.yml` also triggers on `categorize.go`, `stage2.go` and the categorizer labels.
- **Test support:** `engine.Script` gains `Categorizer` and `CategorizerFamily`.
- **Not built:** wiring Stage 2 into Review and approval (auto-approve stays off). The CODEOWNERS and branch-rule lookups and the Stage 2 combination are in the next sections. Stored PR labels from feedback, as `verifier_labels` does for findings, are not built either.

### Phase 5 head start: Stage 2 CODEOWNERS and branch-rule lookups (2026-10-13)

Spec §6.13's other two Stage 2 lookups. Both are deterministic, and both fail closed.
- **CODEOWNERS (`gates.CheckOwners`, `Prepared.CheckOwners`):**
  - Every touched path's owners must all be in the new `auto_approve.approvable_owners`. That list holds `@user`, `@org/team` or an email address, compared case-insensitively.
  - Touched paths include both sides of a rename, and excluded and binary files.
  - A path with no owner fails, as does a repo with no CODEOWNERS, since nobody vouches for those paths. An empty set approves nothing.
  - CODEOWNERS is read from the **base** commit (in `Prepare`, through `GitFS`), so a PR can't make itself approvable. GitHub's lookup order applies: `.github/`, then the root, then `docs/`.
  - The trace lists unowned paths and, for each path, the owners that aren't approvable.
- **Shared parser:** `internal/codeowners` now holds the parser that ingest had, which both use. It fixes one difference from GitHub: `docs/*` matches only the files directly in `docs/`, not nested ones.
- **Branch rules (`gh.BranchRules`, `gates.CheckBranchRules`):**
  - The lookup combines classic branch protection with every active ruleset for the base branch (paged), taking the strictest value of each: required approvals, code-owner review, last-push approval, and the union of required checks. Sources (`branch_protection`, `ruleset:<id>`) are kept for the trace.
  - An unprotected branch means "no rules". A 403, 404 or any other failure is an error, and the gate fails on it.
  - The gate fails when:
    - the rules are unknown;
    - a compliance repo requires fewer than 2 approvals;
    - a `native_approve` repo requires code-owner review, which an App's approval can never satisfy (that repo belongs in `merge_policy_check`, per T-0.3);
    - a `merge_policy_check` repo doesn't require `exemplar/merge-policy`;
    - the mode is unknown.
  - Approvals required beyond the bot's are recorded (`bot_approvals`, `human_approvals`, note `human_approval_still_required`) but don't fail the gate.
- **Compliance constants:** `internal/gates/compliance.go` holds `complianceRepos`, empty for now, and `maxBotApprovals = 1`, in code as the spec says. It still needs a CODEOWNERS entry for Security once this repo has one.
- **Token:** a new `gate` flavor, one repo, `Administration: read`, which classic branch protection needs. Rulesets need only metadata. The GitHub App registration must add Administration read (spec §5 lists it for this).
- **Not built:** calling either lookup from a review, and recording them in `approval_decisions` (see the next section).

### Phase 5 head start: Stage 2 combination (2026-10-13)

`gates.EvaluateStage2` combines spec §6.13's four Stage 2 checks into the `approval_decisions.stage2` trace, and `gates.Decide` gives the run's decision.
- **All must hold:** the CODEOWNERS lookup passes, the branch-rules lookup passes, `merge_safety_score` ≥ 0.7, and `review_confidence` ≥ 0.8.
  - A part that wasn't measured (nil) fails, with its own reason (`codeowners_not_checked`, `branch_rules_not_checked`, `merge_safety_not_scored`, `categorization_not_run`). A NaN score fails too.
  - The deterministic checks are listed first. A caller can skip the categorizer once they've failed, to save the model calls, and the trace then also says `categorization_not_run`.
  - The trace keeps each part's result, the score, and both bars actually used.
- **Bars only go up:** `auto_approve.min_merge_safety_score` and `min_review_confidence` can raise the bars for a repo but never lower them.
  - Config load rejects a lower value, and so does `eval_thresholds.categorizer.min_review_confidence`. The gate also takes the larger of the default and the configured value, as a second guard.
  - Categorizer calibration now uses the same production bar (`gates.ReviewConfidenceBar`), so it can't calibrate against a laxer one.
- **Decisions:** `approved` only when Stage 1 is eligible and Stage 2 ran and passed; otherwise `ineligible`.
  - The exception is a compliance repo whose categorizer pair isn't independent (same or unknown family; `Stage2Categorization.not_independent`). It records `blocked_compliance`, since such a repo can never auto-approve, which the spec intends.
  - A failed categorizer call is an outage, not a property of the provider pair, so it stays `ineligible`.
- **Wiring:** see the next section.

### Phase 5 head start: Stage 2 in Review (2026-10-13)

`engine.Review` now runs Stage 2 after Stage 1 passes, and only then (`engine/stage2.go`).
- **Inputs:**
  - **CODEOWNERS:** `Prepared.CheckOwners`, from the base commit.
  - **Branch rules:** the new `Options.BranchRules`, a lookup the caller provides (`gh.BranchRules` with a `gate` token). With no lookup, or a failed one, the trace records `branch_rules_unknown` and the error (`lookup_error`).
  - **Merge safety:** `Options.MergeSafety`, which is already logged in the Stage 1 trace.
  - **Categorization:** two-model, attributed to the review (review id header, caller). It runs **only** when the deterministic checks pass, to save the calls.
- **Outputs:**
  - `Result.Stage2` is the trace; it is nil when Stage 2 didn't run. `Result.Decision()` (`gates.Decide`) gives the decision, and `Verdict` is `APPROVE` only when that decision is `approved`.
  - Replay persists `approval_decisions.stage2` and the combined decision, and reports `stage2_reasons` and `decision`.
  - Categorizer runs are kept out of `rule_runs`, like the verifier's; their cost is in `model_calls` and the review total.
  - The adversarial check also fails on an `approved` decision.
- **Stage 1 still blocks everything:** see the next section; the bug lane is the remaining gap. `TestReviewStage2` reaches Stage 2 by standing in a passing Stage 1 (the `evaluateStage1` seam).
- **Not built:**
  - a production caller that supplies `BranchRules` and `MergeSafety`; replay and eval pass neither, so Stage 2 fails closed there;
  - the poster's `APPROVE` (or the `exemplar/merge-policy` status) and its revocation on push.

### Phase 5 head start: the rest of Stage 1 (2026-10-13)

Spec §6.13's path, author, fork and size checks (FR-A6, FR-A8, FR-H5).
- **Trust boundary kept:** `gates.CheckPolicy` evaluates them from GitHub, git and config facts. Stage 1 gets only the four resulting flags plus the file and line counts, so `TestStage1InputHasNoModelText` still holds. The detail goes in `Stage1Result.policy` (which paths or identities failed).
- **Paths:**
  - Every touched path must match `auto_approve.paths` and none may be denylisted. Touched paths include renames' old side, excluded and binary files. An empty path set fails.
  - The hardcoded denylist is `internal/gates/denylist.go`, matched case-insensitively. It covers the spec's list:
    - `.github/**`, which includes rulesets, instructions and `copilot-instructions.md`;
    - other CI (`.gitlab-ci.yml`, `Jenkinsfile`, `.buildkite/**`, `.circleci/**`);
    - CODEOWNERS anywhere;
    - build definitions;
    - dependency manifests and lockfiles;
    - `AGENTS.md` and `CLAUDE.md` anywhere, and `.cursor/rules/**`;
    - every path in a repo named `exemplar-config`.
  - **Addition beyond the spec:** Exemplar's own review inputs in the target repo (`.exemplar/**`, `.exemplarignore`, `.gitattributes`, `.gitleaksignore`), since merging a change to them alters later reviews.
  - `extra_denylist` can only add.
- **Author (FR-A6):**
  - A human must be on `allowed_authors`, by login or through an `@org/team`. Teams are checked with the new `Options.TeamMember` (`gh.TeamMember`), and only when auto-approve is on for the repo. An unknown membership fails.
  - A bot is detected from account type `Bot` or a `[bot]` login. An agent is detected from a `Co-authored-by` trailer naming a known agent (`knownAgents`; its identity is the email, or else the name).
  - Every bot or agent identity must be listed under `bot_authors`, and every path must be inside that identity's narrower paths.
  - A human PR with an agent co-author needs both the human check and the agent check.
  - A false positive in agent detection (a human named "Claude", say) only blocks auto-approve.
- **Fork and facts:**
  - `PRInfo` gains `AuthorType`, `HeadRepo` and `CommitMessages`. The PR is a fork when `HeadRepo` isn't the base repo.
  - Each fact left empty fails closed (`author_unknown`, `fork_unknown`, `commits_unknown`). Replay, eval, playground and calibration don't fill them, so their Stage 1 always fails.
- **Size:**
  - At most 10 files and 1,000 changed lines. `scope.Hunk` now counts added and deleted lines, and `Diff.ChangedLines` sums them (binary files count 0).
  - `max_files` and `max_changed_lines` can only lower the limits; config load rejects higher values.
- **title_pattern:** recorded as `title_matches`, informational only.
- **Config checks:** config load also validates the `paths`, `extra_denylist` and bot `paths` globs, and that `title_pattern` compiles.
- **Bug lane:** Stage 1 now also requires the forced bug lane to have run to 100% coverage (`bug_lane_not_covered`), as the spec's coverage row says. The bug lane is Phase 4, so **no run is eligible yet**. That reason replaces `stage1_policy_and_stage2_not_implemented` as what blocks auto-approve.
- **Token:** the `gate` flavor now also has Members read, for team lookups.
- **Not built:**
  - the bug lane;
  - a production caller that fills the new PRInfo facts and lookups;
  - fixture fields for them, so replay could exercise a Stage 1 pass.

### Phase 4 head start: the bug lane (2026-10-13)

Lane 3 (spec §6.7) runs in `engine.Review`; classes and gate are in `internal/buglane`.
- **Classes:** a code allowlist of the spec's six (`bug.nil-deref`, `error-handling`, `concurrency`, `resource-leak`, `off-by-one`, `contract`). Each has class guidance and a version, like a rule's SHA.
  - `global.yaml` `bug_lane.classes` can only enable or disable them; config load rejects an unknown id.
  - The example config enables the spec's set, except `bug.contract`, which stays off (see below).
- **Risk gate (`buglane.Decide`):** the lane runs if any trigger holds:
  - a reviewable file matches the repo's `bug_lane.critical_paths`;
  - changed non-test lines in reviewable files reach `min_changed_lines` (80);
  - `merge_safety_score` < 0.6;
  - a touched path had an incident or revert in the last 90 days.
  - It is **forced** for an auto-approve candidate: global and repo auto-approve on, and `CheckPolicy` passing. That check now runs before the lanes (the spec's §6.3 pre-check).
  - The merge-safety and 90-day facts come from `Options.MergeSafety`. When it's missing they're recorded as `unmeasured` and don't trigger the lane by themselves.
  - The gate decision is `Result.BugLane`, and replay persists `reviews.bug_lane`.
- **90-day fact:** `RecentPathEvents` now returns each path and kind's latest time. The collector sets `PathFacts.Recent` (`incident_or_revert_90d`), which isn't a merge-safety term.
- **Agents:**
  - One per enabled class, over the reviewable files, on the high tier, with the shared harness, tools and coverage (`get_hunk` on every file).
  - The system prompt forbids style, naming, performance-preference, missing-test and "consider" comments.
  - The output schema requires `trigger` and `impact`. An empty or overlong one fails the answer (`schema_error`), so a finding without a concrete trigger never becomes a candidate. The finding message carries both.
  - Findings are `issue (bug)`, severity important.
- **Verification:** bug candidates go through the verifier like Lane 2's, with `bug_lane.importance_min` (0.6) as their bar.
- **Shadow:** bug findings are stored, never posted, as the roadmap starts the lane. They still count as Stage 1 candidates.
- **Shedding:** classes share the review's concurrency slots and are queued after Lane 2. Once any call reports the budget past `ok`, the classes not yet started are recorded `shed`, which marks the run degraded and lowers coverage.
- **Stage 1:** `bug_lane_not_covered` clears when every bug-class run is `ok` at full coverage.
- **bug.contract:** needs `find_references`, which agents don't have yet, so when enabled it's recorded `degraded_skip`. That keeps the bug lane uncovered and blocks auto-approve, as the spec says for a degraded exemplar-search.
- **Test support:** `engine.Script` gains `Budget` and `Importance`.
- **Not built:**
  - `find_references` for agents, and so `bug.contract`: built, see the next section;
  - per-class eval sets and metrics, and the bug-class mutation evals;
  - posting bug findings on critical paths, the step after shadow;
  - a production caller that passes `MergeSafety`.

### Head start: find_references and dependents (2026-10-13)

Spec §6.4.3–6.4.4's import-scoped search, in exemplar-search and as an agent tool.
- **Package scoping (`internal/search/index/langs.go`):** each language gets a package identity and an importer rule.
  - **Go:** the nearest `go.mod` module plus the directory.
  - **Python:** the dotted module, also without its first one or two segments, since import roots aren't known. Matches `from M import`, `import M` and `from parent import leaf`.
  - **TS/JS:** the path without extension; an `index` file is also its directory. Relative specifiers are resolved against the importer; others match on their last two segments, as in the spec's example.
  - **Java/Kotlin:** the `package` declaration, plus imports of the class or `pkg.*`.
  - **C/C++:** the header, as its own path, relative to a `context.include_roots` entry, or relative to the including file. A source file uses the same-stem headers beside it, so a header kept elsewhere isn't found from its source file.
  - Each rule is a coarse pattern valid as both RE2 (index) and POSIX ERE (degraded git grep), plus a precise Go check of each candidate line.
  - Other languages fall back to directory proximity: the defining directory's subtree, flagged `proximity`.
  - Patterns live here rather than in the spec's `internal/ctxprov/langs/`. Tests cover each language in indexed and degraded mode, not golden files per language.
- **`Dependents(path, depth)`:** importing files and packages, repeated through importers up to depth 2, at most 200 packages.
- **`FindReferences(name, defined_in)`:**
  - Scope: a whole-word search over the defining package's directory (its direct files) plus the dependents' files.
  - Ranking: by shared path prefix with `defined_in`, the same directory first.
  - Limits: at most 50 sites (25 in degraded mode), each with two lines of context either side, read from the mirror. Without `defined_in` it is refused for names with more than 500 repo-wide hits.
  - Exclusions apply. A top-level package is searched repo-wide and filtered.
  - Both functions honor the repo's `context.dependents_depth`.
- **Degraded mode:** with no serving generation, or one over an hour behind, everything runs on git grep at the mirror commit. The new `mirror.GrepIn` takes literal pathspecs, so scoped files can be passed safely. Answers carry `degraded: true`.
- **API:**
  - New endpoints `POST /v1/find_references` and `/v1/dependents`, with the usual scope and visibility checks and snapshot-mismatch flag. Client methods: `FindReferences`, `Dependents`.
  - `/v1/snapshot` now also reports whether the repo's search is degraded (`SnapshotStatus`).
- **Agents (`engine/references.go`):**
  - `Engine.WithReferences` (an `*api.Client`) offers a `find_references` tool. `Review` pins the review to the repo's snapshot first, and each call passes the pin. If exemplar-search is unreachable when pinning, the tool isn't offered.
  - **PR overlay:** index hits in files the PR touches are dropped, and the PR's reviewable files are searched at the head instead; their hits come first.
  - Output is redacted like every tool result. A degraded or failed answer sets `RunResult.ContextDegraded`.
- **bug.contract:**
  - It now runs when the review has a usable, non-degraded pin.
  - It is `degraded_skip` when exemplar-search isn't configured, is unreachable, or is degraded at pin time, or when any answer degrades during the run. Any of these blocks auto-approve, as the spec says.
  - The example config keeps it off until the worker is wired to exemplar-search.
- **Not built:**
  - a production worker that constructs the engine with `WithReferences`, and `WithPathHistory`, which no command wires either;
  - recording the pin in `reviews.search_snapshot`;
  - a `dependents` tool for agents (the API exists);
  - preferring the replica whose index matches the pin.

### Head start: the production worker (2026-10-13)

`cmd/exemplar-worker` and `internal/worker` review live PRs. The spec's orchestrator, worker and poster (§5) run as one process for now.
- **Job:** `store.PRReviewArgs` (`pr_review`) on the River queue `reviews`. It carries the installation, repo, PR, head and attempt, and is unique on those args, so a redelivered webhook is a no-op. It is retried up to 3 times and times out after 30 minutes; a malformed job is cancelled.
- **One review (`Worker.Review`):**
  1. Read the PR with a `worker` token. A closed PR stops here. So does a PR whose head isn't the job's: it is superseded, and the newer push has its own job.
  2. **FR-A9:** dismiss Exemplar's approvals on earlier heads before reviewing (`Poster.DismissApprovals`). Only reviews carrying the approve marker *and* from a bot account are dismissed, so a human approval never is, and one on this head (a retry) is kept.
  3. Read the commit messages (`gh.PRCommitMessages`, at most 250) and check out base and head with a clone token.
  4. Prepare, with the PR facts Stage 1 needs: `AuthorType`, `HeadRepo` and the commits.
  5. If the PR is ineligible (kill switch, not opted in, draft), post a neutral check run saying why and stop, as §6.2 says.
  6. Persist the review (`caller=production`) and start the check run `in_progress`.
  7. Collect the inputs:
     - merge safety from the store and GraphQL; if it can't be measured, Stage 2 fails closed;
     - branch rules for the base ref and team membership, through a `gate` token minted only on first use;
     - when `EXEMPLAR_SEARCH_URL` is set, `find_references` and `path_history` through exemplar-search.
  8. Run the engine and persist the result.
  9. If the run is incomplete, complete the check as "Review did not complete" (with Re-run) and post nothing. Otherwise post the COMMENT review and complete the check.
  10. If the decision is `approved` and the repo is `native_approve`, re-read the PR. Only if the head hasn't moved does it submit `Poster.Approve` (APPROVE with an idempotent marker). `merge_policy_check` repos are never approved; they get the `exemplar/merge-policy` check (built later, see below).
- **Shared pipeline:** replay's persistence and posting moved to `internal/pipeline` (`Begin`, `Finish`, `Post`), so live and replayed reviews are written and posted the same way. `Begin` takes the attempt.
- **GitHub:** `gh.PullRequest` gains `AuthorType`, `HeadRepo` and `BaseRef`. `worker.AppClients` mints `worker`, `gate` and `poster` tokens scoped to the one repo.
- **Binary:**
  - `serve` takes `--config` (an exemplar-config checkout kept current by a sidecar, re-read every `--reload`, keeping the last good one on error), `--work-dir` and `--concurrency`. Settings are read from env: `EXEMPLAR_DATABASE_URL`, the GitHub App, the gateway and, optionally, exemplar-search.
  - It runs migrations at startup, like the other binaries.
  - Probes: `/healthz` never depends on dependencies; `/readyz` pings Postgres and turns false on SIGTERM.
  - Shutdown: drain, then River `Stop` (running reviews finish within `--stop-timeout`), then `StopAndCancel`. A cancelled review is retried.
  - `enqueue` inserts a job by hand.
- **Tests:** a fake GitHub covers a posted review with a dismissed old approval, superseded, closed and ineligible PRs, and approval re-checking the head. `TestRiverJobPersists` runs a job through River into Postgres; it is skipped without a container runtime.
- **Not built:**
  - the webhook receiver: built, see the next section; the redelivery reconciler and the 10-minute sweeper are not;
  - cancelling running jobs for an older head with River `JobCancel`: built, see "Superseding running reviews";
  - per-repo concurrency (`queues.per_repo_concurrency`);
  - incremental re-review and carrying findings across heads (fingerprint lookup, dismissed findings, "Addressed in", rebase re-anchoring, retired rules);
  - check-run Re-run handling: built, see the next section;
  - the `exemplar/merge-policy` status;
  - SARIF;
  - the baseline sample;
  - linter waits and dedupe;
  - a config cache with push invalidation;
  - recording `reviews.search_snapshot`;
  - Kubernetes manifests (Phase 2).

### Head start: the webhook receiver and event routing (2026-10-13)

- **`cmd/exemplar-webhook` (`internal/webhook`), spec §5:** no business logic.
  - It requires `X-Hub-Signature-256`; the legacy SHA-1 header alone is refused. It verifies the signature with go-github's `ValidatePayload` against `EXEMPLAR_WEBHOOK_SECRET_FILE`, before parsing.
  - It caps bodies at GitHub's 25 MB, and reads only the event, action, installation and repository from the payload.
  - In one transaction it inserts the raw delivery into `gh_events` (`X-GitHub-Delivery` is the key) and enqueues a `gh_event` job (queue `events`). If the enqueue fails, the event isn't stored either, and a 503 makes GitHub record a failed delivery for the reconciler to redeliver.
  - A redelivery hits the key and enqueues nothing.
  - Installation-less events (`ping`) get 202 and aren't stored.
  - Payloads are never logged. A `TimeoutHandler` keeps answers inside GitHub's 10 s window.
  - It is stateless, with `/healthz` and `/readyz` (Postgres, false while draining), and runs migrations at startup like the other binaries.
- **Routing (`worker.RouteEvent`, `EventWorker` in exemplar-worker), spec §6.2:**
  - These enqueue a `pr_review` for the head: `pull_request` `opened`, `reopened`, `synchronize` and `ready_for_review`, plus an `edited` that changed the base. Drafts are enqueued too; the review decides from `review_drafts`.
  - **Re-run (§6.15):** a `check_run` `requested_action: rerun` or `rerequested` on Exemplar's own check enqueues the head's next attempt, but only if its latest production review is `incomplete` (`LatestReviewAttempt`). Re-running a complete review would post its comments again.
  - Everything else is marked processed and ignored for now: `closed`, labels, feedback events, `push`.
  - Reviews are enqueued in the transaction that marks the event processed, so a retried routing job doesn't enqueue twice. An expired payload (retention) is ignored; an unreadable one is cancelled.
- **Fork PRs (FR-H5), which were not enforced anywhere until now:** `Prepare` makes a PR whose `HeadRepo` differs from the base ineligible unless the repo sets `forks.review: true`. The worker treats a deleted fork (empty head repo) as a fork.
- **Tests:**
  - Signature, header, size and method rejections need no database.
  - Database tests cover store-and-enqueue-once, redelivery, rollback on a failed enqueue, and routing through River including Re-run of complete and incomplete reviews.
  - These Postgres tests, and `TestRealSemgrep`, had been skipping or failing inside Claude Code's sandbox, which blocks the Docker socket. They all pass with `EXEMPLAR_REQUIRE_DB=1` outside it. Running them caught a missing GraphQL answer in the worker test's fake GitHub, on the merge-safety path.
- **Not built:**
  - the reconciler and the sweeper: built, see the next section;
  - `installation` and `installation_repositories` events;
  - feedback events (`pull_request_review*`, `code_scanning_alert`);
  - `issue_comment` slash commands in production (playground has its own path);
  - linter `check_run` dedupe;
  - `push` relays to exemplar-search;
  - superseding running reviews with `JobCancel`: built, see below;
  - the gh_events payload retention job: built, see "Retention" below.

### Head start: the redelivery reconciler and the PR sweeper (2026-10-13)

Spec §5's two webhook safety nets, since GitHub does not retry a failed delivery. Both are River periodic jobs in `exemplar-worker` (queue `maintenance`, one at a time). River runs periodic jobs on its elected leader only, so one replica acts per period; each job is unique per period and runs once at startup.
- **Reconciler (`reconcile_deliveries`, every 5 minutes):**
  - As the App (JWT), it lists the App's delivery attempts from the last 6 hours (`gh.App.HookDeliveries`, cursor-paged, newest first), and groups them by GUID, the `X-GitHub-Delivery` key of `gh_events`.
  - It asks GitHub to redeliver the latest attempt of an event when:
    - no attempt got a 2xx;
    - the receiver didn't store it anyway (it can commit and still time out);
    - it has had fewer than 3 attempts. Past that it is left alone and logged.
  - Redeliveries go to the receiver as usual, whose idempotency key makes a surplus one harmless.
- **Sweeper (`sweep_prs`, every 10 minutes):**
  - It covers the opted-in, enabled, non-denylisted repos (nothing while disabled globally).
  - Per owner, it finds the installation (`RepoInstallation`) and runs one GraphQL `search(type: ISSUE)` for `is:pr is:open updated:>=<now−1h> repo:…`. Repos are batched to fit GitHub's 256-character query limit. It reads up to 1,000 results per query.
  - It enqueues a `pr_review` for every open head with no production review row (`ReviewedHead`). River's per-args uniqueness skips heads already queued or running.
  - It skips drafts in repos without `review_drafts`, and forks (including deleted ones) in repos without `forks.review`, so it doesn't post a "Not reviewed" check on them every period.
  - The one-hour lookback overlaps runs; reviewed heads cost one indexed lookup.
  - One owner's failure is logged, and the rest still run.
- **Token:** a new `sweep` flavor, pull requests read only, which may span one owner's repos but never the config repo.
- **Tests:** the planners are pure and table-tested; the GitHub calls run against a fake; the reconciler and sweeper run against Postgres, the sweeper through River twice.
- **Not built:**
  - a checkpoint across runs (each run re-reads its lookback);
  - alerts on events the reconciler gave up on;
  - sweeping PRs older than the lookback after a long outage (the lookback is a constant; widen it for a manual catch-up).

### Head start: superseding running reviews (2026-10-13)

Spec §6.2: when a PR gets a new head, any queued or running review of an older head is superseded.
- **Routing:**
  - The `pull_request` routes that review a head (opened, reopened, synchronize, ready_for_review, base change) also carry `Supersede{repo, pr, keep: head}`; `closed` carries one that keeps nothing.
  - In the same transaction that marks the event processed and enqueues the new review, the router finds the PR's open `pr_review` jobs for other heads (`store.ReviewJobsToSupersede`) and cancels each with River's `JobCancelTx`. Open means available, pending, retryable, running or scheduled.
  - That lookup queries `river_job` directly on the job args `repo` (case-insensitive), `pr` and `head_sha`.
  - River cancels a running job's context on whichever pod holds it (cause `rivertype.ErrJobCancelledRemotely`); a queued one just never runs. Re-runs supersede nothing.
- **The cancelled review:**
  - Any failure after its review row exists goes through `Worker.failed`. When the context's cause is a remote cancel, it records the review `superseded` (`MarkReviewSuperseded`) and completes its check run as neutral "Superseded" with nothing posted. This runs on a fresh 30-second context, because the job's own is cancelled, so the old head isn't left with an in-progress check.
  - The job then ends as cancelled in River.
- **Last check before posting:** the worker re-reads the PR. A moved head or a closed PR is handled the same way, so no stale comments post even if the cancel hasn't arrived yet. The approval re-check from before still applies after posting.
- **Tests:**
  - The head moves mid-review.
  - A review blocked inside its model call is cancelled through `ReviewJobsToSupersede` and `JobCancelTx` and ends cancelled, `superseded`, with its check completed.
  - Routing: a second push cancels both of the first head's queued jobs, and closing the PR cancels the rest.
- **Not built:**
  - resolving the threads of findings on a superseded head (part of incremental re-review);
  - closing threads when the PR closes.

### Head start: re-review across pushes (2026-10-13)

Spec §6.2's finding lifecycle across a PR's pushes. Every push is still a full review; interdiff scoping comes later (below).
- **Comment ids:** after posting, `pipeline.Post` lists the review's comments and maps each to its finding by path, side, end line and body (in order among equal ones). It records `findings.gh_comment_id`, replacing the old "mapped in Phase 3" placeholder.
- **Finding identity (FR-C2):**
  - Before reviewing, the worker loads the PR's open findings (`OpenFindings`: state `posted`, from production reviews) and passes their fingerprints as `Options.PostedFingerprints`.
  - A finding with one is the same finding. It is stored `dropped` / `already_posted`, not posted again, and not counted against the comment budget; the earlier row stays the open one.
  - The check summary says how many earlier findings still apply.
- **Closing (`pipeline.PlanClosures`, pure; `CloseFindings`), after a complete review of a new head:**
  - **addressed:** the rule ran `ok` at full coverage on the finding's file and the fingerprint is gone or now refuted. Reply "Addressed in \<sha7\>.", resolve the thread (GraphQL `resolveReviewThread`), state `resolved`.
  - **retired (FR-C6):** the rule is killed, shadow or no longer configured for the repo. Reply "Rule `<id>` was retired…", resolve, state `resolved`.
  - **outdated:** the file is no longer part of the PR. Reply, resolve, state `outdated`.
  - Anything the review could not judge stays open: the rule failed, was deferred or skipped the file. So do findings of the head just reviewed (a retry), and everything after an incomplete review.
  - The thread id is found once through `reviewThreads` (the thread whose first comment is ours) and stored in `gh_thread_id`. A thread someone already resolved gets no reply.
  - Failures are logged, not retried, since the review is already posted; the next push tries again.
- **Tests:**
  - A finding followed over four pushes against Postgres: posted with its comment id; found again and not reposted; fixed, then replied to, resolved and recorded; then nothing left to close.
  - Table tests of every closure rule.
  - `CloseThread` against a fake GraphQL.
- **Not built:**
  - **Interdiff scoping:** built, see the next section.
  - **Dismissed findings (FR-C3):** needs feedback events (Phase 3).
  - **Re-anchoring after a rebase (FR-C4):** built, see "Rebase and force-push" below.
  - Resolving threads when a PR closes.

### Head start: interdiff scoping (2026-10-13)

Spec §6.2's incremental re-review. On a new head, an exemplar rule whose scoped files didn't change since the PR's last complete review, and that holds no open finding, carries its result forward instead of re-running.
- **When it applies (`worker.carry`, `PlanCarry`, pure):** all of these must hold, or it's a full review.
  - There is a previous production review of another head with status `done`.
  - It had the **same base commit**: a moved base changes every file's diff.
  - Its head can be fetched (`Workspace.ChangedSince` fetches trees only); after a force-push it may be gone.
  - The interdiff is `git diff --name-only --no-renames old new`, so a rename counts as both paths.
  - Per rule, the previous run was `ok` at full coverage, with the same rule version and exactly the same scoped files.
  - None of those files is in the interdiff or holds an open finding.
  - A rule that doesn't carry re-runs on its whole scope.
- **Only Lane 2 (exemplar) rules carry.** They are where the model cost is. Lane 1, secrets and the bug lane always run; they are cheap, or review the whole PR.
- **Auto-approve candidates always get a full review.** `Review` ignores every offer when the Stage 1 policy pre-check makes the PR a candidate: Stage 1 requires every lane to have *run* at full coverage, and a carried result never saw cross-file changes. Candidates are at most 10 files anyway.
- **A carried run:**
  - It is `RunResult{Status: ok, Coverage: 1, Carried: true, CarriedFrom}` with no model call.
  - It counts its original run's candidates toward Stage 1.
  - It is persisted with `rule_runs.carried_from` (migration 00013), always pointing at the run that did the work, never at another carried run; the findings stay on that run.
  - It never closes an earlier finding, since it re-checked nothing (`PlanClosures` ignores carried runs).
  - The check run says how many rules were carried, and from which head.
  - `rule_runs.context_degraded` is now persisted too; it was left at its default before.
- **Tests:**
  - `PlanCarry` covers every condition.
  - The engine carries without a model call and counts carried candidates; a stale version re-runs; an auto-approve candidate re-runs everything.
  - A worker run over four pushes against Postgres: a README-only push carries both Go rules with no model call, linked to the original runs. A `main.go` change re-runs them. The next README-only push carries from that re-run.
- **Not built:**
  - Re-running a rule on only the changed part of its scope; it re-runs on the whole scope.
  - Carrying across a moved base.

### Head start: rebase and force-push (2026-10-13)

Spec §6.2 FR-C4. When a push rewrites history, open findings are re-anchored before the review.
- **Stored identity:** findings now keep `anchor_code` (the normalized anchor with its two lines of context, exactly what the fingerprint hashes) and `anchor_ordinal` (k). This is migration 00014, set from the fingerprinter's new `identify`.
- **Detecting a rewrite:**
  - For each older head that open findings were posted on, the worker asks GitHub's compare API (`gh.IsAncestor`), because its checkout is shallow and can't tell.
  - `diverged` or `behind`, or a 404 because the old commit is gone, means rewritten.
  - An API error leaves that head's findings alone; the normal closure rules still apply after the review.
- **Re-anchoring (`pipeline.Reanchor`, pure), for findings of rewritten heads:**
  - The same file at the new head (the new base, for a LEFT anchor) is searched for the stored code.
  - Findings with identical code in one file each take the nearest unused match to their old line, in line order. (k counts identical *findings*, not occurrences in the file, so it can't be used as an index.)
  - **A match:** the finding's lines move (`MoveFinding`). Its fingerprint stays valid, since path, code and k are unchanged, so the review that follows doesn't re-post it.
  - **No match, or the file is gone:** outdated, with "No longer present after rebase." and its thread resolved.
  - Findings posted before migration 00014 have no stored code and are left to the normal closure rules.
- **What counts as "the same code":** context counts. Code whose ±2 surrounding lines changed is, by the fingerprint's definition, a different finding: the old one is outdated, and a new one is posted if the rule still finds it. The GitHub comment itself can't move; it shows as outdated on the old commit.
- **Fix:** findings closed by re-anchoring are no longer overwritten in the review's closed count.
- **Tests:**
  - `Reanchor`: moved, unchanged, code gone, file gone, no stored code, and identical code assigned by nearness.
  - `IsAncestor`: ahead, identical, diverged, gone, and an error.
  - Against Postgres: a force-push that shifts the code moves the finding to its new line, with no repost and no reply. One that removes it outdates it, with the reply and a resolved thread.
- **Not built:**
  - Searching other files for code that moved between files in a rebase; that finding is outdated.

### Head start: retention (2026-10-13)

Spec §13.1, FR-H7. A daily River periodic job (`retention`) in `exemplar-worker` applies `platform/retention.yaml` from the current config snapshot. It runs on River's leader in the maintenance queue, once at startup, then every 24 hours.
- **gh_events:** the payload, which holds PR bodies and comment text, is nulled after `gh_events_payload_days` (30). The row stays as the delivery's idempotency key.
- **model_calls:** deleted after `model_calls_days` (90).
- **findings:** deleted after `findings_days` (730), together with their `verifier_labels` in the same statement.
- **approval_decisions:** deleted after `approval_decisions_days` (2555). Their age is the review's `finished_at`, or `started_at`; a decision with neither is kept.
- **Transcripts and feedback events:** not stored yet, so nothing to apply.
- **Mechanics:**
  - Each table is handled in batches of 5,000, at most 200 batches per run; a larger backlog continues the next day.
  - A period below 1 is an error that skips that table. It is never read as "delete everything".
  - One table's failure doesn't stop the others.
  - Migration 00015 adds the indexes the scans use (`CONCURRENTLY`, outside a transaction).
- **Docs:** `docs/security/phase-0-data-handling.md` now says payloads are stored and retained.
- **Tests:** against Postgres, old and fresh rows in each table: expired payloads nulled across batches with the rows kept; old model calls, findings with their labels, and decisions deleted; fresh ones kept; a second run removes nothing; a zero period is refused.
- **Not built:** Security's sign-off of the periods, still pending (spec §17 #17).

### Head start: per-repo concurrency (2026-10-13)

Spec §6.15 "Backpressure" (FR-D6, the last of the degraded-operation requirements FR-D1–D6). At most `queues.per_repo_concurrency` (4) reviews of one repo run at once across every worker replica. Repos in `queues.large_repos`, such as the monorepo, get 32, or the configured limit if that's higher. Per PR, one running review is already enforced by supersession.
- **Why not River queues:** River's queue `MaxWorkers` is per process, so N replicas would allow N×4. Open-source River has no global per-key limit.
- **Slots (`worker.Slots`):**
  - The limit is a set of Postgres advisory-lock slots, `pg_try_advisory_lock` on a key hashed from the lower-cased repo and slot number.
  - A review job holds one on a dedicated pool connection for its whole run. If the pod dies, its connection drops and Postgres frees the slot.
  - A job that finds every slot taken is snoozed for 20 s (`river.JobSnooze`), which costs no attempt. Bursts therefore drain roughly in order rather than strictly.
  - A failed unlock closes the connection rather than returning it to the pool still locked.
- **Pool size:** `exemplar-worker` opens its pool with at least 2×`--concurrency`+8 connections (+12 once late-CI linter checks got their own queue) (`store.OpenWithConns`); a larger `pool_max_conns` in the URL is kept.
- **Tests:** `RepoLimit` (default, large repo, case, a larger configured limit). Against Postgres, slots held from two pools (two replicas) share one repo's limit, other repos are unaffected, and no advisory lock is left afterwards. A job for a repo at its limit snoozes without a single GitHub call.

### Head start: repo-level shadow (2026-10-13)

A repo switch for Phase 2 pilots: `shadow: true` in `repos/<org>/<repo>.yaml`, specified in spec §6.9 item 9. It is added to the canonical spec, since the spec had only rule-level shadow.
- **Why:** setting every rule to `shadow` leaves secret-in-diff findings posting, along with check runs and replies. A pilot must be invisible to developers.
- **Engine:**
  - A finding that would post becomes `shadow` / `repo_shadow`, after the comment budget, so "would have posted" stays measurable for shadow precision. Shadow-rule findings carry no reason.
  - Stage 1 gets `RepoShadow`, reason `repo_in_shadow`, so the decision is never `approved`, Stage 2 and its categorizer calls don't run, and the PR is never an auto-approve candidate (so the bug lane isn't forced).
- **Worker:**
  - In shadow it writes nothing to the PR: no check run, review, comments, thread closures or re-anchoring replies, and no approval. That covers ineligible PRs too: a draft or fork in a shadow repo gets no "not reviewed" check.
  - The review is still persisted, with its gate trace.
  - Revoking an earlier Exemplar approval still happens, so moving a repo into shadow cannot leave an approval standing.
  - `Outcome.Shadow` is logged.
- **Config:** `schema/repo.schema.json` gains `shadow`. The example repo config and the spec's Appendix A example show it, off.
- **Tests:**
  - The engine stores a shadow repo's would-post rule and secret findings as `repo_shadow`, posts none, and records `repo_in_shadow` with no Stage 2.
  - Against Postgres, a shadow review makes no GitHub write but the approval dismissal, stores its finding as `shadow` / `repo_shadow`, and gives an ineligible fork PR no check.
- **Not built:**
  - Moving a repo from shadow to commenting doesn't post its earlier shadow findings; they surface when a later review finds them.
  - Shadow-precision reporting (the Phase 2 exit metric) needs the Grafana boards.

### Head start: linter dedupe (2026-10-13)

Spec §6.10, FR-E7, before posting. A finding that one of the repo's configured linters (`linters` in repo config) already reports on the head is withheld as `linter_duplicate`.
- **Matching (engine):**
  - An annotation of a completed linter check run on the head must overlap the finding's lines in the same file (RIGHT side).
  - It's a duplicate when the annotation is from a linter rule the finding's rule maps to (`linter_map`, new in the rule schema; title is the id, or the message ends with `(id)`), or the light model judges them the same issue.
  - The judge (`linter-judge-v1`, stage `light`) sees both as untrusted data. Its verdicts are cached per pair in the engine, across reviews. At most 20 pairs are judged per review; past that, and on any failure, the finding posts.
  - The check sits after exact dedupe and `already_posted`, before shadow and the budget, so a duplicate takes no budget slot. Stage 1 still counts it as a candidate.
- **Waiting (worker):**
  - The engine asks for annotations only when a finding could post, so a clean review never waits.
  - The worker then re-reads the head's check runs every 10 s until every configured linter has completed, or `linter_wait_seconds` (default 120, now optional so 0 means no wait) has passed, and uses the completed runs.
  - Reading other check runs needs `checks: read`, so it uses the poster's client, the only flavor with checks permission.
  - A failed read logs a warning and dedupes nothing.
  - A shadow repo dedupes too, so shadow precision counts what would have posted.
- **Decisions:**
  - A PR author can shape what CI reports (a workflow in the PR can create a check run with a configured name). So the judge never withholds a Security, blocking or secret finding; those need a mapped id. Added to the spec.
  - Config doesn't say which files a linter covers, so the spec's "no finding in a covered file" skip is approximated by "no finding could post".
  - Check runs are matched by name, not by the app that created them.
- **Python contracts:** `rule.py` regenerated for `linter_map`. `gen-contracts.sh` couldn't run here (macOS `mktemp` and the sandbox), so its exact `datamodel-codegen` command was run from `learning/.venv`; `global_config.py` came out unchanged.
- **Tests:**
  - Engine, table-driven: mapped by title, mapped by message suffix, a map for another linter (judged instead), judged same and different, other lines, other file, a load error, no linters configured. In every case the overlapping secret finding posts, and candidates are unchanged.
  - The judge cache across two reviews. No annotation read when nothing could post.
  - `gh.LinterAnnotations`: pending names a running and a missing linter, and a running run's annotations are not read.
  - Worker: waits through two "in progress" reads, takes an already-reported linter at once, and reads once with `linter_wait_seconds: 0`.
- **Not built:**
  - Late CI: built, see the next section.
  - Open code-scanning alerts as a source.
  - Pinning check runs to the app that creates them.

### Head start: late-CI linter dedupe (2026-10-13)

Spec §6.10, FR-E7. A configured linter whose check run completes after Exemplar posted gets its findings on that head minimized.
- **Routing:**
  - A `check_run` `completed` event for any check other than Exemplar's, on a PR, enqueues a `linter_check` job: repo, PR, head and check, unique by args, on a new `linters` queue (4 workers).
  - The router reads only the payload, so the job decides from config whether the check is one of the repo's `linters` (case-insensitive). Other checks cost one config read.
- **The job (`LinterCheckWorker`):**
  - **Skips** an unconfigured repo, a repo in shadow (it posted nothing), and a check that isn't a configured linter.
  - **Waits** (snoozes 30 s) while a `pr_review` job of the same head is queued or running, so it sees what that review posts.
  - **Compares** the findings production reviews of that head posted and that are still open (`PostedFindingsAtHead`) with the check's annotations. Only completed runs are read; if the check is running again, its later completion handles it.
  - **Matches** through `Engine.LinterDuplicates`, the same matcher as before posting: mapped ids, and the judge with its cache, cap and Security/blocking/secret exemption. A stored finding has no category, so it's taken from the current rule.
  - **For each duplicate:**
    1. Minimize the comment (`minimizeComment`, `OUTDATED`; the node id comes from the REST comment).
    2. Record it as `minimized` / `linter_duplicate` (`MinimizeFinding`, only from `posted`).
    3. Reply: "`<linter>` reports this too, so Exemplar has hidden its comment as a duplicate."
    
    A retry re-minimizes (a no-op) but finds the finding no longer posted, so it never replies twice. A failed reply is logged, not retried.
- **Later pushes:** a minimized finding is no longer open. If a later review finds it again, pre-post dedupe withholds it while the linter still reports it.
- **Pool:** the worker's pool is now at least 2×`--concurrency`+12 connections, for the new queue.
- **Tests:**
  - Routing: a completed CI check routes a linter check; Exemplar's own completed check doesn't.
  - Engine: mapped, judged, a Security rule's stored finding never judged, no overlap.
  - `gh`: `MinimizeComment` and `Reply`.
  - Against Postgres: a review posts while the linter is still running. The check then waits for a queued review of the head, minimizes the comment, records `minimized` / `linter_duplicate`, and replies once. A redelivered check does nothing. An unconfigured check, an unconfigured repo and a shadow repo are ignored.
- **Not built:**
  - Open findings that earlier heads posted aren't compared, since their stored lines may not match this head.
  - Rules from a repo's own `.exemplar/rules` aren't in the snapshot, so their findings get no `linter_map` match (the judge still applies).
  - Code-scanning alerts as a source.

### Head start: human-comment dedupe (2026-10-13)

Spec §6.10, FR-E15. A finding a reviewer already raised on the same lines is withheld as `human_duplicate`. It counts as agreement with the reviewer (a true positive, §9.2), never as a false positive.
- **Comments:** the worker lists the PR's inline review comments (`gh.PRComments`, at most 1,000) with the reader token. Comments GitHub marks outdated (no current `line`) are left out. The engine asks only when a finding could post.
- **Whose comments count:** a `User` other than the PR's author, with `author_association` `OWNER`, `MEMBER` or `COLLABORATOR`. Bots, outside contributors and the author are ignored.
  - **Why:** anyone can comment on a public repo's PR, and an author could otherwise silence findings on their own lines by commenting first.
  - Unlike the linter judge, there's no Security or blocking exemption: a trusted reviewer raising the concern is the point.
- **Matching:**
  - Same path and side, overlapping lines, then the light-model judge (`human-judge-v1`, data classes `diff,pr_text,review_comments`).
  - It sits after `already_posted` and before linter dedupe, so a finding both raised is the reviewer's.
  - At most 20 judged pairs per review. Verdicts are cached per (finding, comment id, comment text), so an edited comment is judged again.
- **Shared judge plumbing:** both judges now go through `Engine.judgeSame`, with the `{"same": bool}` schema and the per-pair cache. It also redacts the context with the secret scanner before sending.
  - This closes a gap from linter dedupe, where annotation messages were sent unredacted (the gateway's layer-2 scan would have refused them).
  - A context that can't be redacted isn't judged, and the finding posts.
  - `AgentSpec.DataClasses` lets a call declare its own data classes.
- **Docs:** `docs/security/phase-0-data-handling.md` gains a row for the dedupe judges.
- **Tests:**
  - Engine, table-driven:
    - a member or collaborator judged same is a duplicate;
    - judged different posts;
    - the author (any case), a bot, a contributor and `NONE` are never judged;
    - other side and other lines aren't compared;
    - a covering range matches;
    - an empty comment is skipped;
    - the declared data classes.
  - A finding both a reviewer and a linter raised is `human_duplicate`.
  - A token in a reviewer comment never reaches the judge.
  - `gh.PRComments`: ranges, single lines, outdated comments left out.
  - Worker: a member's comment on the finding's line withholds it, and an outdated one is ignored.
- **Not built:**
  - Recording which comment a duplicate matched (for the §9.2 agreement event, which needs feedback events).
  - Comments in the PR conversation (not on lines).
  - Pages past 1,000 comments.

### Head start: validated suggestions (2026-10-13)

Spec §6.11, FR-F1–F2. A finding's `suggested_fix` is validated in code and, when valid, posted as a GitHub `suggestion` block that replaces exactly the anchored lines.
- **Who may fix:** rules with `fix_policy: suggest`, and bug classes. The rule task now says "Fixes: allowed" or "not allowed", and the system prompts say a fix is the complete replacement of the anchored lines, with their indentation and nothing around them.
- **Validation** (`engine.checkFix`), after the budget, for findings that post or are stored as shadow:
  - Rejected:
    - fixes on removed lines;
    - fixes over 30 lines;
    - fixes containing a code fence, the redaction placeholder or a detectable secret;
    - fixes identical to the original;
    - fixes that repeat the line above or below the range (an implied edit outside it);
    - an anchor outside the file.
  - Then the replacement is applied to the head file. Go must still parse (`go/parser`) and, when the original was gofmt-clean, stay gofmt-clean (`go/format`). A file that didn't parse before isn't blamed on the fix.
  - Failures are silent: the finding posts without the fix. `findings.fix_valid` records the result, and `Finding.FixProblem` says why.
- **Decision: Go only for now.**
  - The spec names go-tree-sitter grammars for other languages. That's a new cgo dependency, so it needs approval.
  - I tried ast-grep (already a Lane 1 engine) with a `kind: ERROR` rule. It catches some syntax errors, but tree-sitter recovers from missing tokens (an unclosed paren, a dropped semicolon) with MISSING nodes, which ast-grep can't match. It would approve broken fixes, so it isn't used.
  - Fixes in other languages are dropped as `no parser for the file's language`.
- **Posting:** `commentBody` appends the suggestion block after the explanation. The multi-line anchor (`start_line`..`line`) was already posted, so a suggestion replaces the whole range.
- **Tests:**
  - `checkFix`, table-driven:
    - valid (also with a final newline, and a two-line range);
    - bug classes may fix; a `describe` rule may not;
    - left side, over 30 lines, code fence, placeholder, secret, unchanged;
    - the line above repeated, the line below repeated;
    - a parse error, a replacement that breaks the block, not gofmt-formatted;
    - past the end of the file, a missing file.
  - Other languages: no parser. A Go file broken before isn't blamed.
  - A review with a `suggest` rule posts its fix as valid, and the agent was told fixes are allowed.
  - `commentBody` renders the block only for a valid fix (CRLF normalized).
- **Not built:**
  - Other languages: built, see [fix validation in more languages](#head-start-fix-validation-in-more-languages-2026-10-14).
  - Formatter checks other than gofmt (`clang-format`, `prettier`).
  - FR-F3: recording `applied_suggestion` when the next commit contains the suggested text, which needs the feedback-events table.

### Head start: feedback events (2026-10-13)

Spec §9.1, §4. Migration 00016 adds `feedback_events`, as spec §4 defines it, and code now records the first two signals that need no human action in Exemplar.
- **Table:**
  - The spec's columns, with check constraints on `kind`, `role`, `via` and `teach_class`.
  - `observer` added to `role`: §9.1 lists it, §4's comment didn't, and the spec now does.
  - Unique on (finding, kind, actor), so a repeated observation is a no-op (`InsertFeedbackEvent` uses `ON CONFLICT DO NOTHING`).
- **Weights:** `pipeline.RoleWeight` uses `feedback.role_weights` when configured, else author 1, reviewer 2, owner 4, observer 0.
- **`human_agreed`** (FR-B8):
  - When a finding is withheld as `human_duplicate`, the engine keeps the matched reviewer comment (`Finding.HumanMatch`).
  - `pipeline.Finish` records the event for that reviewer: role `reviewer`, `via: inferred`, with the comment id as evidence.
- **`applied_suggestion`** (FR-F3):
  - Before each review, the worker checks the open findings posted with a valid suggestion on an earlier head (`OpenFindings` now returns `suggested_fix` and `fix_valid`).
  - A finding counts when the suggested text is in the head file exactly at the anchored start line, or, when other edits moved it, exactly once within 50 lines and at least 8 non-space characters long.
  - The event is the PR author's, with the head and posting head as evidence. A failure is logged, not fatal.
  - The rule then re-runs without finding it, so the usual closure marks it addressed.
- **Retention:** deleting an expired finding now deletes its feedback events in the same statement (they share findings' 2-year period, spec §13.1).
- **Tests:**
  - `suggestionAt`: at the anchor, moved, absent, a short line away from the anchor (no), a short line at it (yes), past the end, found twice (no).
  - `RoleWeight` defaults and config.
  - Against Postgres:
    - A suggestion posted as a block, committed with a line added above it, is recorded once as the author's `applied_suggestion` (weight 1), and a repeat review adds nothing.
    - A member's same-concern comment records `human_agreed` (reviewer, weight 2) on the `human_duplicate` finding.
    - Retention deletes an old finding's events.
- **Not built:**
  - The other §9.1 signals: links and slash commands, reply classification, reactions, thread resolution, `fixed` without a suggestion, `ignored`, a human raising a finding independently later, and adjudication.
  - Actor roles beyond author and reviewer (owner needs the Teams API at event time).
  - Precedence, `finding_outcomes`, and the weighted roll-ups (§9.2).

### Head start: feedback slash commands (2026-10-14)

Spec §6.12 item 2, FR-B1. A reply `/exemplar <command> [reason]` in an Exemplar review thread records a feedback event on the thread's finding.
- **Commands:**

  | Command | Event |
  |---|---|
  | `fixed` | `fb_fixed` |
  | `wrong` | `fb_wrong` |
  | `not-important` (or `not_important`) | `fb_not_important` |
  | `handled` | `fb_handled` |
  | `disagree` | `fb_disagree` |
  | `uphold` | `adjudicated_uphold` |
  | `overturn` | `adjudicated_overturn` |

  The rest of the comment, up to 1,000 characters, is stored as `reason_text`.
- **Routing:**
  - A `pull_request_review_comment` `created` event whose first line starts with `/exemplar`, that replies in a thread, from a `User`, enqueues a `feedback_command` job.
  - The job runs on a new `feedback` queue (2 workers) and is unique per comment. The pool is now 2×`--concurrency`+14.
- **The job (`FeedbackCommandWorker.Handle`):**
  - **Finding:** looked up by the thread's first comment (`FindingByComment`, production reviews only). Not an Exemplar thread: ignored, with nothing posted in someone else's thread.
  - **Role:**
    - The PR's author is `author`, even if also on the owning team.
    - Otherwise an active member of the rule's owning team (Teams API, gate token) is `owner`; a single-user owner is compared directly.
    - Otherwise someone who submitted a review, an inline comment or a conversation comment before the command (`gh.Participated`, reader token) is `reviewer`.
    - Anyone else gets one reply, and no event.
  - **`uphold` / `overturn`:** need an owner who isn't the PR's author; otherwise one reply.
  - **An unknown command:** one reply listing the commands.
  - **Recording:** the event uses `via: slash` and the role's weight, with the command's comment id as evidence. The command then gets a 👍 reaction (a failed reaction is logged). A repeat of the same command by the same person is a no-op, by the table's uniqueness.
- **Comment footer:** each finding's comment now says "reply `/exemplar fixed`, `wrong`, `not-important`, `handled` or `disagree`", until the signed links exist.
- **Tests:**
  - `ParseCommand`: case, reason on the line and below it, a bare `/exemplar`, a command mid-text (no), the reason cap.
  - Routing: a reply routes; an edit, no command, a top-level comment and a bot don't.
  - Against Postgres, after a real review posts:
    - recorded: the author's `wrong` with its reason, a prior commenter's `fixed` as reviewer, an owner's `disagree` and `overturn`;
    - refused with one reply: a stranger, the author's `uphold`, a reviewer's `uphold`, an unknown command;
    - ignored silently: a thread that isn't Exemplar's;
    - a repeat adds nothing;
    - recorded commands get a reaction;
    - the stored weights are 1, 2, 4 and 4.
- **Not built:**
  - Signed links and `exemplar-api` (§6.12 item 1).
  - Teach-the-bot classification of the reason (`teach_class`).
  - Precedence between an actor's events, and the roll-ups that use them (§9.1, §9.2).

### Head start: feedback roll-ups (2026-10-14)

Spec §9.1 precedence, §9.2 definitions, §12.6 views. Migration 00017 adds the views the dashboards, the owner digest and auto-demotion will read, so they can't disagree about a rule's acceptance.
- **`effective_events`** applies precedence:
  - observers are dropped;
  - each actor's latest signal on a finding wins (Wrong, then Fixed, is Fixed), whether they gave it or it was inferred for them (`applied_suggestion`, `human_agreed`);
  - `resolved` and `ignored` are always kept;
  - once an owner adjudicates, only the latest adjudication counts.

  The spec's sketch referred to this view but didn't define it.
- **`finding_outcomes`:** one row per production finding that was seen (posted, resolved or outdated) or withheld as a `human_duplicate`.
  - It has the §12.6 columns: `seen`, `accepted`, `rejected`, `ignored`, `handled`, `human_agreed`, `neg_w`, `pos_w`, `non_author_negatives` and `owner_overturned`.
  - An adjudication decides the finding: uphold accepts it and is the eFP positive at its weight; overturn rejects it and is the negative.
- **Decisions** (recorded in the spec):
  - `outdated` counts as seen.
  - `minimized` (a late linter duplicate) is left out, since §6.10 says it counts against neither precision nor feedback.
  - Only production reviews count.
  - `react_down` rejects, for acceptance only, never eFP; `react_up` counts for nothing.
- **`rule_weekly_stats`** and **`review_daily_stats`:** materialized, as in §12.6, with unique indexes so a new `refresh_stats` periodic job (maintenance queue, every 15 minutes, `RunOnStart`) refreshes them `CONCURRENTLY`.
  - Two fixes to the sketch's SQL: the eFP ratio is computed in `numeric` (`round` has no double overload), and `review_daily_stats` skips reviews never started.
- **Tests** (against Postgres):
  - Each rule as its own finding:
    - the author's latest signal wins;
    - two actors' signals both count;
    - observers count for nothing;
    - an uphold decides;
    - the latest adjudication decides;
    - a reviewer's agreement on a withheld duplicate;
    - an applied suggestion on an outdated finding;
    - resolution kept beside a later judgment;
    - a late linter duplicate, an eval run and a shadow finding are left out.
  - After the refresh, `rule_weekly_stats` gives 7 posted, 4 accepted, 3 rejected, acceptance 0.667 and eFP 0.438. `review_daily_stats` counts the reviews.
- **Not built:**
  - The Wilson bound and the demotion job (§9.4).
  - Flagging into the owner's adjudication queue, the digest, and the Grafana boards that would read these views.

### Head start: fix validation in more languages (2026-10-14)

Spec §6.11. Approved on 2026-10-14: a tree-sitter dependency, so suggested fixes in languages other than Go are validated and posted.
- **New dependencies** (all tagged releases more than two weeks old; cgo):
  - `github.com/tree-sitter/go-tree-sitter` v0.25.0, the official bindings;
  - grammar modules: `tree-sitter-c` v0.24.2, `-cpp` v0.23.4, `-c-sharp` v0.23.5, `-java` v0.23.5, `-javascript` v0.25.0, `-typescript` v0.23.2 (TypeScript and TSX), `-python` v0.25.0, `-ruby` v0.23.1, `-rust` v0.24.2;
  - one transitive dependency, `github.com/mattn/go-pointer` v0.0.1.
  - `smacker/go-tree-sitter` bundles more grammars in one module, but its last release was in August 2024.
  - `go mod tidy` also adds go.sum lines for go-tree-sitter's own test grammars; they're not built into anything.
- **`internal/syntax`:**
  - `Errors(ctx, path, src)` parses by file extension and counts ERROR and MISSING nodes. MISSING nodes are the zero-width tokens tree-sitter inserts for an unclosed paren or a dropped semicolon, which ast-grep couldn't see.
  - C headers (`.h`) parse as C++.
  - A parse is bounded at 5 s.
  - **Not `ParseCtx`:** in go-tree-sitter v0.25.0 it writes through the parser's cancellation flag, which tree-sitter 0.25 no longer provides, so any context that ends, even after the parse, dereferences nil. A test caught this as a segfault. `Errors` cancels through `ParseWithOptions`' progress callback, the supported path.
- **Fix validation:** `checkSyntax` rejects a fix that adds parse errors (`FixParse`), so errors already in the file aren't blamed on it. A parser failure is `FixParserError`; a language with no grammar is still `FixNoParser`. Go is unchanged.
- **Build:** the worker now needs cgo and a C compiler. Devenv has clang, and GitHub's runners have gcc. A container image (Phase 2 manifests) must build with `CGO_ENABLED=1`. The first build compiles the grammars, which takes about 20 s.
- **Tests:**
  - For each of the ten languages, a clean snippet parses with no errors, and a broken one has at least one, including missing-token cases: an unclosed paren in C, C++, JavaScript, TypeScript, Python, Ruby and Rust; a dropped semicolon in C, C++ and C#; an unclosed class in Java; JSX and TSX.
  - Unsupported extensions.
  - A cancelled context returns `ErrTimeout`, and 50 parses with contexts cancelled right after don't crash (also under `-race`).
  - `checkSyntax`: Python, C and TypeScript valid and broken; an error already in the file isn't blamed; `.zig` has no parser.
- **Not built:** formatter checks other than gofmt; Kotlin, Swift, Scala, PHP, shell and others.

### Head start: auto-demotion (2026-10-14)

Spec §9.4. `exemplar-learn demote --config DIR [--dry-run] [--all]`, a nightly CronJob, moves rules whose recent feedback crosses both bars to shadow.
- **Inputs** (`DemotionStats`): for each rule, its last 30 findings with a judgment in `finding_outcomes`, giving:
  - judged and negative counts (a finding is negative when its negative weight outweighs its positive weight);
  - the summed weights;
  - whether an owner overturned one;
  - distinct non-author people with an explicit negative (wrong, not important or disagree; reactions aren't explicit).
- **Decision** (`demote.Decide`, pure): demote when all of these hold:
  - the full 30 are judged;
  - weighted eFP > `feedback.efp_bar` (0.10);
  - the 95% Wilson lower bound of unweighted precision < `feedback.demotion_wilson_floor` (0.80);
  - an owner adjudication, or at least two non-author people.

  Otherwise it says why not; "author signals alone" is a flag, not a demotion.
- **Action:**
  - Only active global rules are demoted; already-shadow rules, a repo's own rules and bug classes are reported and skipped.
  - It rewrites the one top-level `status: active` line of the rule file on the default branch to `status: shadow`, keeping the rest of the file, comments included.
  - It opens a PR naming the owner, with the numbers and how to re-promote (a human PR), and squash-merges it. If branch protection refuses the merge, the PR stays open (`opened`).
  - The branch is `exemplar/demote/<rule>/<version>`, so a later night finds the PR and does nothing, while a re-promoted rule (a new version) can be demoted again.
- **Why `exemplar-learn`, not a worker job:** it writes to `exemplar-config` with the config-writer token, which spec §5 keeps out of processes that run model agents. `--dry-run` needs only the database.
- **New `ConfigRepo` calls:** `GetFile`, `OpenPR` (ready for review, with its number) and `MergePR` (squash).
- **Tests:**
  - Wilson bounds against published values (30/30 = 0.8865, 24/30 = 0.6269, …).
  - `Decide`:
    - both bars with two people, or with an adjudication;
    - author signals alone;
    - 29 judged;
    - eFP within the bar;
    - the precision bound meeting the floor;
    - configured floor and bar.
  - `shadowed`: plain, quoted with a comment; refuses no active line, two of them, or an indented one.
  - The runner against a fake repo:
    - demotes the active global rule (branch, file, body naming the owner, merge);
    - skips shadow, repo and bug-class rules;
    - does nothing the next night;
    - leaves the PR open when the merge is refused;
    - a dry run writes nothing;
    - a file no longer active is an error for that rule only.
  - Against Postgres, the query counts a reviewer's wrong and an overturn that supersedes the author's wrong, ignores a reaction and an unjudged finding, and keeps only the most recent 30.
- **Not built:**
  - Flagging rules into the owner's adjudication queue.
  - Demotion after 14 days without an owner response.
  - Notifying anywhere but the PR (Slack).
  - Demoting bug classes (they start in shadow).
  - The CronJob manifest.

### Head start: SARIF and alert dismissals (2026-10-14)

Spec §6.12, FR-P. Security-category findings are also uploaded as code-scanning alerts, and dismissing one of those alerts is feedback on its finding.
- **Upload** (`pipeline.UploadSARIF`, after posting, production only):
  - **The log:** SARIF 2.1.0, written with `encoding/json`; the spec names go-sarif, but these few fields don't need a dependency.
    - Driver `Exemplar`, with one rule per rule id carrying its citation as `helpUri`.
    - Level and `security-severity` follow severity: blocking is error/8.0, important warning/5.0, suggestion note/2.0.
    - Each result carries the finding's fingerprint in `partialFingerprints`, so GitHub keeps one alert per finding across pushes.
  - **What's in it:** every Security finding present on the head: posted now, or posted earlier and still open (`already_posted`), on head lines.
  - **Where:** `refs/pull/<n>/head` at the head commit, category `exemplar/`, gzipped and base64-encoded, with the poster token.
  - **Empty uploads:** a PR whose earlier reviews posted a Security finding gets an empty analysis once none remain, which closes its alerts. A PR that never had one makes no call.
  - **Failures:** a 403 or 404 (no code scanning, or no Advanced Security on a private repo) is skipped silently; other failures are logged and the review stands.
- **Dismissals:**
  - **Routing:** a `code_scanning_alert` `closed_by_user` on Exemplar's alert on a PR ref enqueues `alert_dismissal` on the `feedback` queue. Another tool's alert, a non-PR ref and a reopen are ignored.
  - **Finding:** found by PR, rule and file: the one at the alert's line, else the only candidate (the code moved). The alert number is stored in `findings.sarif_alert_number`.
  - **Mapping:** "false positive" becomes `fb_wrong`; "won't fix" and "used in tests" become `fb_not_important`; other reasons are ignored. The dismissal comment is the reason text.
  - **Role:** the PR's author, else a member of the rule's owning team (owner), else a reviewer (dismissing needs security permissions, so never an observer). The event uses `via: code_scanning`.
- **Refactor:** `isRuleOwner` is shared by slash commands and dismissals.
- **Docs:** the data-handling doc gains a code-scanning row (no code or secret in the upload, and it stays in GitHub).
- **Tests:**
  - `BuildSARIF`: posted, still-open and lower-case `security` findings are in; left side, shadow and other categories are out. It checks rules (level, severity, `helpUri`), fingerprints and regions, and that an empty log is valid.
  - `UploadSARIF`: ref, commit, and gzip+base64 round trip; 403 becomes `ErrCodeScanningUnavailable`.
  - Routing: a dismissal routes; a reopen, another tool and the default branch don't.
  - Against Postgres:
    - a pushed secret is reviewed and uploaded as `possible-secret-in-diff` on `config.go`, with the secret absent from the SARIF;
    - the author's "false positive" records `fb_wrong` as author;
    - a security engineer's "used in tests" on a moved line records `fb_not_important` as reviewer;
    - an unknown reason is ignored;
    - the alert number is stored;
    - removing the secret uploads an empty analysis.
- **Not built:**
  - `reopened_by_user` (retracting a dismissal).
  - Polling the upload's processing status.
  - Code-scanning alerts as a linter-dedupe source.

### Head start: Kubernetes manifests and images (2026-10-14)

Spec §2.2, §6.4, §13; Phase 2's deployability. `deploy/README.md` is the operator's guide; this records what was decided.
- **Images:**
  - `deploy/docker/Dockerfile` builds every `cmd/` binary (cgo, for tree-sitter) on `golang:1.27.2-trixie`. It runs them on `debian:trixie-20260918-slim` with git, openssh-client, universal-ctags, util-linux (unshare, prlimit), Semgrep 1.172.0 in a venv, and ast-grep 0.45.1 from its release zip.
  - `deploy/docker/learning.Dockerfile` builds the Python jobs on `python:3.13.15-slim-trixie` with uv 0.12.11 and `uv sync --frozen`.
  - Every base image is pinned by digest, and ast-grep by `ADD --checksum`.
  - The images are amd64 only, run as UID 65532, and use HOME and TMPDIR `/tmp`.
  - No `# syntax=` line, since that would pull an unpinned frontend image; BuildKit's built-in frontend has `--mount` and `ADD --checksum`.
- **Manifests:** plain YAML under `deploy/k8s/base`, rendered by Kustomize. That's the only tool, and kubectl has it built in; no Helm.
  - Namespace `exemplar` with the `restricted` Pod Security level.
  - One service account per binary, none mounting a token, and no RBAC (nothing calls the Kubernetes API).
  - **Workloads:**
    - the webhook (2 replicas, Service, Ingress, PDB);
    - the gateway (2, Service, PDB);
    - the worker (2 × concurrency 4, PDB);
    - search (a StatefulSet of 2 on different nodes, each with a `local-ssd` claim; a headless and a ClusterIP Service; PDB);
    - eight `exemplar-learn` CronJobs and two Python CronJobs, in pipeline order, all `Forbid` with deadlines.
  - **Config:** git-sync v4.7.1 with a read-only deploy key provides the `exemplar-config` checkout. It's a native sidecar for services (the startup probe holds the main container until the first sync) and a one-time init container for jobs.
  - **Grace periods** (shutdown from the source): webhook 30 s (15 s), gateway 40 s (25 s), worker 120 s (80 s), search 45 s (25 s).
  - **NetworkPolicy:**
    - default-deny plus DNS;
    - the webhook in from the ingress controller and out to Postgres only;
    - the gateway in from the worker and learn jobs;
    - search in from the worker;
    - HTTPS and SSH out where the spec lists GitHub, providers or Jira; Postgres on 5432.
  - **Images:** named `exemplar` and `exemplar-learning`. `kustomization.yaml` maps them to all-zero digests, which the release overlay must replace, so an unpinned deploy fails to pull.
- **Decisions:**
  - **The compile CronJob is suspended.** Its sandbox needs unprivileged user namespaces, which RuntimeDefault seccomp at the restricted level normally refuses. The sandbox is never turned off instead.
  - **Egress by host name** (GitHub only, providers only) needs an FQDN-aware CNI policy over the address-based NetworkPolicy.
  - **The App private key in the worker** can mint any token flavor, config-writer included. A minting service would close that. This is recorded as a risk, since a minting service doesn't exist yet.
  - Sizes (worker 1–4 CPU and 2–8 Gi; search 2–4 CPU, 8–32 Gi and 500 Gi SSD) are placeholders until the T-0.4 benchmark.
- **Checks:**
  - `go test ./deploy` (`deploy/manifests_test.go`) loads every manifest the kustomization lists and checks references, hardening, image pinning, ports, probes, grace periods against the source's shutdown deadlines, selectors, NetworkPolicy coverage, the webhook's Postgres-only egress, and that every `EXEMPLAR_*` variable is one the code reads.
  - A mutation (a shorter grace period, a misspelled variable, a writable root) fails it.
  - Locally, OrbStack's kubectl 1.33.9 (`kubectl kustomize`, no cluster contact) rendered 36 resources, and kubeconform v0.7.0 `-strict` for 1.33.0 found all 36 valid.
- **CI:** a new `images` job builds both images (no push), checks the engines are in the image, and renders and schema-checks the manifests.
- **Not built:**
  - The release pipeline that builds, pushes and pins digests.
  - An overlay for a real installation.
  - FQDN egress policies.
  - The token-minting service.
  - Grafana provisioning.

### Head start: Grafana boards (2026-10-14)

Spec §12.4–12.7. Six boards in `dashboards/`, provisioned read-only (`dashboards/provisioning`) against the Postgres read replica through datasource `exemplar-pg`:
- **rule health:** per rule posted, accepted, rejected, ignored, human agreed, acceptance, weighted eFP, judged count, the 95% Wilson bound demotion uses, and overturns, plus weekly acceptance and eFP trends;
- **adjudication:** the owner queue (an author's latest wrong or disagree with no adjudication) per team, with its oldest age, and uphold/overturn by rule;
- **operations:** reviews, incomplete and degraded runs, coverage, cost and duration (labelled informational), rule runs that didn't complete, River queue depth by queue and by repo (FR-D6's "queue depth visible to the platform team"), and model calls by stage and provider;
- **auto-approve audit:** decisions per day, and every decision with Stage 1 reasons, merge safety, Stage 2 and review confidence, linked to the PR;
- **lanes and verifier:** findings by lane and state, held back by reason, verifier refutation by rule, fixes (with, valid, applied), and acceptance by lane;
- **baseline:** Exemplar against the baseline reviewer on the same heads.

Owner and status come from each rule's latest production run, since the rules mirror has no timestamp.
- **Access (§12.7):** aggregates, rule ids, repo names and links only. `go test ./dashboards` refuses a query that selects finding text, reasons, code, fixes, evidence or rule specs; the one exception is counting findings with a fix.
- **Tests:** every board parses with a unique uid and the provisioned datasource. Every panel's SQL, with `$__timeFilter` expanded, runs against a freshly migrated Postgres, and time series select `time` first. It caught an `ORDER BY` over output aliases, which Postgres refuses.
- **Not built:**
  - OTEL-backed panels (webhook-to-first-comment latency, per-stage timing, 429/5xx, GitHub rate limits).
  - The program and counter-metric boards, whose inputs (reverts, incidents, LGTM recall) aren't recorded.
  - Alerting rules.
  - The Grafana deployment itself (dashboards are mounted from this directory).

### Head start: code-scanning alerts as a dedupe source (2026-10-14)

Spec §6.10. Open code-scanning alerts from the tools named in a repo's `linters` (for example `CodeQL`) are linter annotations too.
- **Reading:** `gh.CodeScanningAnnotations` lists the PR's open alerts on `refs/pull/<n>/merge` (where code scanning analyzes PRs), at most 1,000, with the poster token (security events). The tool name becomes the check name and the alert's rule id its title, so `linter_map` keys on the tool name, such as `codeql: [go/request-without-context]`.
- **Filtering:** Exemplar's own alerts, and other tools', don't count. A repo without code scanning (403 or 404) contributes none.
- **Pre-post:** the worker adds the alerts once the check runs are read (after the wait), so the model judge and mapped ids apply to them the same way.
- **Late CI:** a completed check run of a configured tool adds that tool's alerts.
- **Tests:** the helper (ref and state, tool filter, line range, 404); a review whose only match is a CodeQL alert withholds the finding, while an Exemplar alert at the same place is ignored.

### Head start: webhook feedback signals (2026-10-14)

Spec §9.1. Four more signals, from webhooks, as `feedback_signal` jobs on the feedback queue:
- **Thread resolution:** `pull_request_review_thread` `resolved`, by a `User`, records `resolved` on the thread's finding. The role is author, owner, or else reviewer (resolving needs write access). Exemplar resolves its own threads when they're settled, so bot senders are ignored.
- **Replies:** a reply in an Exemplar thread that isn't a command is classified by the light model (`Engine.ClassifyReply`, `reply-classifier-v1`).
  - The prompt sees the finding and the reply as untrusted, redacted data, declared `review_comments`.
  - Agree, disagree and question record `reply_agree`, `reply_disagree` and `reply_question`; other records nothing.
  - The role is author, owner, a reviewer who took part before, or else observer (weight 0, kept for audit).
- **Raised independently:** a new top-level review comment from an owner, member or collaborator who isn't the PR author is compared with the PR's production findings on the same file and side, overlapping lines and made earlier (`FindingsOnLines`: posted, shadow or suppressed; not exact duplicates, bad anchors, already-posted repeats or human duplicates; one per fingerprint).
  - Each candidate goes to the human-comment judge (`Engine.HumanSameConcern`, sharing the dedupe cache), at most 5 per comment.
  - A yes records `human_agreed`, as reviewer or owner.
- **Ignored:** `pull_request` `closed` with `merged` records the PR author's `ignored` on each finding still posted with no feedback event at all.
- **Routing:** review comments that aren't commands now route as reply or comment signals; comments from bots never do.
- **Tests:**
  - Routing: merge, unmerged close, resolved, unresolved, resolved by the bot, reply, top-level comment.
  - Against Postgres, after a real review:
    - the author's agreement, a prior commenter's question as reviewer, a stranger's disagreement as observer (weight 0), and an unrelated reply that records nothing;
    - a reviewer's resolution;
    - a member raising it again (`human_agreed`) and raising something else (nothing);
    - an outside contributor and the author ignored;
    - another thread ignored;
    - merging after feedback records no ignored, while a separate PR left untouched records it once (`finding_outcomes.ignored`), and a redelivery adds nothing.
- **Not built:**
  - Reactions (a GraphQL poll every 30 minutes).
  - `fixed` without a suggestion (a light-model check on later changes to the anchored lines).
  - Teach-the-bot classification of reasons.

### Head start: fixed-without-suggestion and reactions (2026-10-14)

Spec §9.1, §9.2. The last two automatic signals.
- **`fixed`:** after a push closes findings as addressed (the rule ran again and no longer finds them), the worker asks the light model (`Engine.ChangeAddresses`, `fixed-judge-v1`, shared judge cache) whether the change addresses each one.
  - The judge sees the stored `anchor_code` (the code the finding was anchored to) and the head's lines there now (±3).
  - At most 10 per review, skipping findings whose suggestion was applied (already `applied_suggestion`).
  - A yes records the PR author's `fixed`, which makes the finding accepted.
- **Reactions:** a `poll_reactions` periodic job (maintenance queue, every 30 minutes) reads 👍 and 👎 on the first comment of each review thread.
  - It covers every PR with a production review finished in the last 24 hours, with one GraphQL query per PR (`gh.CommentReactions`, paged by threads) and the installation from the repo's latest `gh_events` row.
  - It records `react_up` and `react_down` (`via: reaction`) at `feedback.reaction_weight` (0.5) for the author, a rule owner or a participant, and 0 for an observer.
  - Bot reactors (FR-T4, the bot's own reactions) and other contents are skipped.
  - The merge signal polls once more before it counts ignored findings.
  - `Event.Weight` overrides the role weight for this.
- **Tests:**
  - A hand-written fix (`http.Get` replaced by `NewRequestWithContext`) closes the finding and records the author's `fixed`, accepted in `finding_outcomes`.
  - A committed suggestion records only `applied_suggestion`.
  - Reactions from the author, a participant, a stranger, the bot and a heart record `react_down` author 0.5, `react_up` reviewer 0.5 and `react_down` observer 0, and nothing for the bot or the heart. The finding is rejected with `neg_w` 0, and a second poll adds nothing.
- **Not built:**
  - Removing a reaction doesn't remove its event.
  - Teach-the-bot classification of reasons (`teach_class`).

### Head start: flags and owner-response demotion (2026-10-14)

Spec §9.4, FR-B5. `exemplar-learn demote` now also flags.
- **When:** a rule crosses both bars on author signals alone (`Decision.Flag`).
- **The issue:** one per rule version in `exemplar-config` (title `Exemplar flag: <rule> (<version>)`, label `exemplar-flag`). It names the owner, gives the numbers, and sets a deadline `feedback.owner_response_days` (14) days out.
- **Later nights:**
  - before the deadline, it waits;
  - if a person has commented, or the issue is closed, the owner responded and nothing happens;
  - otherwise the rule is demoted by the same PR (the reason says when it was flagged) and the issue is closed with the PR's link.

  The owner's adjudication queue itself is the `effective_events` query the adjudication board shows.
- **Permissions:** the config-writer token now carries `issues: write` (still `exemplar-config` only), and spec §5's App permissions say Issues read & write. The App registration must ask for it.
- **New `ConfigRepo` calls:** `PRByBranch`, `HumanActivity` (a non-bot comment or review since a time), `FindIssue` (by label and exact title), `CreateIssue`, `CloseIssue`.
- **Tests:** a flag opens with the owner and the deadline (2026-10-28 from 2026-10-14), waits on day 7, counts an owner response on day 15, then with no response demotes, merges and closes the flag. A closed flag counts as a response.

### Head start: rule expiry and owner digests (2026-10-14)

Spec §8.10, §9.4, FR-B5, FR-A5. Two `exemplar-learn` subcommands, with CronJobs: expiry daily after demote, the digest on Mondays.
- **`expiry`:** for each active global rule past `review_by`, the last 90 days of production outcomes decide (`RuleRecentOutcomes`).
  - **Extend:** at least one judged finding and weighted eFP within the bar opens a PR moving `review_by` to `expiry.default_months` (12) from today. Only the owner merges it.
  - **Otherwise:** a PR sets `status: shadow`, saying it merges on its own after `expiry.auto_demote_after_days` (14). A later run merges it unless a person commented, reviewed or closed it.
  - **Repeats:** one branch per rule version and decision. Closed or merged PRs end it.
  - **Deviation:** the spec re-runs the eval on recent cases. This uses recent production feedback, so a rule with no feedback in 90 days goes to the shadow PR, which its owner can close and replace with an extension. Recorded in the spec.
- **`digest`:** one issue per owner and week in `exemplar-config` (`Rule health for <owner>, week of <Monday>`, label `exemplar-digest`). It's found by title, so reruns open nothing.
  - **Rules table:** for each rule the owner has, active or in shadow: status, posted, accepted, effective FP over 28 days, and health. Health is review date passed, being demoted, flagged, over the bar (with too few judged findings to demote), close to the bar (within 5 points, spec §12.5), healthy, or no feedback yet. It reuses `demote.Decide`.
  - **Dismissal reasons:** the top one per rule, with the path prefix (first two segments) when it covers most of them.
  - **Adjudication queue:** the owner's findings waiting, linked to their threads (`#discussion_r<id>`).
  - **Auto-approved PRs** in the last week whose touched paths the owner holds in CODEOWNERS. The Stage 2 trace (`OwnersResult.Owners`) now records every owned touched path's owners.
  - `EXEMPLAR_GH_WEB_URL` sets the link base for GHES.
- **Tests:**
  - Expiry against a fake repo:
    - a healthy rule's extension rewrites only `review_by` (quotes dropped, comment kept on status);
    - a noisy and a quiet rule get shadow PRs;
    - day 7 waits; day 15 with a response leaves them; without one merges both; closed PRs end it;
    - a rule not yet due, or already in shadow, is skipped;
    - a dry run writes nothing.
  - Expiry against Postgres: only the last 90 days count.
  - The digest against Postgres, from seeded findings, events and an approval:
    - two owners' issues with the week's Monday in the title;
    - the exact table rows (75% accepted, 25% eFP, review date passed, no feedback yet);
    - "Not important here" under `tools/gen/**`;
    - the waiting finding's link;
    - the auto-approved PR with the owner's path only;
    - a second run in the same week opens nothing.
  - The deploy test now counts 11 CronJobs; the rendered base (38 resources) is valid under kubeconform `-strict`.

### Head start: teach-the-bot and routing by reason (2026-10-14)

Spec §8.7 (teach-the-bot fast path), §9.3, PRD FR-L6.5.
- **Classification:** when a slash command or a code-scanning dismissal records a rejection (wrong, not important, handled or disagree) with free text, the light model (`Engine.ClassifyTeach`, `teach-classifier-v1`) classes it scope, exception, rule_wrong or noise.
  - The class is stored as `feedback_events.teach_class`.
  - A repeated command that records nothing new keeps the first reason's class.
- **Verifier labels (§9.3 "Wrong"):** every new event goes through `routeLabel`.
  - A wrong becomes a `wrong` verifier label: an author's as `author_signal`, anyone else's as `human_review`.
  - An owner's uphold or overturn is a `human_review` label, `true` or `wrong`, replacing an author signal, never the reverse.
  - Observers don't label.
- **`exemplar-learn teach`** (nightly CronJob) reads `TeachSignals` (each person's latest rejection in 30 days, observers excluded).
  - **Classes:** a reason's class wins. Without one (or noise), "not important here" is scope and "disagree" stays a disagreement.
  - **Threshold:** 3 in 30 days, or one from the owner. Repo rules and bug classes are skipped.
  - **Scope:** a draft PR adds `!<prefix>` to `file_patterns`. The prefix is the first-two-segments prefix most of the scope evidence shares, which must itself reach the threshold and not already be excluded. It edits a flow list or a block list in place.
  - **Exception, rule_wrong, disagree:** an owner review issue (label `exemplar-teach`).
  - **Contents:** finding links and counts, never reasons, since `exemplar-config` is widely readable.
  - **Repeats:** recorded in `teach_proposals` (migration 00018), unique per rule version and class.
- **Deviations** (recorded in the spec): no drafted description amendment or negative eval case for exception, and no computed before/after eval scores (the draft PR's eval CI shows them).
- **Tests:**
  - `excluded`: flow, empty flow, block, block at EOF; refuses a file without the list.
  - Against Postgres:
    - three not-important dismissals under `tools/gen/**` (two with scope reasons, one nested deeper) make a scope PR with exactly that exclusion; one elsewhere and two exception reasons don't reach the threshold;
    - three disagrees make a disagree issue, and one owner rule_wrong makes an issue;
    - an observer's reason counts for nothing;
    - no proposal quotes a reason, and all link to the threads;
    - a second run proposes nothing.
  - Worker tests: the author's wrong is classed scope, a disagree's reason noise, a fixed or an overturn unclassed; the owner's overturn replaces the author's wrong label as `human_review`.

### Head start: signed feedback links and exemplar-api (2026-10-14)

Spec §6.12 item 1, FR-B1. Comments now carry the five feedback links, and a new `cmd/exemplar-api` serves them.
- **Links** (`internal/feedbacklink`):
  - `…/f/<finding>/<reason>?s=<HMAC(finding, reason)>`, with HMAC-SHA256 over a versioned label, at least a 32-byte key, an https base, and constant-time verification.
  - The footer is "Fixed · Wrong · Not important here · Already handled · Disagree with rule", followed by "or reply `/exemplar <reason>`".
  - `exemplar-worker` signs them when `EXEMPLAR_API_URL` and `EXEMPLAR_LINK_KEY_FILE` are set, and only for persisted findings; otherwise comments name the slash commands as before.
- **`exemplar-api`** (`internal/api`):
  - **GET of a link:** without a session, it redirects to `/oauth/start`. That sends the person to GitHub's authorize page with the review app's OAuth client, a signed state (the return path, an expiry, and a nonce held in an HttpOnly cookie), and `allow_signup=false`.
  - **`/oauth/callback`:**
    - checks the state and nonce;
    - exchanges the code, reads `/user` (bots refused), and drops the token;
    - sets a signed 8-hour session cookie (HttpOnly, Secure, SameSite=Lax);
    - returns to the link, which now shows a confirmation form with an optional note (1,000 characters).
  - **A GET never records anything**, since previews fetch links. The POST needs a token bound to the login, finding and reason. It then enqueues `feedback_link` and says thanks.
  - **Pages:** a strict CSP, no Referer (the link holds its signature), no caching. Return paths must start with `/f/`.
  - **Recording:** the server has no installation key. The worker's `FeedbackLinkWorker` finds the finding, its repo and the latest installation, and derives the role: author, owner, a participant before the click, or else observer (a click alone never makes a reviewer). It records `via: link` and classes the note for teach-the-bot.
  - **SSO:** org SSO is the ingress's job (spec §13 IN-2), so no OIDC dependency.
- **Deploy:**
  - `api.yaml`: 2 replicas, a Service, an Ingress for the whole host, and a PDB.
  - A service account and a NetworkPolicy: in from the ingress controller, out to Postgres and HTTPS for GitHub OAuth.
  - The worker mounts only `link-key` from the new `exemplar-api` Secret.
  - Settings gain `EXEMPLAR_API_URL` and `EXEMPLAR_GH_OAUTH_CLIENT_ID`.
  - The rendered base is 45 resources, valid under kubeconform `-strict`.
- **Tests:**
  - The signer: round trip; another finding, reason or key; unknown reason; bad id; empty signature; short key; plain http.
  - The full browser flow against a fake GitHub:
    - a redirect to sign-in, with the client id and redirect URI;
    - the callback sets the session;
    - the GET shows the form and records nothing (headers checked);
    - a POST without the form token, or with one for another reason, is refused; with it, enqueues exactly the login, finding, reason and trimmed note;
    - forged and unknown links are 404s;
    - an expired session signs in again.
  - Sign-in failures: a bad code, a bot account, a foreign state, and open-redirect return paths.
  - The worker against Postgres: the posted comment carries the finding's signed link; the author's click records author with the note classed scope, a participant's reviewer, and a stranger's observer, all `via: link`; a finding retention removed records nothing.
- **Not built:**
  - The engine API (`/v1/evaluate`), the CLI's device flow, and transcripts.
  - A per-identity rate limit.
  - The Phase 6 UI.

### Head start: formatter checks and the CLI release (2026-10-14)

Spec §6.11, §10.1.
- **Formatters:** after the parse check, `checkFormat` runs the file's formatter if it's on PATH, on the original and on the fixed file, in the checkout, with a 10-second bound.
  - A fix fails (`FixFormat`) when the original was clean and the fix isn't. An unclean original, or a formatter error, holds the fix to nothing.
  - **clang-format** (C and C++) reads the repo's `.clang-format` through `--assume-filename`. It's YAML, so reading it runs nothing.
  - **prettier** (JavaScript, TypeScript, CSS, JSON, YAML) runs with `--no-config --no-editorconfig`. Its config can be JavaScript from the PR, which would mean running PR code, so a repo off prettier's defaults isn't checked.
  - The services image and CI now install clang-format. prettier needs Node, so it's used only where installed.
- **CLI release:** `exemplar --version` (cobra; `main.version` set by `-ldflags`).
  - `.github/workflows/release.yml` runs on a `v*` tag pushed by a maintainer. It builds natively on four runners (linux amd64/arm64, darwin arm64/amd64), because cgo, for the tree-sitter grammars, rules out cross-compiling.
  - The build checks `--version` and archives the binary. A `contents: write` job (the only one) creates the GitHub release with the archives and `SHA256SUMS`.
  - **Deviation from §10.1:** no GoReleaser, Homebrew tap or internal mirror yet.
- **Tests:**
  - clang-format with a repo style of IndentWidth 4: a clean fix passes; a squashed one fails; one in the wrong indent fails; an unformatted original holds nothing; a language with no formatter is skipped.
  - `--version` prints `exemplar version dev`, and a build with `-X main.version=v0.1.0` prints it.

### Head start: bug findings on critical paths (2026-10-14)

Spec §6.7, §16 Phase 4 ("bug lane in shadow, then commenting on critical paths").
- **Config:** each `global.yaml` `bug_lane.classes.<id>` gains `post_on_critical_paths` (off by default).
- **Engine:** a bug finding of such a class whose path matches the repo's `bug_lane.critical_paths` (doublestar globs, the same matcher as the lane's gate) goes through the normal path: exact dedupe, already posted, human and linter dedupe, the comment budget, and repo shadow. Every other bug finding stays in shadow, as before.
- **Unchanged:** the bug lane's eligibility and Stage 1's forced-bug-lane rule.
- **Contracts:** `schema/global.schema.json` and the Python `global_config.py` are regenerated.
- **Tests:** a class that posts, on a critical path, posts. The same class off posts nothing, and so does a path that isn't critical.
- **Not built:** demoting a bug class (setting `post_on_critical_paths` back), which `exemplar-learn demote` reports but skips.

### Head start: mutation evals (2026-10-14)

Spec §8.8, FR-X6, §16 Phase 4. Deterministic recall cases for the bug lane, with no model.
- **`internal/mutate`** (Go, on `go/ast`), one operator per class:

  | Class | Operator | Finding expected at |
  |---|---|---|
  | `bug.error-handling` | drop an `if err != nil { … }` right after err is assigned | the assignment |
  | `bug.off-by-one` | `< len(x)` becomes `<= len(x)` | the line |
  | `bug.resource-leak` | drop `defer x.Close()` | where x was acquired |
  | `bug.concurrency` | remove `x.Lock()` and its `x.Unlock()` or `defer x.Unlock()` in one block | the statements that were guarded |
  | `bug.nil-deref` | drop `if x == nil { return … }` | the next statement |

  - Expected lines are in the mutated file's numbering.
  - A mutation is kept only if the result parses, it changes 1 to 15 lines, and the per-operator cap allows it.
  - Diffs are multi-hunk git-style patches, checked with `git apply`.
- **`exemplar mutations --config DIR --checkout DIR --repo owner/name [--sha] [--paths] [--per-file] [--max-per-class] [--dry-run]`:**
  - It walks the checkout's tracked Go files (no tests, no vendor) in the repo's `bug_lane.critical_paths` (or `--paths`).
  - For enabled classes only, it merges new cases (named by operator, path and line) into `evals/<class>/cases.yaml` with `patches/<name>.diff`, and validates against the eval-case schema.
  - Code stays referenced by `repo@sha`; an owner reviews and commits.
- **Eval for bug classes:**
  - The config loader accepts `evals/<bug class>/` for classes in `global.yaml`.
  - `exemplar eval` evaluates a class as a pseudo-rule (`kind: bug_class`, every file).
  - `evalrun` runs only that class, with every file a critical path, on a copy of the snapshot's settings.
- **Tests:**
  - Each operator on a sample: class, expected range, the removed line gone, the expected line's content, and the patch applies to give exactly the mutated file. The per-operator cap; an unparseable file refused.
  - The command end to end: on a real checkout, only the enabled error-handling class gets a case (resource-leak is off in the example config). The config still loads. `evalrun.Cases` resolves the case at the checkout's SHA with its patch, which `git apply --check`s, and a second run adds nothing.
  - `evalrun`: a bug class scores the positive as tp and the negative as tn, and only bug-lane calls run.
- **Not built:**
  - C/C++ operators on tree-sitter: built later (2026-10-14), see "C/C++ mutation operators".
  - Running the generator as a job that opens config PRs.

### Head start: rule exports (2026-10-14)

Spec §10.2, PRD FR-S6, §16 Phase 4. Active rules with `export.agents` reach coding agents through the instruction files each repo chose.
- **`internal/exports`:**
  - `Render` groups each rule's one-line summary by the directory its patterns share, then writes the targets:
    - AGENTS.md and CLAUDE.md, at the root and nested;
    - `.github/copilot-instructions.md`, or `.github/instructions/<area>.instructions.md` with `applyTo`;
    - `.cursor/rules/exemplar[-<area>].mdc` with globs.
  - The blocks sit between BEGIN and END markers, with a 3,000-character budget for Copilot and 8,000 elsewhere; the overflow is dropped with a note.
  - `Merge` replaces the block in place or appends it after the team's text. A new file gets its frontmatter.
  - `Runner`:
    - It runs per opted-in, eligible repo.
    - It reads each file on the default branch.
    - It skips files whose block was hand-edited, judged by the hashes in the `exports` table (migration 00019).
    - It rebuilds `exemplar/exports` from the default branch and writes the changed files.
    - It opens the PR if none is open and enables auto-merge when the repo allows it.
    - An open PR that already has the rendering is left alone.
- **`gh`:**
  - `FlavorExport`: contents and pull requests write, exactly one repo, never exemplar-config.
  - `RepoWriter` covers the default branch, file reads and writes, branch reset, PRs, and native auto-merge over GraphQL.
- **`exemplar-learn export --config DIR [--dry-run]`:** it uses the separate exports app (`EXEMPLAR_EXPORTS_APP_ID`, `EXEMPLAR_EXPORTS_PRIVATE_KEY_FILE`, secret `exemplar-exports-app`).
- **Deployment:** a daily CronJob, `exemplar-learn-export`, at 08:15 UTC, giving 13 learn CronJobs.
- **Tests:**
  - Rendering, areas, budget and merge.
  - The runner against Postgres with a fake repo:
    - the first PR appends after the team's text with auto-merge;
    - a rerun before the merge writes nothing;
    - after the merge the file is current;
    - a new rule replaces the block in place;
    - a change while the PR is open updates it;
    - a hand edit is left alone and marked edited;
    - a denylisted repo is skipped;
    - the dry run.
  - RepoWriter against an httptest GitHub.
  - Token scopes for the new flavor.
- **Deviations:** see spec §10.2 "As implemented":
  - daily instead of on promotion;
  - global rules only;
  - order by severity, not acceptance.
- **Not built:** per-file opt-out. Drift followed in the next section.

### Head start: export drift (2026-10-14)

Spec §10.2 "Drift". How far each repo's generated files are behind the rules.
- **Migration 00020:**
  - `export_runs` holds each config SHA the export job first rendered.
  - `exports` gains `created_at`, `current_sha` and `current_at`. The job sets the last two whenever the default branch matches the rendering.
  - The `export_file_drift` view gives versions behind per file:
    - 0 when current;
    - otherwise the versions since the file was last current;
    - for a file never merged, 1 plus the versions since it was first proposed.
  - The `export_drift` view gives one row per repo, for its furthest-behind file.
- **Shift-left board** (`dashboards/shift-left.json`):
  - stats: repos exporting, current, more than two behind, and hand-edited files;
  - drift per repo, with the export PR;
  - files by target and state;
  - config versions exported per week.
- **Digest:** `--platform-team` (default `@platform/devex`) gets a weekly "Export drift" issue listing repos more than two versions behind and what they wait on (an unmerged PR or a hand edit).
- **Tests:**
  - The runner's drift at each stage: 1 before the first merge, 0 after it, and 3 after a hand edit.
  - The digest against seeded rows: the stale repo listed, current and two-behind repos left out, and no issue without a platform team.
  - The board's SQL runs against the migrated database.

### Head start: upstream effectiveness (2026-10-14)

Spec §10.4, PRD FR-S5. Do exported summaries change what coding agents write?
- **`internal/upstream`:**
  - `Detect` names a PR's agent from a bot author, a `Co-authored-by` trailer or a label. Prose is never used.
  - `Reads` maps the agent to the export targets it reads.
  - `Judge` flags a summary for rewrite when every measurable comparison (at least 20 PRs on each side) drops less than 20%.
- **Recording:**
  - `gh.PullRequest` and `engine.PRInfo` carry labels.
  - `pipeline.Begin` stores `reviews.agent` and `agent_reads` for production reviews. Only the name is stored.
  - Exports record `landed_at` the first time a file matches.
- **Migration 00021:** the `upstream_pr_rule` view has one row per exported rule and in-scope PR, with its agent, whether it received the export, the repo's landing time, and its violations.
- **Query:** `UpstreamRuleStats` returns, per rule, the received and other counts and the before and after counts.
- **Digest:** owners get "Exported summaries to rewrite" with the rates as evidence.
- **Board:** shift-left gains an upstream-effectiveness table.
- **Tests:**
  - Detection: trailers, emails, bot authors, labels, and the negatives (a human co-author, prose, a long label, a human login like a bot's).
  - `Judge`: working, no drop, too few PRs, nothing to drop, mixed comparisons.
  - The worker stores a labelled PR's agent.
  - The view and digest against 50 seeded PRs: a refuted finding is not a violation, the rule without a drop is flagged, and the rule that dropped is not.
- **Not built:**
  - agent markers in config;
  - interval estimates.

### Head start: the exemplar/merge-policy check (2026-10-14)

Spec §6.13 "Approval mechanism", T-0.3. Repos where an App's approval cannot count, for example because they require code-owner review, use `approval_mode: merge_policy_check`. They require the `exemplar/merge-policy` check instead.
- **`gates.HumanMergePolicy`:**
  - It works from people's reviews: each person's latest non-comment review counts, approvals must be of the head, and the author's and bots' reviews don't count.
  - Every owned touched path needs an approver among its CODEOWNERS owners, with teams checked via the gate token's Members read.
  - With no owned paths, one approver is enough.
  - It returns the owner sets still missing.
- **`Poster.SetMergePolicy`** creates a check run. On pass it is completed with success; otherwise it stays in progress with what it waits for. Exemplar's own `exemplar` check stays neutral.
- **The worker sets it:**
  - when a review of a merge_policy_check repo finishes: success if the gates passed, otherwise from people's approvals;
  - when a review is incomplete: from people's approvals;
  - never for shadow repos.
- **`merge_policy` job:**
  - `pull_request_review` submitted or edited by a person, or dismissed, re-decides the check for the PR's current head.
  - It reads the PR's files (with rename sources), the base's CODEOWNERS, people's reviews, and whether a production review of that head passed the gates (`HeadAutoApproved`).
  - Repos in other modes are ignored.
- **Tests:**
  - The gate: head-only approvals, author and bot excluded, changes-requested and dismissal override, comments don't, team and user owners, an email owner unmatched, no CODEOWNERS, and a membership error surfaced.
  - The review path: in progress without approvals, success after an owner's approval, never an APPROVE.
  - The job: owner, stale, non-owner and dismissed approvals; gates passed on the head; a native repo ignored.
  - Routing of review events.
- **Not built:** auto-approve stays off in config, so in practice only the human side runs.

### Head start: counter-metrics and alerts (2026-10-14)

Spec §12.5 "Auto-approve audit" and "Counter-metrics", PRD §13.1, §16 Phase 5 exit ("no counter-metric alarms over 4 weeks").
- **Migration 00022:**
  - `pr_merges`: the merged head and time, from the merge webhook through the merged signal.
  - `pr_links`: PRs later reverted or named in an incident.
  - The `merge_outcomes` view.
- **Path events:**
  - Each revert's undone PR is resolved with the seeds' own resolver: "Reverts owner/repo#N", or the PR of the reverted commit.
  - Each incident's existing PR links are recorded. Only refs are stored.
- **Boards:**
  - `counter-metrics.json`: change failure by approval kind, weekly revert rate and merges, incidents on quiet quick merges, and failed merges.
  - `auto-approve.json`: failed auto-approved merges, and kill-switch state from each repo's latest decision.
- **Alerts:** `dashboards/provisioning/alerts.yaml` has three Grafana rules:
  - a failed auto-approved merge;
  - excess change failure over human-approved merges, once there are 20;
  - an approval from a degraded or incomplete review.
- **Tests:**
  - path events: the revert link through the PR body and through the reverted commit; incident links only for PRs that exist;
  - the merged signal records the merge, and the view and `CounterMetrics` see a later revert;
  - every board and alert query runs; each alert is 0 on an empty database and fires on a seeded degraded approval whose merge was reverted.
- **Not built:**
  - unviewed owned paths and human substantive-comment volume (their inputs aren't recorded);
  - the program board.

### Head start: exemplar review and /v1/evaluate (2026-10-14)

Spec §10.1, PRD FR-S4 and FR-S7 (the access control; MCP stays deferred). A working-tree review before a PR exists.
- **CLI:**
  - `exemplar login` signs in with GitHub's device flow, using the review app's client, and saves the token 0600.
  - `exemplar review [--base] [--repo] [--format text|sarif] [--agent] [--exit-zero]`:
    - It diffs the working tree, including untracked non-ignored files, against the merge-base.
    - It posts the diff, polls, and prints the findings with suggestions, or SARIF.
    - It exits 1 when there are findings.
- **`internal/evaluate`:** the wire format, with request validation (repo, full base SHA, a git diff of at most 1 MiB, the agent name).
- **exemplar-api `/v1/evaluate`:**
  - It verifies the GitHub user token (users only) and the caller's pull access to the repo, both cached for 5 minutes by token hash.
  - It enforces per-caller rate limits and the daily spend and run budget.
  - It stores the request and enqueues the job transactionally.
  - `GET /v1/evaluate/{id}` serves only the requester.
- **exemplar-worker `evaluate` queue:**
  - It runs an eligibility check, finds the installation from deliveries, and checks out the base.
  - `Workspace.ApplyPatch` applies the diff (`git apply --index`, then a no-hooks commit).
  - It runs the full review with `caller=cli` and returns the findings that would have been posted.
  - It clears the diff and marks the evaluation refused, done or failed.
- **Retention:** evaluations follow the findings period; stale diffs are cleared after a day.
- **Tests:**
  - ApplyPatch: the patch applies, and patches that don't apply, escape the tree, use an absolute path, are empty or are oversized are refused.
  - Request validation.
  - API: auth failures, bots, bad bodies, unreadable repos, the size limit, cached identity, another user's result hidden, the budget and the rate limit.
  - Worker against the scripted model: a finding is returned, the diff is cleared, nothing is posted or stored, it runs once, and an ineligible repo, a bad patch, an unpushed base and a missing installation are each refused.
  - CLI: the device flow (pending, slow_down, denied); remote parsing; review end to end against a fake API (tracked, untracked and ignored files; text and SARIF; a clean tree; signed out).
  - Retention of evaluations.
- **Not built:**
  - service JWT callers and `mode: eval`;
  - budgets in config;
  - the CLI panel on the shift-left board.

### Head start: the web UI (2026-10-14)

Spec §12.8, §16 Phase 6. A first, read-only slice of the platform UI in exemplar-api under `/ui/`.
- **`internal/web`:**
  - Pages: rules (owner, status and search filters), rule detail (weekly outcomes and recent findings), finding detail (explanation, fix, verdict, feedback timeline), candidates, the auto-approve audit, and cost (by caller, and by day, caller, stage and provider).
  - `html/template`, no JavaScript, with strict CSP and no-store.
  - The queries are in `db/queries/ui.sql`.
- **Access:**
  - `internal/ghuser` checks a user token's login and repo pull access, cached 5 minutes by token hash. It is shared with `/v1/evaluate`, which now uses it.
  - Sign-in returning to `/ui/` seals the user token (AES-GCM, key derived from the session key) into an HttpOnly cookie for `/ui/` only. `UISession` opens it, bound to the login.
  - Repo rows appear only for readable repos: findings, PRs, decisions, repo-scoped rules, and candidates by their source repos. Failed checks hide those rows and say so.
- **Tests:**
  - Against Postgres with a fake access checker: global rules shown, a secret repo's rule hidden; hidden findings counted; a finding's HTML escaped; a secret repo's finding 404; candidates and audit filtered; cost totals; an access failure notice; the sign-in redirect.
  - Sealed session: a link sign-in keeps no token; the UI cookie is scoped to `/ui/`, unreadable, and rejected when tampered with; bad return paths are refused.
  - The checker: users only, refusals, a failure kept apart from no access, and expiry of the cache.
- **Deviations** (spec §12.8 "As implemented"): `html/template` in place of templ and htmx.
- **Not built:**
  - owner actions, which need a config-writer path from the UI;
  - the playground page and the transcript viewer;
  - eval results and source comments on rule detail.

### Head start: C/C++ mutation operators (2026-10-14)

Spec §8.8, FR-X6. Mutation evals for the bug classes now cover C and C++.
- **`mutate.C`** works on tree-sitter, through the new `syntax.Parse` (which `syntax.Errors` now uses). It has one operator per class:
  - error handling: an `if (rc …)` with a return or goto after rc is assigned from a call;
  - off-by-one: `<` becomes `<=` in a for condition;
  - resource leak: drop the release of something acquired in the block;
  - concurrency: a pthread or `.lock()` pair, or a scoped guard;
  - nil deref: a null guard that only returns.
- Statements are removed only when alone on their lines, and the result must parse with no errors.
- **`exemplar mutations`** sends `.c`, `.h`, `.cc`, `.cpp`, `.cxx`, `.hh`, `.hpp` and `.hxx` files to it. It skips C test files and `third_party/`.
- **Tests:**
  - A C sample (pthread lock, NULL guard, fclose, a for loop, an rc check) and a C++ sample (`lock_guard`, `mu_.lock()`): each class, its expected range, the removed line, and the patch applying to give exactly the mutated file.
  - An unparseable or unsupported file is refused.
  - The command, end to end, adds a C case beside the Go one and skips the C test file.

### Head start: OTel metrics and the telemetry board (2026-10-14)

Spec §12.5 Operations. The user chose environment-variable OTEL destinations.
- **`internal/telemetry`:**
  - `FromEnv` reads the OTel variables: the shared or per-signal endpoints; `OTEL_TRACES_EXPORTER` and `OTEL_METRICS_EXPORTER` (otlp or none); `OTEL_SDK_DISABLED`; and the protocol, where only `http/protobuf` is accepted and anything else fails at startup.
  - `Setup` installs OTLP/HTTP trace and metric exporters accordingly, with the environment's resource attributes winning.
  - Dependencies added: `otlpmetrichttp` and `sdk/metric` v1.47.0, matching the otel modules already in use.
- **Instruments:**
  - in the gateway: model calls by outcome (429 and 5xx included) and their latency;
  - in the GitHub client: rate-limit remaining per installation and resource, and responses by status class;
  - in the worker: review duration by outcome, and enqueue-to-post time.
- **Deploy:**
  - Every container gets `envFrom` from the optional `exemplar-otel` ConfigMap and Secret (the CronJob generator too).
  - The `allow-otel` NetworkPolicy allows port 4318 to an OpenTelemetry Collector.
- **Grafana:** an `exemplar-metrics` Prometheus-compatible datasource (`EXEMPLAR_GRAFANA_METRICS_URL`), and `telemetry.json`. The board test checks that every PromQL expression names a known instrument.
- **Tests:**
  - env parsing (none, shared, per-signal, disabled, grpc refused, unknown exporter);
  - instruments collected through a manual reader;
  - the board check;
  - the deploy test, with manifests rendering to 47 valid resources.
- **Not built:** per-stage engine spans as metrics; River queue metrics, which stay on the Postgres boards.

### Head start: Auth0 service callers of /v1/evaluate (2026-10-14)

Spec §13 IN-5, PRD FR-S7. The user chose Auth0 as the identity provider.
- **`internal/auth0`:**
  - It verifies RS256 access tokens against the tenant's JWKS: keys of at least 2048 bits, an unknown key refetched at most once a minute, and rotation picked up.
  - It checks the issuer `https://<domain>/`, the audience (string or list), and `exp`/`nbf` with a one-minute leeway. `alg` must be RS256; `none` and HS256 are refused.
  - `Claims.CanEvaluate` reads `evaluate:<owner>/<repo>`, `<owner>/*` and `*` from `permissions` or `scope`.
- **`/v1/evaluate`:**
  - A JWT goes to the verifier when Auth0 is configured; a GitHub token goes to GitHub as before.
  - A service is `svc:<subject>` with `caller=agent`, rate-limited and budgeted like a person.
  - Its repos come only from its permissions, never from GitHub.
- **exemplar-api:** `EXEMPLAR_AUTH0_DOMAIN` and `EXEMPLAR_AUTH0_AUDIENCE` must be set together, from the settings ConfigMap, where they are empty by default.
- **Tests:**
  - the verifier: valid tokens; wrong issuer, wrong audience, expired, missing exp, not yet valid, no subject, HS256, alg none and tampered claims; the leeway; the refetch throttle; rotation; domain validation;
  - the API: a service accepted for its permitted repo and refused for another; a bad token; polling; no GitHub calls for services; users unchanged.
- **Not built:** `mode: eval` for config CI, which can now authenticate the same way.

### Head start: web UI owner actions (2026-10-14)

Spec §12.8. The user chose to route owner actions through a worker job.
- **Migration 00024:** `owner_actions` records the action, target, actor (the signed-in login), reason, status, result and PR. It is the audit of who asked for what.
- **exemplar-api:**
  - Form buttons:
    - rule detail: shadow, and kill with a reason;
    - candidates: kill with a reason;
    - audit: a kill switch per readable repo;
    - finding detail: mark false positive.
  - Each form carries an HMAC token bound to the viewer, the action and the target, from a key derived from the session key.
  - A kill switch also checks repo read access.
  - The action row and its River job are inserted in one transaction.
  - A false positive enqueues `feedback_link` with reason `wrong`.
  - `/ui/actions` lists the viewer's actions.
- **`internal/owneract`:**
  - Authorization is by owning team, candidate owner, the repo's `approvable_owners`, or the platform team.
  - Rule status is edited by value only (quotes and comments kept). Only global rules can be changed; shadow needs an active rule.
  - Candidate kills require a reason, which carries the actor.
  - The kill switch sets `auto_approve.enabled: false` only; turning it on stays manual.
  - Each action gets one branch and PR, naming the actor and reason. It is merged when protection allows, otherwise it waits for review.
  - Refusals are recorded and not retried; GitHub failures are retried and recorded as failed after the last attempt.
- **exemplar-worker:** the `owner_action` job, with the config-writer and gate tokens scoped to the config repo, whose installation is found from the app. `--platform-team` sets the platform team.
- **Tests:**
  - The runner against Postgres with a fake config repo: owner shadow (merged, comment kept); a stranger refused; already shadow; not active; a platform kill; repo and unknown rules refused; candidate kills (no reason, a stranger, success, again); the kill switch (approvable owner, merge blocked so the PR stays open, no block, not opted in, a stranger); a membership failure retried then recorded as failed; a finished action not rerun.
  - The UI: valid forms queue, forged and cross-target tokens are refused, an unknown action is refused, a kill switch for an unreadable repo is refused, the actions page, and false positive (a token reused on another finding is refused).
  - The worker's last-attempt failure.
- **Not built:**
  - rule editing;
  - eval-case suggestions from false positives.

### Head start: mode eval for config CI (2026-10-14)

Spec §8.9 step 2. Config CI's eval runs in exemplar-worker, so CI holds no model or GitHub credentials.
- **`evaluate.Request`** gains `mode: eval`, `head_sha` and `rules`. Validation requires a full head SHA, refuses a diff, and checks rule ids (at most 50).
- **Migration 00025** adds `evaluations.mode`, `head_sha` and `rules`.
- **`internal/evalrun`** now holds `EvaluateRule`, `ChangedRules` and `Markdown`, moved from the CLI, so the CLI and the worker share them.
- **exemplar-worker:**
  - Mode eval is for exemplar-config only.
  - It checks out base and head with the installation's read-only token and loads the head config.
  - It evaluates the changed rules plus the requested ones, with eligibility from the deployed config.
  - It returns the reports, the summary table and pass. The job timeout is now an hour.
- **CLI:** `exemplar eval --remote [--repo] [--wait]`.
- **Example workflow:** the eval job gets an Auth0 token by client credentials and runs `--remote`. It no longer needs the gateway token, the case-repo token, or the engines installed.
- **Tests:**
  - Request validation.
  - The worker against a local config repo: the changed rule without cases fails the gate, the extra rule is evaluated, no change passes, another repo is refused, and an unpushed head is refused.
  - The API: a service evaluating the config repo, and a diff refused in eval mode.
  - The CLI end to end against a fake API: the request (mode, merge-base, head, repo, rules), indented JSON, markdown, the failing and passing exits, and that `--changed-since` is required.

### Head start: the web UI playground (2026-10-14)

Spec §12.8 "Playground".
- **Migration 00026:** `playground_runs` (actor, PR, rule YAML and id, status, result, cost), and the `propose_rule` owner action.
- **exemplar-api:**
  - `/ui/playground` takes a rule file, prefilled from a visible rule (`?rule=`) or one of your runs (`?from=`), and a PR given as `org/repo#N` or a URL.
  - Submitting checks the form token, the schema, the viewer's repo access and the daily budget, then records the run and enqueues `playground_run` in one transaction.
  - `/ui/playground/{id}` is visible only to the person who ran it.
  - Rule pages link to the playground.
- **exemplar-worker:**
  - The `playground_run` job, on the evaluate queue, uses `playground.RuleFileSnapshot` over the deployed config and `playground.Runner.Try`.
  - It reads the PR and clones with the installation found from deliveries. The requester's access was already checked with their own token.
  - It stores `evaluate.TryResult`.
- **`propose_rule`:**
  - Only the requester can save.
  - A new id becomes a candidate file. An existing global rule is replaced, for its owners or the platform team. An unchanged rule and a repo's own rule are refused.
  - It opens a draft PR and never merges it.
- **Retention:** runs follow the findings period.
- **Tests:**
  - The worker against the scripted model: the PR not found without an installation, a finding kept, nothing posted, a rule that doesn't load or names another id refused, no rerun.
  - `propose_rule`: another actor refused; a new rule as a draft candidate, never merged; an edit by a stranger or the owner; unchanged; invalid.
  - The UI: prefill from a visible rule and not from a secret one; a bad token, a bad PR, an invalid rule, an unreadable repo; a run and its page (running, then findings escaped, dropped reasons, cost); save; the budget; another person's run hidden.

### Head start: sign-in sources, GitHub OAuth and OIDC (2026-10-14)

Spec §12.8 and §13 IN-2. The user asked for GitHub as an authentication source following Coder's pattern, and chose OIDC to replace GitHub for authorization: repo access from IdP groups mapped to repos.
- **`internal/oidc`:**
  - `EXEMPLAR_OIDC_*` settings, as in Coder, plus `GROUP_REPOS`, `CLI_CLIENT_ID` and `CLIENT_SECRET_FILE`.
  - It discovers the provider and checks the issuer.
  - The authorization-code flow uses state, nonce and PKCE S256; the ID token is verified against the JWKS.
  - The userinfo merge requires the same subject.
  - Claim handling: the email field; `email_verified` unless ignored; email domains; the username field, else the email's local part; groups (string or list); allowed groups.
  - Groups map to repo patterns.
  - `Verify` accepts the web or CLI client's ID tokens for `/v1/evaluate`.
- **`internal/svcauth`:**
  - The JWKS and RS256 verifier, extracted from `internal/auth0`, which now builds on it.
  - `Multi` tries every source sharing an issuer, so an Auth0 tenant can be both the OIDC provider and the service-token issuer.
  - `Claims.User` marks people.
- **exemplar-api:**
  - `/login` offers the configured sources, going straight to the only one when there is one.
  - GitHub OAuth (`EXEMPLAR_OAUTH2_GITHUB_*`, renamed from `EXEMPLAR_GH_OAUTH_*`) requires allowed orgs or allow-everyone; membership is checked with the person's token (`ghuser.InOrgs`).
  - `/oidc/start` and `/oidc/callback` handle OIDC; the state, nonce and PKCE verifier are kept in a sealed cookie.
  - `UISession` returns the viewer and an access check: their GitHub permission, or their groups' repos. The web UI uses it for every repo-gated row and form.
  - `/v1/evaluate` refuses GitHub tokens when GitHub sign-in is off, enforces allowed orgs, and accepts OIDC ID tokens as people.
- **CLI:**
  - `exemplar login --oidc [--oidc-issuer] [--client-id] [--scopes]` runs the device authorization grant against the discovered endpoint, then refreshes the ID token when it is near expiry.
  - GitHub's client id is `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID`.
- **Deploy:**
  - The `exemplar-auth` ConfigMap (envFrom) and the optional `exemplar-oidc` Secret.
  - The settings key is renamed.
- **Tests:**
  - `oidc` against a fake IdP: the exchange with userinfo merge; refusals for domain, unverified email, no email, nonce, audience, userinfo subject and mismatched PKCE; fallbacks and allowed groups; CLI tokens; config validation; env parsing.
  - The api package:
    - the GitHub flow through `/login`;
    - allowed orgs;
    - GitHub access through the session;
    - OIDC end to end (forged state, wrong domain, session and group access, GitHub off);
    - the two-source login page;
    - `/v1/evaluate` with an org non-member, an OIDC person, and GitHub off.
  - The CLI's OIDC device flow (a pending 400, then a token) and refresh.

### Head start: connect GitHub after OIDC sign-in (2026-10-14)

Spec §12.8 "sign-in sources". With OIDC replacing GitHub for authorization, people still need their GitHub identity wherever Exemplar compares them with GitHub. That covers feedback roles from PR participation, and rule-owner team membership for owner actions.
- **Migration 00027:** `identity_links` (issuer, subject, email, GitHub id and login). The GitHub id is unique.
- **exemplar-api:**
  - After an OIDC callback with no link, the sign-in waits in a sealed `exemplar_link` cookie (10 minutes), and the person is sent to GitHub OAuth. The state now carries a mode, `signin` or `link`.
  - The GitHub callback in link mode checks allowed orgs, refuses an account linked to another identity, saves the link, and starts the session as the GitHub login with the OIDC repo patterns.
  - Later OIDC sign-ins use the stored link.
  - `optional` mode shows a connect page with "continue without" (`/connect/skip`).
  - `/connect/github` resumes a waiting sign-in; `/ui/connect-github` (`Relink`) connects a different account.
  - `EXEMPLAR_OAUTH2_GITHUB_SIGN_IN=false` hides GitHub as a sign-in (and its tokens on `/v1/evaluate`) but keeps it for connecting.
  - `/v1/evaluate` maps OIDC ID tokens to the linked login, and refuses unlinked people when linking is required.
- **Tests:**
  - Required connect: the session is the GitHub login while access stays the OIDC groups'; the second sign-in is remembered; the same account for another identity is refused and leaves no session; an identity's own account works; relink; `/connect/github` needs a waiting sign-in.
  - Optional: the page, then skip.
  - Required: skip refused.
  - Connect-only GitHub: no button, no `/oauth/start`, connecting still works, and it is refused without OIDC.
  - `/v1/evaluate`: linked, unlinked-required and unlinked-optional people.
  - The Postgres identity store: link, conflict, switch, and a freed account.
