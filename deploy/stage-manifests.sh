#!/usr/bin/env bash
# Stages a release's Kustomize tree for the Flux OCI artifact
# (ghcr.io/koblas/exemplar-manifests): copies deploy/k8s (base, direct, core,
# components; only the YAML that Kustomize reads) and pins both images in the
# copy's core/kustomization.yaml, as render-release.sh's overlay does. Then
# renders the copy's base and direct and checks them against the
# install-tunnel.yaml and install-direct.yaml render-release.sh wrote, so the
# artifact and the release assets cannot drift apart. manifests.yml runs it
# after render-release.sh and pushes the copy with flux. It stages the
# deploy/k8s of the checkout it runs in, not its own, so the workflow can run
# this copy against an older release's tag. From the checkout root:
#   deploy/stage-manifests.sh sha256:<services> sha256:<learning> <rendered-dir> <out-dir>
# Needs kubectl (kustomize is built in).
set -euo pipefail

if [ $# -ne 4 ]; then
  echo "usage: $0 <services-digest> <learning-digest> <rendered-dir> <out-dir>" >&2
  exit 2
fi
services=$1 learning=$2 rendered=$3 out=$4
for d in "$services" "$learning"; do
  [[ $d =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "not a digest: $d" >&2; exit 2; }
done
[ ! -e "$out" ] || { echo "$out already exists" >&2; exit 2; }

root=$PWD
[ -d "$root/deploy/k8s/core" ] || { echo "run from the checkout root (no deploy/k8s/core here)" >&2; exit 2; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
(
  cd "$root/deploy/k8s"
  find base direct core components -type f -name '*.yaml' | while read -r f; do
    mkdir -p "$out/$(dirname "$f")"
    cp "$f" "$out/$f"
  done
)

# Each images entry is "- name: <image>" followed by its digest line; give it
# the Docker Hub repository and the release digest.
core=$out/core/kustomization.yaml
awk -v services="$services" -v learning="$learning" '
  /^  - name: / { image = $3 }
  /^    digest: / && image != "" {
    pin = image == "exemplar" ? services : image == "exemplar-learning" ? learning : ""
    if (pin != "") {
      print "    newName: docker.io/koblas/" image
      print "    digest: " pin
      pinned++
      image = ""
      next
    }
  }
  { print }
  END { if (pinned != 2) { print "pinned " pinned + 0 " images, want 2" > "/dev/stderr"; exit 1 } }
' "$core" > "$core.new"
mv "$core.new" "$core"

if grep -rq 'sha256:0000000000000000000000000000000000000000000000000000000000000000' "$out"; then
  echo "$out still has an unpinned image" >&2
  exit 1
fi

# render-release.sh heads each file with comment lines; compare what follows.
for build in base direct; do
  name=tunnel
  [ "$build" = direct ] && name=direct
  if ! diff -u <(awk 'body || !/^#/ { body = 1; print }' "$rendered/install-$name.yaml") \
    <(kubectl kustomize "$out/$build") >&2; then
    echo "$out/$build renders differently from $rendered/install-$name.yaml" >&2
    exit 1
  fi
done
echo "staged $out (base matches install-tunnel.yaml, direct matches install-direct.yaml)"
