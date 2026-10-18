# The CLI

**Download.** Tagged releases attach archives for linux (amd64, arm64) and darwin (arm64, amd64) with `SHA256SUMS`. Download one from the GitHub release, check its checksum, and put `exemplar` on your `PATH`.

**Build from source.** The CLI embeds the tree-sitter grammars, so it needs cgo and a C compiler:

```bash
CGO_ENABLED=1 go build -o exemplar ./cmd/exemplar
```

**Sign in** with the App's client id (`client-id` in `exemplar-github-app`), or `--oidc` with the OIDC CLI client:

```bash
exemplar login --api https://exemplar.acme.com --client-id <Client ID>
```

**Review your working tree before opening a PR.** It exits 1 when there are findings:

```bash
exemplar review
```
