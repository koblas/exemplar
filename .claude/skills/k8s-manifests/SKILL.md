---
name: k8s-manifests
description: "Use when authoring or validating Kubernetes manifests, Deployments, Services, probes, RBAC, security contexts, Helm charts, or Kustomize overlays for the Go service. Defaults to local validation, not deployment."
argument-hint: "<manifest change or validation scope>"
---

# Kubernetes Manifest Workflow

Handle this task: $ARGUMENTS

1. Read project guidance, existing manifests, and service configuration. Establish
   target Kubernetes version and existing Helm/Kustomize/plain YAML conventions.
   If absent, prefer plain YAML for a small service; do not introduce both Helm and
   Kustomize. Ask for missing deployment inputs rather than inventing real targets.
2. Check image references, selectors, container/Service ports, configuration keys,
   and probe paths against the Go service. Do not define probes for absent endpoints.
3. Set appropriate resource requests/limits and hardened security contexts. Use
   least-privilege RBAC only if Kubernetes API access is needed. Ensure writable
   mounts support any required runtime files with a read-only root filesystem.
4. Match shutdown deadlines to `terminationGracePeriodSeconds` and readiness
   draining. Add rollout, availability, or autoscaling configuration only when the
   service's requirements and expected replica count justify it.
5. Render locally using the repository's command, `helm template`, or
   `kubectl kustomize`, as applicable. Helm/kubectl commands still need approval.
   Validate rendered resources with an available schema validator such as
   `kubeconform -strict -summary -kubernetes-version <target-version> <rendered-file>`.
   Check CRDs with their supplied schemas; do not silently skip unknown resources.
6. Report rendering and validation results, unresolved inputs, and rollout risks.
   A successful local schema check is not proof of cluster admission or runtime health.

Do not contact a cluster automatically. Server-side dry runs and `kubectl diff`
contact the API server too; obtain explicit approval and specify context/namespace.
This skill does not authorize apply, upgrade, deletion, or image publishing.