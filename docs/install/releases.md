# Releases and images

Exemplar ships two images in `docker.io/koblas/exemplar`, both running as UID 65532 with a read-only root filesystem, for `linux/amd64`:

| Image | Tags | What |
|---|---|---|
| Services | `<version>`, `<major>.<minor>`, `main`, `sha-<short>` | Every Go service, plus git, universal-ctags, ast-grep, Semgrep and clang-format |
| Learning | `learning-<version>`, `learning-main`, … | The Python learning jobs (`cluster`, `rank`) |

Each GitHub release (what changed is in [CHANGELOG.md](https://github.com/koblas/exemplar/blob/main/CHANGELOG.md); see [CONTRIBUTING.md](https://github.com/koblas/exemplar/blob/main/CONTRIBUTING.md#releasing)) attaches `install-tunnel.yaml` and `install-direct.yaml`, rendered with both images pinned by digest. Use one as your overlay's resource:

```yaml
resources:
  - https://github.com/koblas/exemplar/releases/download/<version>/install-tunnel.yaml
```

Building on `github.com/koblas/exemplar//deploy/k8s/base?ref=<version>` instead, pin both digests yourself; the base's all-zero digests fail to pull rather than run something unknown:

```bash
docker buildx imagetools inspect koblas/exemplar:<version> --format '{{json .Manifest.Digest}}'
```

## Building your own images

You only need this to run changes that aren't published, or to pull from your own registry. Build both images from the repository root (BuildKit, `linux/amd64` only):

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/Dockerfile -t exemplar:dev .
```

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/learning.Dockerfile -t exemplar-learning:dev .
```

Tag and push both to your registry, and look up each digest as in [Releases](releases.md). In the overlay, set each image's `newName` to your repository, for example `<registry>/exemplar` and `<registry>/exemplar-learning`, and its `digest` to what you pushed.
