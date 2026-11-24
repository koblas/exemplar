#!/usr/bin/env bash
# PostToolUse hook for Edit and Write: format the edited Go file and build its
# package, so a compile error reaches Claude at once instead of at make check.
# Exit 2 sends stderr back to Claude; anything else is silent.
set -euo pipefail

file=$(jq -r '.tool_input.file_path // empty')
[[ "$file" == *.go && -f "$file" ]] || exit 0

cd "${CLAUDE_PROJECT_DIR:-.}"
command -v go >/dev/null || exit 0

# The formatters make fmt runs (gci, gofumpt), else plain gofmt.
if command -v golangci-lint >/dev/null; then
	golangci-lint fmt "$file" >/dev/null 2>&1 || true
else
	gofmt -w "$file" 2>/dev/null || true
fi

dir=$(dirname "$file")
dir=${dir#"$PWD"/}
[[ "$dir" == /* ]] && exit 0 # outside this checkout
# go vet type-checks the package and its _test.go files, which go build skips.
if ! out=$(go vet "./$dir" 2>&1); then
	printf 'go vet %s failed after editing %s:\n%s\n' "$dir" "$file" "$out" >&2
	exit 2
fi
