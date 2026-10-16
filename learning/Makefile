# Learning-plane jobs (uv project). Mirrors the python job in
# .github/workflows/ci.yml; `make help` lists the targets. The root
# Makefile's py-* targets call these.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

.PHONY: sync
sync: ## Create .venv from uv.lock
	uv sync --locked

.PHONY: lint
lint: ## ruff check and format check
	uv run --frozen ruff check .
	uv run --frozen ruff format --check .

.PHONY: fmt
fmt: ## ruff fix and format
	uv run --frozen ruff check --fix .
	uv run --frozen ruff format .

.PHONY: test
test: ## pytest (cluster job test needs Docker; EXEMPLAR_REQUIRE_DB=1 to fail rather than skip)
	uv run --frozen pytest

.PHONY: contracts
contracts: ## Regenerate src/exemplar_learning/contracts from ../schema
	scripts/gen-contracts.sh

.PHONY: contracts-check
contracts-check: ## Fail if the contract models are stale
	scripts/gen-contracts.sh --check

.PHONY: check
check: sync lint test contracts-check ## Everything the CI python job runs

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
