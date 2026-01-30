# Default target
.DEFAULT_GOAL := help

# ==============================================================================
# Build Variables
# ==============================================================================
VERSION ?= dev
BINARY_NAME := gpt-load
# Optimization flags for smallest size and best performance:
# -s: Omit symbol table and debug info (~20% size reduction)
# -w: Omit DWARF symbol table (~10% size reduction)
# -X: Set version info at build time
LDFLAGS := -s -w -X gpt-load/internal/version.Version=$(VERSION)
# Use go_json tag to enable goccy/go-json for gin framework (2-3x faster than encoding/json)
GOTAGS := go_json
# Build flags:
# -tags: Build tags for conditional compilation
# -trimpath: Remove file system paths from binary (smaller size, reproducible builds)
# -buildvcs=false: Skip VCS info for reproducible builds
BUILD_FLAGS := -tags $(GOTAGS) -trimpath -buildvcs=false -ldflags="$(LDFLAGS)"
# PGO profile file (Go compiler automatically detects this file)
PGO_PROFILE := default.pgo
# Allow extra go flags to be passed via environment
GOFLAGS ?=
# CPU Architecture Level: v2 (SSE4.2, POPCNT) is safe for most CPUs
# v3 requires AVX/AVX2 which may not be available on older CPUs
export GOAMD64 ?= v2

# ==============================================================================
# PGO (Profile-Guided Optimization) Targets
# ==============================================================================
.PHONY: pgo-profile
pgo-profile: ## Collect PGO profile from tests and benchmarks
	@echo "🔍 Collecting PGO profile..."
	@if [ -f "$(PGO_PROFILE)" ]; then \
		echo "⚠️  Removing existing profile: $(PGO_PROFILE)"; \
		rm -f "$(PGO_PROFILE)"; \
	fi
	@chmod +x scripts/collect-pgo-profile.sh
	@./scripts/collect-pgo-profile.sh
	@echo "✅ PGO profile ready: $(PGO_PROFILE)"

.PHONY: pgo-build
pgo-build: pgo-profile ## Build with PGO optimization (collect profile + build)
	@echo "🔨 Building with PGO optimization..."
	@$(MAKE) build
	@echo "✅ PGO-optimized build complete"

.PHONY: pgo-clean
pgo-clean: ## Remove PGO profile and profile directory
	@echo "🧹 Cleaning PGO artifacts..."
	@rm -f "$(PGO_PROFILE)"
	@rm -rf profiles/
	@echo "✅ PGO artifacts cleaned"

# ==============================================================================
# Build Targets
# ==============================================================================
.PHONY: build
build: ## Build production binary (optimized)
	@echo "🔨 Building production binary..."
	@if [ -f "$(PGO_PROFILE)" ]; then \
		echo "✅ Using PGO profile for optimization"; \
	else \
		echo "ℹ️  Building without PGO (run 'make pgo-profile' to enable PGO)"; \
	fi
	CGO_ENABLED=0 go build $(GOFLAGS) $(BUILD_FLAGS) -o $(BINARY_NAME)
	@echo "✅ Build complete: $(BINARY_NAME)"

.PHONY: build-all
build-all: ## Build for all platforms
	@echo "🌍 Building for all platforms..."
	@$(MAKE) build-linux-amd64
	@$(MAKE) build-linux-arm64
	@$(MAKE) build-windows-amd64
	@$(MAKE) build-darwin-amd64
	@$(MAKE) build-darwin-arm64
	@echo "✅ All builds complete"

.PHONY: build-linux-amd64
build-linux-amd64: ## Build for Linux AMD64
	@echo "🐧 Building for Linux AMD64..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(BUILD_FLAGS) -o $(BINARY_NAME)-linux-amd64

.PHONY: build-linux-arm64
build-linux-arm64: ## Build for Linux ARM64
	@echo "🐧 Building for Linux ARM64..."
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(BUILD_FLAGS) -o $(BINARY_NAME)-linux-arm64

