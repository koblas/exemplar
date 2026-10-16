#!/usr/bin/env bash
# Generate the Python contract models from ../schema (the source of truth).
# With --check, fail if the committed models differ (CI).
#
# Models are always generated into a temp dir, outside the project, so the
# generator's formatting never depends on where the output lands; the
# result is then copied into the tree or compared with it.
set -euo pipefail

cd "$(dirname "$0")/.."
out=src/exemplar_learning/contracts
check=false
[[ "${1:-}" == "--check" ]] && check=true
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# schema:module pairs. Add one when a Python job needs another contract.
models="rule:rule global:global_config"

for pair in $models; do
  schema=${pair%%:*}
  module=${pair##*:}
  uv run --frozen datamodel-codegen \
    --input "../schema/${schema}.schema.json" \
    --input-file-type jsonschema \
    --output-model-type pydantic_v2.BaseModel \
    --target-python-version 3.13 \
    --use-standard-collections \
    --use-union-operator \
    --use-schema-description \
    --field-constraints \
    --disable-timestamp \
    --formatters ruff-check ruff-format \
    --output "${tmp}/${module}.py" 2>/dev/null
done

stale=false
for pair in $models; do
  module=${pair##*:}
  if $check; then
    if ! diff -u "${out}/${module}.py" "${tmp}/${module}.py"; then
      echo "contracts/${module}.py is stale: run learning/scripts/gen-contracts.sh" >&2
      stale=true
    fi
  else
    cp "${tmp}/${module}.py" "${out}/${module}.py"
  fi
done
$stale && exit 1
$check && echo "contracts up to date"
exit 0
