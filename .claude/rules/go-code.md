---
paths:
  - "**/*.go"
---

# Go code

Conventions for the Go service. The repository map, work tracking and the
ask-before rules are in `.claude/CLAUDE.md`; the design is in
`docs/spec/technical-spec.md`.

## Checks

- Default checks: `go test ./...`, `go test -race ./...`, `golangci-lint run`,
  `go build ./...`. Start with the touched package's tests, then broaden.
  `make check` runs the quick gate; `make ci` mirrors `ci.yml`.
- `golangci-lint run` follows `.golangci.yml` and includes `govet`; there is
  no separate `go vet` step in the Makefile or CI.
- Format touched Go files with `gofmt`; do not reformat unrelated files.
- Set `EXEMPLAR_REQUIRE_ENGINES=1` (as CI does) to make engine-dependent tests
  fail rather than skip. Postgres tests start a pgvector container with
  testcontainers-go and skip without a container runtime unless
  `EXEMPLAR_REQUIRE_DB=1`. Regenerate `internal/store/queries` with sqlc v1.31.1.
- Under devenv, the nix cc wrapper rejects the tree-sitter grammars' cgo include
  paths when `NIX_ENFORCE_PURITY=1`; the Makefile exports 0 and the session
  hook leaves it unset. If a direct `go build` fails with
  `NIX_BUILD_TOP: unbound variable`, run it with `NIX_ENFORCE_PURITY=0`.

## Service conventions

- Prefer the standard library and existing dependencies. Add a dependency only
  for a concrete requirement; do not assume a router, database, or cloud provider.
- New code goes in `cmd/<service>/` for entrypoints and `internal/` for private
  packages.
- Keep transport, business logic, and infrastructure separate without speculative
  interfaces. Define small interfaces at the consumer when they are needed.
- Propagate `context.Context`, cancellation, and deadlines to downstream calls.
- Return contextual errors, preserving causes with `%w`; avoid panic for ordinary
  failures. Validate configuration at startup and fail clearly on invalid values.
- Use structured logs (`log/slog`); never log credentials or payload secrets.
- Configure HTTP server and client timeouts, bound request sizes, and shut down
  gracefully on SIGTERM/SIGINT. Stop background workers and close resources
  within a deadline.
- Keep liveness independent of external dependency outages. Readiness reflects
  the ability to serve traffic and turns false during shutdown. Align probe paths
  and ports with the manifests; keep the termination grace period longer than
  shutdown.

## Tests

- Deterministic table-driven tests are the default. Test cancellation, error
  paths, and shutdown for changes that affect them. `httptest` servers are fine
  outside a synctest bubble; inside one, serve HTTP through an in-memory
  `http.RoundTripper` (see "Time and clocks"). Seed any randomness.
- Pick the technique by the change (spec §16.1, "Which technique a change
  needs"): a worker state machine → a seeded simulation invariant
  (`internal/worker/sim_test.go`); review output → a metamorphic relation over
  `replay --scripted`; a parser of outside input → a native fuzz target, added to
  `make fuzz` and `ci.yml`; an invariant over an input domain → a seeded property
  test (stdlib `math/rand/v2` with a fixed seed, as `TestStage1Invariants`; no
  new library); a new setting → a configuration-matrix row; a hot path → its
  benchmark. Name a technique you skipped, and why, in the PR description.

## Time and clocks

The reasoning is in `docs/spec/technical-spec.md` §16.1 ("Time in tests"). The rules:

- **No injected clocks.** Production code calls `time.Now`, `time.Since`,
  `time.NewTimer` and so on directly. No `Now func() time.Time` fields, clock
  interfaces, or clock parameters "for tests".
- **Tests never wait on the real clock.** A `time.Sleep`, `time.After`,
  `time.AfterFunc`, `time.Tick`, `time.NewTicker`, `time.NewTimer`,
  `time.Since` or `time.Until` in a `_test.go` file goes inside the function
  passed to `synctest.Test`, or in a helper used only from there. That
  includes sleep-poll loops (`for { if done { break }; time.Sleep(...) }`)
  and `select { case <-ch: case <-time.After(30 * time.Second): }` hang guards.
  `context.WithTimeout` and `context.WithDeadline` count too: an outer guard
  becomes `t.Context()`.
  - In a bubble, replace polling with `synctest.Wait()` and move time with
    `time.Sleep(d)` at the real durations (defaults, not shrunk to milliseconds).
  - Pin a date by sleeping to it first: `time.Sleep(time.Until(fixed))`.
  - Outside a bubble, wait on an event (a channel, a `sync.WaitGroup`, a
    library's wait strategy) and let `go test -timeout` catch hangs.
    `time.Now()` to build fixture values is fine.
- **Bubble pitfalls.**
  - Create everything the test drives inside the bubble: fakes, clients,
    servers, `t.Context()`. Goroutines started outside are not in it.
  - `t.Run` and `t.Parallel` cannot be called inside a bubble: put
    `t.Parallel()` before `synctest.Test`, and the bubble inside each subtest.
  - Bubble time moves only while every goroutine in it is durably blocked.
    Network I/O and syscalls are not, so an `httptest` server or a live
    Postgres LISTEN freezes the clock. Serve HTTP through an in-memory
    `http.RoundTripper`; keep Postgres/River tests out of bubbles.
  - Cancel and stop what the test started (`defer cancel()`) inside the
    closure, so no goroutine outlives the bubble.
- **Exceptions are marked.** Code that cannot run in a bubble (a real
  container, zoekt's fsnotify watcher) and still needs a wall-clock wait
  carries `// wallclock: <reason>` on that line or the line above. Prefer
  backdating state (as `internal/search/index` does) over a marker.
  A multi-line comment works if the block ends on the line above.
- **Enforced** by `go test ./internal/testclock`, which runs in `go test ./...`.