.PHONY: build-windows-amd64
build-windows-amd64: ## Build for Windows AMD64
	@echo "🪟 Building for Windows AMD64..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) $(BUILD_FLAGS) -o $(BINARY_NAME)-windows-amd64.exe

.PHONY: build-darwin-amd64
build-darwin-amd64: ## Build for macOS AMD64
	@echo "🍎 Building for macOS AMD64..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) $(BUILD_FLAGS) -o $(BINARY_NAME)-darwin-amd64

.PHONY: build-darwin-arm64
build-darwin-arm64: ## Build for macOS ARM64
	@echo "🍎 Building for macOS ARM64..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) $(BUILD_FLAGS) -o $(BINARY_NAME)-darwin-arm64

# ==============================================================================
# Run & Development
# ==============================================================================
.PHONY: run
run: ## Build frontend and run server (local development)
	@echo "--- Building frontend... ---"
	# npm install: Fast incremental install for local development
	cd web && npm install && npm run build
	@echo "--- Starting backend... ---"
	# Equivalent to: go run -tags go_json ./main.go
	go run -tags $(GOTAGS) ./main.go

.PHONY: run-ci
run-ci: ## Build frontend and run server (CI/CD - clean install)
	@echo "--- Building frontend (CI mode)... ---"
	# npm ci: Clean install for reproducible CI/CD builds
	cd web && npm ci && npm run build
	@echo "--- Starting backend... ---"
	go run -tags $(GOTAGS) ./main.go

.PHONY: quick-run
quick-run: ## Quick run (skip npm install, assumes dependencies are ready)
	@echo "--- Building frontend (quick mode)... ---"
	cd web && npm run build
	@echo "--- Starting backend... ---"
	go run -tags $(GOTAGS) ./main.go

.PHONY: dev
dev: ## Run in development mode (with race detection)
	@echo "🔧 Starting development mode..."
	# Equivalent to: go run -tags go_json -race ./main.go
	go run -tags $(GOTAGS) -race ./main.go

# ==============================================================================
# Testing & Quality
# ==============================================================================
.PHONY: test
test: ## Run all tests
	@echo "🧪 Running tests..."
	go test -tags $(GOTAGS) ./... -v -count=1

.PHONY: vet
vet: ## Run go vet
	@echo "🔍 Running go vet..."
	go vet -tags $(GOTAGS) ./...

.PHONY: check
check: vet test ## Run all checks (vet + test)
	@echo "✅ All checks passed"

# ==============================================================================
# Docker
# ==============================================================================
.PHONY: docker-build
docker-build: ## Build Docker image
	@echo "🐳 Building Docker image..."
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY_NAME):$(VERSION) .
	@echo "✅ Docker image built: $(BINARY_NAME):$(VERSION)"

# ==============================================================================
# Key Migration
# ==============================================================================
.PHONY: migrate-keys
migrate-keys: ## Execute key migration (usage: make migrate-keys ARGS="--from old --to new")
	@echo "🔑 Executing key migration..."
	@if [ -z "$(ARGS)" ]; then \
		echo "Usage:"; \
		echo "  Enable encryption: make migrate-keys ARGS=\"--to new-key\""; \
		echo "  Disable encryption: make migrate-keys ARGS=\"--from old-key\""; \
		echo "  Change key: make migrate-keys ARGS=\"--from old-key --to new-key\""; \
		echo ""; \
		echo "⚠️  Important: Always backup database before migration!"; \
		exit 1; \
	fi
	# Equivalent to: go run -tags go_json ./main.go migrate-keys $(ARGS)
	go run -tags $(GOTAGS) ./main.go migrate-keys $(ARGS)

.PHONY: help
help: ## Display this help message
	@awk 'BEGIN {FS = ":.*?## "; printf "Usage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*?## / { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""
	@echo "PGO (Profile-Guided Optimization):"
	@echo "  PGO improves performance by 3-7% through better compiler optimizations"
	@echo "  Run 'make pgo-build' to build with PGO, or 'make pgo-profile' then 'make build'"
