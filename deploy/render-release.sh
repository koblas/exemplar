#!/usr/bin/env bash
# Renders a release's install manifests with both images pinned by digest:
#   install-tunnel.yaml    deploy/k8s/base (Cloudflare Tunnel)
#   install-direct.yaml    deploy/k8s/direct (Ingress)
# release-please.yml runs it after images.yml has pushed the release's
# images, and attaches the files to the GitHub release. By hand:
#   deploy/render-release.sh v0.2.0 sha256:<services> sha256:<learning> out/
# Needs kubectl (kustomize is built in).
set -euo pipefail

if [ $# -ne 4 ]; then
  echo "usage: $0 <tag> <services-digest> <learning-digest> <out-dir>" >&2
  exit 2
fi
tag=$1 services=$2 learning=$3 out=$4
for d in "$services" "$learning"; do
  [[ $d =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "not a digest: $d" >&2; exit 2; }
done
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]] || { echo "not a release tag: $tag" >&2; exit 2; }

root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
work=$(mktemp -d "$root/deploy/k8s/.release-XXXXXX")
trap 'rm -rf "$work"' EXIT

for build in base direct; do
  name=tunnel
  [ "$build" = direct ] && name=direct
  mkdir -p "$work/$build"
  cat > "$work/$build/kustomization.yaml" <<YAML
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../$build]
images:
  - {name: exemplar, newName: docker.io/koblas/exemplar, digest: $services}
  - {name: exemplar-learning, newName: docker.io/koblas/exemplar-learning, digest: $learning}
YAML
  {
    echo "# Exemplar $tag, $name build (deploy/k8s/$build), images pinned by digest."
    echo "# Set EXEMPLAR_CONFIG_REPO and EXEMPLAR_API_URL in exemplar-settings (docs/guides/install/production.md), or"
    echo "# use this file as a kustomize resource and patch them."
    kubectl kustomize "$work/$build"
  } > "$out/install-$name.yaml"
  if grep -q 'sha256:0000000000000000000000000000000000000000000000000000000000000000' "$out/install-$name.yaml"; then
    echo "install-$name.yaml still has an unpinned image" >&2
    exit 1
  fi
done
echo "rendered $out/install-tunnel.yaml and $out/install-direct.yaml"
