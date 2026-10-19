# Local builds of dormeto. `make` lists the targets. The release itself is scripts/release.sh (README, Releasing).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
BIN     := bin/dormeto
MANAGER ?= ../manager
# Where make install puts dormeto: the install script's default, before Homebrew's bin in a usual PATH.
INSTALL_DIR ?= $(HOME)/.local/bin

.DEFAULT_GOAL := help
.PHONY: help build install uninstall run test fmt snapshot parity dry-run clean

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/dormeto for this machine, as a release builds it
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/dormeto
	@$(BIN) --version

install: ## Install this checkout's dormeto into INSTALL_DIR (~/.local/bin), and say which dormeto the PATH runs
	@mkdir -p "$(INSTALL_DIR)"
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o "$(INSTALL_DIR)/dormeto" ./cmd/dormeto
	@"$(INSTALL_DIR)/dormeto" version | head -n 1
	@found="$$(command -v dormeto || true)"; \
	if [ "$$found" != "$(INSTALL_DIR)/dormeto" ]; then \
	  echo "warning: dormeto on the PATH is $${found:-none}, not $(INSTALL_DIR)/dormeto:" \
	    "put $(INSTALL_DIR) before it in PATH (or brew uninstall --cask dormeto), then run rehash (zsh) or hash -r (bash)"; \
	else \
	  echo "dormeto on the PATH is $$found (an open shell may need rehash in zsh, hash -r in bash)"; \
	fi

uninstall: ## Remove the dormeto make install put in INSTALL_DIR
	rm -f "$(INSTALL_DIR)/dormeto"

run: build ## Run it: make run ARGS="estimate --profile <profile> --uptime"
	$(BIN) $(ARGS)

test: ## go vet, the Go tests (shared cases and end-to-end included), the release scripts' tests
	go vet ./...
	go test ./...
	npm test

fmt: ## Format the Go sources
	gofmt -w $$(git ls-files '*.go')

snapshot: ## Build the release's archives, checksums and cask into dist/, publishing nothing
	goreleaser release --snapshot --clean

parity: ## Export the shared cases again from the manager checkout (MANAGER=../manager)
	cd $(MANAGER) && node scripts/parity/export.mjs --out $(CURDIR)/testdata/parity

dry-run: ## Check a release without publishing: make dry-run VERSION=0.1.0
	@if [ -z "$(VERSION)" ] || [ "$(origin VERSION)" = file ]; then echo "VERSION is required: make dry-run VERSION=x.y.z" >&2; exit 2; fi
	scripts/release.sh $(VERSION) --dry-run

clean: ## Remove bin/ and dist/
	rm -rf bin dist
