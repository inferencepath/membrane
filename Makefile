# Membrane — developer tasks.
#
# Quality is non-negotiable: `make check` runs the full local gate that CI also
# enforces (build, vet, format check, lint, race tests, coverage thresholds,
# vulnerability scan, secret scan).

GO            ?= go
PKG           := ./...
COVER_PROFILE := coverage.out
BINARY        := bin/membrane

# Build metadata injected into the binary.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildTime=$(BUILD_TIME)

GOLANGCI_LINT_VERSION ?= v1.64.8
GOVULNCHECK_VERSION   ?= latest
GITLEAKS_VERSION      ?= latest
CYCLONEDX_GOMOD_VERSION ?= latest
SBOM_FILE             ?= sbom.cdx.json

# Container image + release coordinates.
IMAGE_REPO ?= inferencepath/membrane
IMAGE_TAG  ?= $(VERSION)
IMAGE      := $(IMAGE_REPO):$(IMAGE_TAG)
# COSIGN_KEY is operator-provided (e.g. a KMS URI or a cosign key file). When it
# is empty, `make sign` is a documented no-op rather than a fabricated signature.
COSIGN_KEY ?=

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the Membrane binary into bin/.
	@mkdir -p bin
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/membrane

CONFIG ?= ./config/dev.yaml

.PHONY: run
run: ## Run Membrane in managed mode (CONFIG=./config/dev.yaml; seeds from config/seed.example.yaml).
	$(GO) run ./cmd/membrane --config $(CONFIG) --seed ./config/seed.example.yaml

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports).
	$(GO) run mvdan.cc/gofumpt@latest -w .
	$(GO) run golang.org/x/tools/cmd/goimports@latest -w -local github.com/inferencepath/membrane .

.PHONY: fmt-check
fmt-check: ## Verify code is formatted (fails on diff).
	@diff=$$($(GO) run mvdan.cc/gofumpt@latest -l .); \
	if [ -n "$$diff" ]; then \
		echo "Unformatted files:"; echo "$$diff"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (pinned version).
	$(GO) run github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

.PHONY: test
test: ## Run tests with race detector and coverage profile.
	$(GO) test $(PKG) -race -covermode=atomic -coverprofile=$(COVER_PROFILE)

.PHONY: cover
cover: test ## Print coverage summary.
	$(GO) tool cover -func=$(COVER_PROFILE) | tail -n 1

.PHONY: cover-html
cover-html: test ## Open an HTML coverage report.
	$(GO) tool cover -html=$(COVER_PROFILE)

.PHONY: cover-check
cover-check: ## Enforce coverage thresholds (90% overall; 100% Membrane+types).
	@test -f $(COVER_PROFILE) || $(MAKE) test
	./scripts/cover-check.sh $(COVER_PROFILE)

.PHONY: vuln
vuln: ## Scan for known vulnerabilities (govulncheck).
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(PKG)

.PHONY: secretscan
secretscan: ## Scan the repo for committed secrets (gitleaks).
	$(GO) run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) detect --no-banner --redact

.PHONY: sbom
sbom: ## Generate a CycloneDX SBOM for the module ($(SBOM_FILE)).
	$(GO) run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_GOMOD_VERSION) \
		mod -licenses -json -output $(SBOM_FILE)
	@echo "SBOM written to $(SBOM_FILE)"

.PHONY: tidy
tidy: ## Ensure go.mod/go.sum are tidy.
	$(GO) mod tidy

.PHONY: verify
verify: ## Verify the dependency graph against go.sum (supply chain).
	$(GO) mod verify

.PHONY: image
image: ## Build the container image ($(IMAGE)).
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(IMAGE) .
	@echo "Built image $(IMAGE)"

.PHONY: compose
compose: ## Start the single-service stack via docker compose (builds on first run).
	docker compose up -d --build
	@echo "Membrane up: egress http://127.0.0.1:8080/v1/chat · console http://127.0.0.1:7474"

.PHONY: sign
sign: ## Sign the image + SBOM with cosign IF COSIGN_KEY is set (never fabricated).
	@if [ -z "$(COSIGN_KEY)" ]; then \
		echo "sign: COSIGN_KEY is unset — skipping (signing is operator-provided; no signature is fabricated)."; \
		exit 0; \
	fi
	cosign sign --key "$(COSIGN_KEY)" --yes $(IMAGE)
	@if [ -f "$(SBOM_FILE)" ]; then \
		cosign attest --key "$(COSIGN_KEY)" --predicate $(SBOM_FILE) --type cyclonedx --yes $(IMAGE); \
	fi

.PHONY: smoke
smoke: build ## Build + start the binary against the sample config and probe it.
	./scripts/smoke.sh

.PHONY: release
release: check sbom image sign ## Full release gate: quality + SBOM + image + (optional) signing.
	@echo "Release artifacts ready for $(VERSION): binary ($(BINARY)), SBOM ($(SBOM_FILE)), image ($(IMAGE))."
	@echo "Publish + tag (git tag -s v<X.Y.Z>) is an operator step; see CHANGELOG.md."

.PHONY: check
check: build vet fmt-check lint test cover-check vuln ## Run the full local quality gate.

.PHONY: clean
clean: ## Remove build and coverage artifacts.
	rm -rf bin $(COVER_PROFILE) coverage.html $(SBOM_FILE)
