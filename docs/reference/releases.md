# Releases and images

Exemplar ships two images in `docker.io/koblas/exemplar`, both running as UID 65532 with a read-only root filesystem, for `linux/amd64`:

| Image | Tags | What |
|---|---|---|
| Services | `<version>`, `<major>.<minor>`, `main`, `sha-<short>` | Every Go service, plus git, universal-ctags, ast-grep, Semgrep and clang-format |
| Learning | `learning-<version>`, `learning-<major>.<minor>`, `learning-main`, `learning-sha-<short>` | The Python learning jobs (`cluster`, `rank`) |

`.github/workflows/images.yml` pushes both on every push to `main` and each `v*` tag; its run summary prints each digest and the `images:` entry to paste. Both images share one repository and the base keeps the logical names `exemplar` and `exemplar-learning`: Kustomize matches images by name alone, so renaming them in the base would leave an overlay unable to pin them apart.

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
```

```yaml
images:
  - name: exemplar
    newName: docker.io/koblas/exemplar
    digest: sha256:<services digest>
  - name: exemplar-learning
    newName: docker.io/koblas/exemplar
    digest: sha256:<learning digest>
```

Versions are `v<major>.<minor>.<patch>` tags cut by release-please from Conventional Commits; breaking changes bump the minor version until 1.0.0. Unpublished changes: [How to build your own images](../guides/install/build-images.md).
