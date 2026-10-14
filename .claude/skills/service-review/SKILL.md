---
name: service-review
description: "Use when explicitly asked to review Go service or Kubernetes changes for bugs, regressions, security issues, lifecycle mismatches, or missing tests. Produces findings without editing files."
argument-hint: "<files, diff, or review scope>"
disable-model-invocation: true
---

# Go Kubernetes Service Review

Review this scope: $ARGUMENTS

1. Read project guidance and the specified changes. If no scope is supplied,
   inspect staged and unstaged changes plus relevant untracked files. Do not modify
   files, format code, commit, deploy, or publish anything during a review.
2. Follow changed behavior to the nearest callers and tests. Prioritize concrete
   bugs, regressions, unsafe input handling, data exposure, and missing validation
   over style preferences or speculative architecture changes.
3. Check context propagation, races, goroutine/resource leaks, network timeouts,
   error handling, and SIGTERM shutdown. Inspect success and failure test coverage.
4. Check Go/deployment contracts: ports, environment variables, probes, readiness
   during draining, and shutdown versus the termination grace period. Inspect
   selectors, pinned images, resource settings, security contexts, and RBAC scope.
5. Run relevant existing checks when useful and available. Avoid cluster access;
   report anything unverified and distinguish confirmed failures from hypotheses.
6. Present actionable findings ordered by severity, with file/line references,
   failure scenarios, and suggested remedies. Follow with open questions and a
   brief summary. If no issues are found, say so and state remaining validation gaps.