---
paths:
  - "deploy/**"
  - "examples/k8s/**"
---

# Kubernetes manifests and images

Manifests are plain Kustomize (`deploy/k8s/core`, `deploy/k8s/components`,
the builds `deploy/k8s/base` and `deploy/k8s/direct`); images are in
`deploy/docker`. `go test ./deploy` checks every build against the code and
reads components without kustomize, so keep them resource-only
(`docs/reference/workloads.md`). Render with `make manifests` before checking
the rendered resources; do not assume a cluster is available. Asking before
cluster access, deployment or image publishing is in `.claude/CLAUDE.md`.

- Pin container images to immutable versions or digests; avoid `latest`.
- Run as non-root, drop capabilities, disable privilege escalation, and use
  `RuntimeDefault` seccomp. Prefer a read-only root filesystem with explicit
  writable mounts only where needed. Set justified CPU/memory requests and limits.
- Keep Deployment selectors, pod labels, Service selectors, and ports consistent,
  and probe paths and ports aligned with the code; keep the termination grace
  period longer than the service's shutdown deadline.
- Use least-privilege RBAC; disable service account token mounting unless needed.
- Keep secrets out of source and rendered output. Reference externally supplied
  Secrets; do not invent credentials or commit kubeconfigs or secret values.
- A new env var, Secret key, ConfigMap key, port, probe or CronJob schedule gets
  its row in `docs/reference/` (`go test ./docs` checks env vars); `/docs` puts it
  on the right page.
