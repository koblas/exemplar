# Deploying Exemplar

The deployment reference (layout, inbound builds, in-cluster Postgres, workloads, every setting and Secret, local checks and risks) is [`docs/reference/deployment.md`](../docs/reference/deployment.md), published at <https://koblas.github.io/exemplar/reference/deployment/>. Installing starts at <https://koblas.github.io/exemplar/install/>.

Here: `k8s/` (the Kustomize manifests), `docker/` (the images), `render-release.sh` (a release's install manifests) and `manifests_test.go` (`go test ./deploy`).
