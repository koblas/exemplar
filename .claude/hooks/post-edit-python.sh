#!/usr/bin/env bash
# PostToolUse hook for Edit, Write and MultiEdit: format and lint the edited
# learning-plane Python file (learning/**/*.py) with the project's ruff, so a
# lint error reaches Claude at once instead of at make lint. Go files are the
# pipeline plugin's: its own post-edit check runs the format steps in
# .claude/pipeline.json.
#
# Scoped to the one file. Exit 2 sends ruff's findings on that file back to
# Claude; anything else (another file type, no uv, no venv yet) is silent.
set -uo pipefail

root=${CLAUDE_PROJECT_DIR:-$PWD}
file=$(jq -r '.tool_input.file_path // empty')
[[ $file == /* ]] || file=$root/$file
case "$file" in
"$root"/learning/*.py) ;;
*) exit 0 ;;
esac
[[ -f $file ]] || exit 0
cd "$root/learning" || exit 0
rel=${file#"$root/learning/"}

# The locked ruff from learning/uv.lock: the venv's when `uv sync` made one,
# else through uv (which creates the venv on its first run).
if [[ -x .venv/bin/ruff ]]; then
	ruff=(.venv/bin/ruff)
elif command -v uv >/dev/null; then
	ruff=(uv run --frozen --quiet ruff)
else
	exit 0
fi

# --force-exclude keeps pyproject's extend-exclude (generated contracts) in force
# for a path named on the command line.
"${ruff[@]}" format --force-exclude --quiet "$rel" >/dev/null 2>&1 || true
if ! out=$("${ruff[@]}" check --fix --force-exclude --quiet "$rel" 2>&1); then
	printf 'ruff check learning/%s failed:\n%s\n' "$rel" "$out" >&2
	exit 2
fi
