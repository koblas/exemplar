# Developer and CI commands. Each target mirrors a job or step in
# .github/workflows/ci.yml; `make help` lists them. Run inside devenv
# (`devenv shell` or direnv), which provides Go, golangci-lint, the engines
# and uv. Docker and kubectl are not in devenv: `images` and `manifests`
# need them on PATH.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# Tool versions run through `go run`, so they need no install. Same as
# .github/workflows/ci.yml.
SQLC_VERSION        ?= v1.31.1
GOVULNCHECK_VERSION ?= v1.8.0
KUBECONFORM_VERSION ?= v0.7.0
KUBERNETES_VERSION  ?= 1.33.0
MKDOCS_VERSION      ?= 1.6.1
MKDOCS_MATERIAL_VERSION ?= 9.6.14
MKDOCS := uvx --from mkdocs==$(MKDOCS_VERSION) --with mkdocs-material==$(MKDOCS_MATERIAL_VERSION) mkdocs

# Build outputs, replay results and rendered manifests (gitignored).
OUT     := out
BINS    := $(notdir $(wildcard cmd/*))
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Under devenv the nix cc wrapper's purity check rejects the tree-sitter
# grammars' cgo include paths in the module cache ("parser.h not found").
# Outside nix the variable has no effect.
export NIX_ENFORCE_PURITY := 0

FUZZTIME ?= 30s
CONFIG   := examples/exemplar-config
K8S_BUILDS := base direct both postgres

##@ Go

.PHONY: build
build: ## Build all packages
	go build ./...

.PHONY: bins
bins: ## Build every cmd/ binary into out/bin
	@mkdir -p $(OUT)/bin
	for b in $(BINS); do \
	  go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(OUT)/bin/$$b ./cmd/$$b; \
	done

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: test
test: ## Run tests (Postgres and engine tests skip without Docker/engines)
	go test ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	go test -race ./...

.PHONY: lint
lint: ## golangci-lint run
	golangci-lint run

.PHONY: fmt
fmt: ## Format Go code with the configured formatters (gci, gofmt, gofumpt)
	golangci-lint fmt

.PHONY: fmt-check
fmt-check: ## Fail if Go code is not formatted
	golangci-lint fmt --diff

.PHONY: vuln
vuln: ## govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: fuzz
fuzz: ## Short fuzz runs (FUZZTIME=30s)
	go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime=$(FUZZTIME) ./internal/scope
	go test -run='^$$' -fuzz='^FuzzValidateAnchor$$' -fuzztime=$(FUZZTIME) ./internal/scope

##@ Generated code

.PHONY: sqlc
sqlc: ## Regenerate internal/store/queries from db/queries
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate -f db/sqlc.yaml

.PHONY: sqlc-check
sqlc-check: ## Fail if internal/store/queries is stale
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) diff -f db/sqlc.yaml

.PHONY: contracts
contracts: ## Regenerate the Python contract models from schema/
	$(MAKE) -C learning contracts

.PHONY: generate
generate: sqlc contracts ## Regenerate all generated code

##@ Config evals

.PHONY: adversarial
adversarial: ## Validate the example config, run the adversarial set and a scripted replay
	@mkdir -p $(OUT)
	go run ./cmd/exemplar validate $(CONFIG)
	go run ./cmd/exemplar adversarial --config $(CONFIG) --mode compromised
	go run ./cmd/exemplar replay --config $(CONFIG) --fixtures examples/fixtures --scripted --baseline --out $(OUT)/replay.json

##@ Python (learning/)

.PHONY: py-sync py-lint py-test py-check
py-sync: ## uv sync --locked
	$(MAKE) -C learning sync
py-lint: ## ruff check and format check
	$(MAKE) -C learning lint
py-test: ## pytest
	$(MAKE) -C learning test
py-check: ## Everything the CI python job runs
	$(MAKE) -C learning check

##@ Deploy (needs Docker / kubectl)

.PHONY: images
images: ## Build the service and learning images (nothing is pushed)
	@command -v docker >/dev/null || { echo "docker is required (not provided by devenv)" >&2; exit 1; }
	docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(VERSION) -t exemplar:dev .
	docker build -f deploy/docker/learning.Dockerfile -t exemplar-learning:dev .

# "both" is the default build plus the ingress component, and "postgres" the
# default build plus the in-cluster Postgres component, as in CI. Their
# overlays are written into deploy/k8s (they refer to ../base) and removed
# afterwards.
.PHONY: manifests
manifests: ## Render each Kustomize build and the example overlay to out/ and schema-check them
	@command -v kubectl >/dev/null || { echo "kubectl is required (not provided by devenv)" >&2; exit 1; }
	@mkdir -p $(OUT)
	both=deploy/k8s/.make-both; pg=deploy/k8s/.make-postgres; trap 'rm -rf "$$both" "$$pg"' EXIT; mkdir -p "$$both" "$$pg"; \
	printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [../base]\ncomponents: [../components/ingress]\n' > "$$both/kustomization.yaml"; \
	printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [../base]\ncomponents: [../components/postgres]\n' > "$$pg/kustomization.yaml"; \
	for v in $(K8S_BUILDS); do \
	  dir=deploy/k8s/$$v; [ "$$v" = both ] && dir=$$both; [ "$$v" = postgres ] && dir=$$pg; \
	  kubectl kustomize "$$dir" > $(OUT)/rendered-$$v.yaml; \
	  go run github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION) -strict -summary -kubernetes-version $(KUBERNETES_VERSION) $(OUT)/rendered-$$v.yaml; \
	done; \
	for ex in acme quickstart; do \
	  kubectl kustomize examples/k8s/$$ex > $(OUT)/rendered-example-$$ex.yaml; \
	  go run github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION) -strict -summary -kubernetes-version $(KUBERNETES_VERSION) $(OUT)/rendered-example-$$ex.yaml; \
	done; \
	deploy/render-release.sh v0.0.0 "sha256:$$(printf '1%.0s' $$(seq 64))" "sha256:$$(printf '2%.0s' $$(seq 64))" $(OUT)/release; \
	go run github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION) -strict -summary -kubernetes-version $(KUBERNETES_VERSION) $(OUT)/release/install-*.yaml

.PHONY: deploy-test
deploy-test: ## Check every build's manifests against the code (go test ./deploy)
	go test ./deploy

##@ Commits and releases

# CI checks the pull request's commits (base..head).
COMMIT_RANGE ?= origin/main..HEAD

.PHONY: commitlint
commitlint: ## Check commit subjects are Conventional Commits (COMMIT_RANGE=origin/main..HEAD)
	.github/scripts/commitlint.sh $(COMMIT_RANGE)

.PHONY: hooks
hooks: ## Use .githooks (commit-msg lint) in this clone
	git config core.hooksPath .githooks

##@ Aggregates

.PHONY: check
check: build vet lint fmt-check test ## Quick local gate: build, vet, lint, format, tests

.PHONY: docs
docs: ## Build the documentation site to out/site, failing on broken links (as ci.yml's docs job)
	$(MKDOCS) build --strict -d $(OUT)/site

.PHONY: docs-serve
docs-serve: ## Preview the documentation site at http://127.0.0.1:8000
	$(MKDOCS) serve

.PHONY: ci
ci: export EXEMPLAR_REQUIRE_DB = 1
ci: export EXEMPLAR_REQUIRE_ENGINES = 1
ci: build vet test test-race lint sqlc-check vuln fuzz adversarial py-check commitlint docs ## What ci.yml runs, minus images/manifests (DB and engine tests must not skip)

.PHONY: clean
clean: ## Remove out/
	rm -rf $(OUT)

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} \
	  /^##@/ { printf "\n%s\n", substr($$0, 5); next } \
	  /^[a-zA-Z_-]+:.*##/ { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
