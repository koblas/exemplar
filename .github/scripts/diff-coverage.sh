#!/usr/bin/env bash
# Every Go block a change adds or changes runs in a test (ci.yml diff-coverage).
#
#   diff-coverage.sh BASE HEAD
#
# Judges the diff from the merge base of BASE and HEAD to HEAD, which must be
# the checked-out tree. The packages tested are the directories of the changed
# .go files (generated sqlc code excluded); a block only another package's
# tests reach counts as uncovered. The check is the pipeline plugin's
# scripts/diff-coverage.py, from a koblas/throughline checkout at $THROUGHLINE
# (default .throughline). A block that cannot run in a test says why on its
# opening line: `// uncovered: <why>`.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
base=$(git merge-base "$1" "$2")
head=$2

pkgs=$(git diff --name-only "$base" "$head" -- '*.go' |
	{ grep -v '^internal/store/queries/' || true; } |
	xargs -r -n1 dirname | sort -u |
	while read -r d; do if [[ -d $d ]]; then echo "./$d"; fi; done)
if [[ -z $pkgs ]]; then
	echo "diff-coverage: no Go package changed"
	exit 0
fi

# shellcheck disable=SC2086
exec python3 "${THROUGHLINE:-.throughline}/plugins/pipeline/scripts/diff-coverage.py" \
	--base "$base" --to "$head" $pkgs
