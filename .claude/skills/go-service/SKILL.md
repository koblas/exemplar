---
name: go-service
description: "Use when implementing or debugging a Go Kubernetes service, HTTP endpoint, configuration, readiness, cancellation, or graceful shutdown. Includes focused testing and validation."
argument-hint: "<service change or bug>"
---

# Go Service Development

Handle this task: $ARGUMENTS

1. Read project guidance and the nearest implementation, tests, and build commands.
   If no Go module exists, establish the module path, Go version, and intended
   service behavior before initializing one. Do not invent an organization domain.
2. Identify the controlling code path and one focused check of the desired behavior.
   Keep the change within the requested scope; preserve existing public contracts.
3. Implement the smallest change using repository conventions. Propagate contexts,
   bound network operations, preserve error causes, and keep secrets out of logs.
   For lifecycle changes, align readiness, draining, and shutdown deadlines.
4. Add or update nearby tests covering success, failure, and relevant cancellation
   paths. Use `httptest` for HTTP behavior and avoid timing-dependent sleeps.
   Then add the technique the change calls for (spec §16.1, "Which technique a
   change needs"): a simulation invariant for worker state machines, a metamorphic
   relation for review output, a fuzz target for parsers of outside input, a seeded
   property test for invariants over an input domain, a configuration-matrix row
   for new settings, a benchmark for hot paths. Say which you added or skipped.
5. Format touched files and run the focused package tests first. Then run applicable
   broader checks: `go test ./...`, `go test -race ./...`, `go vet ./...`, and
   `go build ./...`. Follow existing CI gates if different; report toolchain blockers.
6. Check affected manifest contracts if ports, probes, environment variables, or
   shutdown behavior changed. Summarize changes, verification, and remaining risks.

Do not deploy, publish images, change dependencies, or commit without approval.