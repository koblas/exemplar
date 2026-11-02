# Releases and images

Exemplar ships two images on Docker Hub, each in its own repository, both running as UID 65532 with a read-only root filesystem, for `linux/amd64`:

| Image | Repository | What |
|---|---|---|
| Services | `docker.io/koblas/exemplar` | Every Go service, plus git, ripgrep, universal-ctags, ast-grep, Semgrep and clang-format |
| Learning | `docker.io/koblas/exemplar-learning` | The Python learning jobs (`cluster`, `rank`) |

Both repositories take the same tags: `<version>` and `<major>.<minor>` for each release, `latest` for the newest release, `main` for the tip of `main`, and `sha-<short>` for every push. Releases up to v0.6.0 published the learning image as `koblas/exemplar:learning-<version>`; those tags stay, but new releases push only to `koblas/exemplar-learning`.

`.github/workflows/images.yml` builds both with [werf](https://werf.io) and pushes them on every push to `main` and each `v*` tag; its run summary prints each digest and the `images:` entry to paste. werf keeps its build cache in private GHCR repositories (`ghcr.io/koblas/exemplar`, `ghcr.io/koblas/exemplar-learning`); pull the images from Docker Hub. The base names the images `exemplar` and `exemplar-learning`, and an overlay's `images:` entries map each name to its repository and digest.

`latest` follows the newest release, not `main`. For an installation, pin a digest instead of following either.

## Release assets

Each GitHub release (what changed is in [CHANGELOG.md](https://github.com/koblas/exemplar/blob/main/CHANGELOG.md); the process is in [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md#releasing)) attaches:

| Asset | What |
|---|---|
| `install-tunnel.yaml` | The default build (core plus the Cloudflare Tunnel), rendered by [`render-release.sh`](https://github.com/koblas/exemplar/blob/main/deploy/render-release.sh) with both images pinned by digest. |
| `install-direct.yaml` | The Ingress build, likewise. |
| `exemplar_<version>_<os>_<arch>.tar.gz`, `SHA256SUMS` | The CLI for linux (amd64, arm64) and darwin (arm64, amd64). |

Each release also publishes `oci://ghcr.io/koblas/exemplar-manifests`, a [Flux](https://fluxcd.io) OCI artifact of the Kustomize tree (`base/`, `direct/`, `core/`, `components/`) with both images pinned by digest in `core/`. It is tagged `<version>` and `<major>.<minor>` (a pre-release gets only `<version>`), and `latest` when it is the newest release; re-publishing an older release never moves `latest`. The release workflow checks that its `base` and `direct` render exactly as `install-tunnel.yaml` and `install-direct.yaml` before pushing it.

## Pinning

Use an install file as the overlay's resource:

```yaml
resources:
  - https://github.com/koblas/exemplar/releases/download/<version>/install-tunnel.yaml
```

Or build on `github.com/koblas/exemplar//deploy/k8s/base?ref=<version>` and pin both digests yourself; the base's all-zero digests fail to pull rather than run something unknown:

```bash
docker buildx imagetools inspect koblas/exemplar:<version> --format '{{json .Manifest.Digest}}'
docker buildx imagetools inspect koblas/exemplar-learning:<version> --format '{{json .Manifest.Digest}}'
```

```yaml
images:
  - name: exemplar
    newName: docker.io/koblas/exemplar
    digest: sha256:<services digest>
  - name: exemplar-learning
    newName: docker.io/koblas/exemplar-learning
    digest: sha256:<learning digest>
```

### Following releases with Flux

A cluster running Flux can follow releases from the OCI artifact instead of pinning versions by hand:

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: exemplar
  namespace: flux-system
spec:
  interval: 10m
  url: oci://ghcr.io/koblas/exemplar-manifests
  ref:
    tag: latest # or semver: "0.9.x"
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: exemplar
  namespace: flux-system
spec:
  interval: 10m
  sourceRef:
    kind: OCIRepository
    name: exemplar
  path: ./base # or ./direct
  # components: [../components/postgres]
  prune: true
```

Manifests and images always match: drop your own `images:` entries for `exemplar` and `exemplar-learning`, since the artifact already pins them. Keep your patches (`spec.patches`) and components, including the settings `EXEMPLAR_CONFIG_REPO` and `EXEMPLAR_API_URL`. The package is public, so the `OCIRepository` needs no credentials; for a private copy, create a pull secret with `flux create secret oci` and name it in `spec.secretRef`.

Versions are `v<major>.<minor>.<patch>` tags cut by release-please from Conventional Commits; breaking changes bump the minor version until 1.0.0. Unpublished changes: [How to build your own images](../guides/install/build-images.md).
