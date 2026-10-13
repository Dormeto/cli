# Local builds of dormeto. `make` lists the targets. The release itself is scripts/release.sh (README, Releasing).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
BIN     := bin/dormeto
MANAGER ?= ../manager

.DEFAULT_GOAL := help
.PHONY: help build install run test fmt snapshot parity dry-run clean

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/dormeto for this machine, as a release builds it
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/dormeto
	@$(BIN) --version

install: ## Install dormeto into $(go env GOPATH)/bin
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/dormeto

run: build ## Run it: make run ARGS="estimate --profile <profile> --uptime"
	$(BIN) $(ARGS)

test: ## go vet, the Go tests (shared cases and end-to-end included), the npm launcher's and release scripts' tests
	go vet ./...
	go test ./...
	npm test

fmt: ## Format the Go sources
	gofmt -w $$(git ls-files '*.go')

snapshot: ## Build the release's archives, checksums, cask and npm packages into dist/, publishing nothing
	goreleaser release --snapshot --clean
	node npm/assemble.mjs

parity: ## Export the shared cases again from the manager checkout (MANAGER=../manager)
	cd $(MANAGER) && node scripts/parity/export.mjs --out $(CURDIR)/testdata/parity

dry-run: ## Check a release without publishing: make dry-run VERSION=0.1.0
	scripts/release.sh $(VERSION) --dry-run

clean: ## Remove bin/ and dist/
	rm -rf bin dist
