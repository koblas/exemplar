# How to check manifests locally

Nothing in the repository needs a cluster. Three checks cover the manifests; one test in the first needs a container runtime.

## 1. Check them against the code

```bash
go test ./deploy
```

It runs every check on each of six builds: `base`, `direct`, `base` with the ingress component, `base` with the Postgres component, `base` with the PgBouncer component, and `base` with both.

- **Rendering:** every manifest in each directory a build uses is listed in its `kustomization.yaml`; no YAML anchors (kustomize mishandles them); no patches, since the test reads manifests without kustomize; no object defined twice.
- **Pods and containers:** pods run non-root with RuntimeDefault seccomp and no token, and set `fsGroup` when a Secret volume's mode is not world-readable; containers are read-only, drop all capabilities, have CPU and memory requests and limits, and a writable `/tmp`; images are either kustomize-pinned or digest-pinned.
- **References:** every volume, ConfigMap key and Secret resolves to a defined or documented one.
- **Workloads:** each long-running workload's selector matches its pods; it listens on the port it exposes; its probes are `/healthz` and `/readyz` on that port (cloudflared: `/healthcheck` and `/ready` on its metrics port; Postgres: `pg_isready` on 5432, with a preStop fast shutdown; PgBouncer: TCP on 5432, transaction mode with prepared statements, and a preStop SIGINT shutdown); `terminationGracePeriodSeconds` outlasts the shutdown deadlines read from the binary's source and args.
- **Selectors:** every Service, PodDisruptionBudget and NetworkPolicy selects a workload, and each workload has its own NetworkPolicy.
- **Inbound:** the build has the Ingresses it should (none in the default); each Ingress and tunnel route goes to a Service port; the tunnel's routes end in a catch-all; the webhook and the API admit exactly the build's sources; the webhook's only egress is Postgres, whichever policies select it.
- **Environment:** every `EXEMPLAR_*` variable is one the Go or Python code actually reads; `EXEMPLAR_DATABASE_URL` is always the direct `url`, and only the runner template uses the pooled `runner-url`.
- **The pooler, live:** the PgBouncer component's config, in its pinned image and run as the pod runs it (its user, read-only root, no capabilities), in front of a test Postgres; a runner's queries and River calls go through it, and its preStop stops it with a client connected. It starts containers with testcontainers-go and skips without a container runtime, unless `EXEMPLAR_REQUIRE_DB=1`.
- **Example overlays:** `examples/k8s/acme`, `quickstart` and `quickstart-direct` map every image the base names, pin each by digest, and list only paths that exist.

Keep components resource-only (no patches), since the test reads them without kustomize.

## 2. Render and schema-check

Devenv has no kubectl, Kustomize or kubeconform. With kubectl on your `PATH` (OrbStack's works; `kubectl kustomize` renders locally and contacts no cluster):

```bash
make manifests
```

It renders `base`, `direct`, both, and `base` with Postgres, the three example overlays, and a release render, into `out/`, and runs kubeconform `-strict` for Kubernetes 1.33.0 on each through `go run`. The resource counts at the time of writing: `base` 53, `direct` 50, both 57, `base` with Postgres 57. By hand:

```bash
kubectl kustomize deploy/k8s/base > rendered.yaml
```

```bash
go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0 -strict -summary -kubernetes-version 1.33.0 rendered.yaml
```

A schema check is still not proof of admission or runtime health.

## 3. Build the images

```bash
make images
```

Both images build with BuildKit for `linux/amd64` and run with a read-only root as UID 65532 with every engine present; nothing is pushed. CI repeats the build, render and schema check in its `images` job.
