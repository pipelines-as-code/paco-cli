BINARY      = paco
OUTPUT_DIR  = bin
GO          = go
GOFLAGS     = -mod=vendor
LDFLAGS     = -s -w

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the paco binary
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTPUT_DIR)/$(BINARY) ./cmd/paco

.PHONY: test
test: ## Run tests with race detection
	$(GO) test $(GOFLAGS) -race -failfast ./...

.PHONY: test-no-cache
test-no-cache: ## Run tests without cache
	$(GO) clean -testcache
	$(MAKE) test

.PHONY: coverage
coverage: ## Generate coverage profile
	$(GO) test $(GOFLAGS) -race -coverprofile=coverage.out -covermode=atomic ./...

.PHONY: html-coverage
html-coverage: coverage ## Open coverage report in browser
	$(GO) tool cover -html=coverage.out

.PHONY: lint
lint: lint-go lint-fmt ## Run all linters

.PHONY: lint-go
lint-go: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: lint-fmt
lint-fmt: ## Check Go formatting
	@test -z "$$(gofumpt -l .)" || { echo "Run 'make fumpt' to fix formatting"; gofumpt -l .; exit 1; }

.PHONY: fumpt
fumpt: ## Format Go files with gofumpt
	gofumpt -w .

.PHONY: fix-linters
fix-linters: fumpt ## Auto-fix linting issues

.PHONY: vendor
vendor: ## Update vendor directory
	$(GO) mod tidy
	$(GO) mod vendor

.PHONY: check
check: lint test ## Run lint and test (CI entry point)

.PHONY: all
all: build test lint ## Build, test, and lint

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(OUTPUT_DIR) coverage* *.out
