# The Go services image: every cmd/ binary, plus the engines and tools they
# run (git for checkouts and mirrors, ripgrep for the agents' grep tool, universal-ctags for exemplar-search
# symbols, ast-grep and Semgrep for compiled rules and compile attempts, clang-format
# for fix validation, unshare and prlimit for the compile sandbox). One image, one Deployment or
# StatefulSet per binary. Build from the repository root:
#
#   docker build -f deploy/docker/Dockerfile -t exemplar .
#
# Every base image is pinned by digest; ast-grep by release checksum. The
# image is linux/amd64 only (the ast-grep release binary). Debian packages
# float within the pinned base snapshot.

FROM golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# cgo: internal/syntax embeds the tree-sitter grammars. VERSION is the
# release tag (images.yml); binaries without main.version ignore it.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ ./cmd/...

FROM debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a AS astgrep
ADD --checksum=sha256:76fb6555be6734fb5057dba8d2fb756430f374bb9e1af694cf1ce00e13238d63 \
    https://github.com/ast-grep/ast-grep/releases/download/0.45.1/app-x86_64-unknown-linux-gnu.zip /tmp/ast-grep.zip
RUN apt-get update -q && apt-get install -y -q --no-install-recommends unzip \
    && unzip /tmp/ast-grep.zip ast-grep -d /opt/ast-grep

FROM debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
RUN apt-get update -q \
    && apt-get install -y -q --no-install-recommends ca-certificates clang-format git openssh-client ripgrep universal-ctags util-linux python3 python3-venv \
    && rm -rf /var/lib/apt/lists/*
# Same Semgrep version as devenv.nix and CI.
RUN python3 -m venv /opt/semgrep && /opt/semgrep/bin/pip install --no-cache-dir semgrep==1.172.0 \
    && ln -s /opt/semgrep/bin/semgrep /usr/local/bin/semgrep
COPY --from=astgrep /opt/ast-grep/ast-grep /usr/local/bin/ast-grep
COPY --from=build /out/ /usr/local/bin/
# The read-only root filesystem leaves HOME and TMPDIR to mounted volumes.
ENV HOME=/tmp TMPDIR=/tmp SEMGREP_SEND_METRICS=off SEMGREP_ENABLE_VERSION_CHECK=0
USER 65532:65532
