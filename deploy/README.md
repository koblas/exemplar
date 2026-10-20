# Deploying Exemplar

The deployment reference is split by quadrant on the documentation site: [`docs/reference/workloads.md`](../docs/reference/workloads.md) (layout, inbound builds, every workload's ports, probes and shutdown, known limits; published at <https://koblas.github.io/exemplar/reference/workloads/>), [`docs/reference/secrets-and-configmaps.md`](../docs/reference/secrets-and-configmaps.md) (every setting and Secret), [`docs/guides/install/in-cluster-postgres.md`](../docs/guides/install/in-cluster-postgres.md) and [`docs/guides/develop/check-manifests.md`](../docs/guides/develop/check-manifests.md) (the local checks). Installing starts at <https://koblas.github.io/exemplar/tutorials/quickstart/>.

Here: `k8s/` (the Kustomize manifests), `docker/` (the images), `render-release.sh` (a release's install manifests) and `manifests_test.go` (`go test ./deploy`).
