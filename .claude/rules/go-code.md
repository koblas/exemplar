---
paths:
  - "**/*.go"
---

# Go code: time and clocks

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
