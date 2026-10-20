# How to build your own images

You only need this to run changes that aren't published, or to pull from your own registry. The published images are described on [Releases and images](../../reference/releases.md).

## 1. Build both images

From the repository root, with BuildKit, for `linux/amd64` only:

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/Dockerfile -t exemplar:dev .
```

```bash
docker buildx build --platform linux/amd64 --load -f deploy/docker/learning.Dockerfile -t exemplar-learning:dev .
```

`make images` does the same (it needs Docker, which devenv does not provide).

## 2. Push and look up the digests

Tag and push both to your registry, then read each digest:

```bash
docker buildx imagetools inspect <registry>/exemplar:<tag> --format '{{json .Manifest.Digest}}'
```

## 3. Pin them in the overlay

Set each image's `newName` to your repository and its `digest` to what you pushed:

```yaml
images:
  - name: exemplar
    newName: <registry>/exemplar
    digest: sha256:<services digest>
  - name: exemplar-learning
    newName: <registry>/exemplar-learning
    digest: sha256:<learning digest>
```

The base keeps the logical names `exemplar` and `exemplar-learning`; its all-zero digests make an unpinned deploy fail to pull rather than run something unknown.
