#!/usr/bin/env bash
# Runs go test for the packages changed against a base (default origin/main)
# and every package whose tests import them (make test-changed).
#
#   test-changed.sh [base] [go test flags...]
#
# Changes are committed, staged, unstaged and untracked files since the merge
# base. A changed file counts for the Go package in its directory or the
# nearest one above it, so embedded templates, manifests read by ./deploy and
# pages read by ./docs select their package. A go.mod or go.sum change runs
# everything.
set -euo pipefail

base=${1:-origin/main}
shift || true
cd "$(git rev-parse --show-toplevel)"
mb=$(git merge-base "$base" HEAD)

changed=$( { git diff --name-only "$mb"; git ls-files --others --exclude-standard; } | sort -u)
if [[ -z $changed ]]; then
  echo "test-changed: nothing changed against $base"
  exit 0
fi
if grep -qx -e go.mod -e go.sum <<<"$changed"; then
  echo "test-changed: go.mod or go.sum changed, testing ./..."
  exec go test "$@" ./...
fi

module=$(go list -m)
declare -A pkgdirs=() seeds=()
while read -r dir; do pkgdirs[$dir]=1; done < <(go list -f '{{.Dir}}' ./... | sed "s|^$PWD/\{0,1\}||; s|^$|.|")

while read -r f; do
  d=$(dirname "$f")
  while [[ -z ${pkgdirs[$d]:-} && $d != . ]]; do d=$(dirname "$d"); done
  [[ -n ${pkgdirs[$d]:-} ]] && seeds[$d]=1
done <<<"$changed"
if [[ ${#seeds[@]} -eq 0 ]]; then
  echo "test-changed: no Go package affected"
  exit 0
fi

# Every package whose test binary depends on a seed: go list -test lists the
# test variants ("p [p.test]", "p_test [p.test]", "p.test") with their deps.
seedpkgs=()
for d in "${!seeds[@]}"; do
  if [[ $d == . ]]; then seedpkgs+=("$module"); else seedpkgs+=("$module/$d"); fi
done
pkgs=$(go list -test -f '{{.ImportPath}} {{join .Deps " "}}' ./... |
  awk -v seeds="${seedpkgs[*]}" '
    BEGIN { n = split(seeds, s, " "); for (i = 1; i <= n; i++) want[s[i]] = 1 }
    {
      p = $1; sub(/ \[.*$/, "", p); sub(/\.test$/, "", p); sub(/_test$/, "", p)
      if (p in want) { print p; next }
      for (i = 2; i <= NF; i++) if ($i in want) { print p; next }
    }' | sort -u)

echo "test-changed: $(wc -l <<<"$pkgs" | tr -d ' ') packages"
sed 's/^/  /' <<<"$pkgs"
# shellcheck disable=SC2086
exec go test "$@" $pkgs
