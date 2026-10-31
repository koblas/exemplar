# Releases and images

Exemplar ships two images on Docker Hub, each in its own repository, both running as UID 65532 with a read-only root filesystem, for `linux/amd64`:

| Image | Repository | What |
|---|---|---|
| Services | `docker.io/koblas/exemplar` | Every Go service, plus git, ripgrep, universal-ctags, ast-grep, Semgrep and clang-format |
| Learning | `docker.io/koblas/exemplar-learning` | The Python learning jobs (`cluster`, `rank`) |

Both repositories take the same tags: `<version>` and `<major>.<minor>` for each release, `latest` for the newest release, `main` for the tip of `main`, and `sha-<short>` for every push. Releases up to v0.6.0 published the learning image as `koblas/exemplar:learning-<version>`; those tags stay, but new releases push only to `koblas/exemplar-learning`.

`.github/workflows/images.yml` pushes both on every push to `main` and each `v*` tag; its run summary prints each digest and the `images:` entry to paste. The base names the images `exemplar` and `exemplar-learning`, and an overlay's `images:` entries map each name to its repository and digest.

`latest` follows the newest release, not `main`. For an installation, pin a digest instead of following either.

`.github/workflows/werf.yml` also builds both images with [werf](https://werf.io) on every push to `main` and each release, and pushes them to `ghcr.io/koblas/exemplar` and `ghcr.io/koblas/exemplar-learning` next to werf's content-based tags: `main` and `sha-<short>` from `main`, and `latest` for the newest release. Version tags are published only to Docker Hub. Its run summary prints each digest and the `images:` entry for GHCR.

## Release assets

Each GitHub release (what changed is in [CHANGELOG.md](https://github.com/koblas/exemplar/blob/main/CHANGELOG.md); the process is in [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md#releasing)) attaches:

| Asset | What |
|---|---|
| `install-tunnel.yaml` | The default build (core plus the Cloudflare Tunnel), rendered by [`render-release.sh`](https://github.com/koblas/exemplar/blob/main/deploy/render-release.sh) with both images pinned by digest. |
| `install-direct.yaml` | The Ingress build, likewise. |
| `exemplar_<version>_<os>_<arch>.tar.gz`, `SHA256SUMS` | The CLI for linux (amd64, arm64) and darwin (arm64, amd64). |

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

Versions are `v<major>.<minor>.<patch>` tags cut by release-please from Conventional Commits; breaking changes bump the minor version until 1.0.0. Unpublished changes: [How to build your own images](../guides/install/build-images.md).
