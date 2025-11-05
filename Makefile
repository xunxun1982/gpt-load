# Default target
.DEFAULT_GOAL := help

# ==============================================================================
# Build Variables
# ==============================================================================
VERSION ?= dev
BINARY_NAME := gpt-load
LDFLAGS := -s -w -X gpt-load/internal/version.Version=$(VERSION)
BUILD_FLAGS := -trimpath -ldflags="$(LDFLAGS)"

# ==============================================================================
# Build Targets
# ==============================================================================
.PHONY: build
build: ## Build production binary (optimized)
	@echo "🔨 Building production binary..."
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(BINARY_NAME)
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
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY_NAME)-linux-amd64

.PHONY: build-linux-arm64
build-linux-arm64: ## Build for Linux ARM64
	@echo "🐧 Building for Linux ARM64..."
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -o $(BINARY_NAME)-linux-arm64

.PHONY: build-windows-amd64
build-windows-amd64: ## Build for Windows AMD64
	@echo "🪟 Building for Windows AMD64..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY_NAME)-windows-amd64.exe

.PHONY: build-darwin-amd64
build-darwin-amd64: ## Build for macOS AMD64
	@echo "🍎 Building for macOS AMD64..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY_NAME)-darwin-amd64

.PHONY: build-darwin-arm64
build-darwin-arm64: ## Build for macOS ARM64
	@echo "🍎 Building for macOS ARM64..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(BUILD_FLAGS) -o $(BINARY_NAME)-darwin-arm64

# ==============================================================================
# Run & Development
# ==============================================================================
.PHONY: run
run: ## Build frontend and run server
	@echo "--- Building frontend... ---"
	cd web && npm install && npm run build
	@echo "--- Preparing backend... ---"
	@echo "--- Starting backend... ---"
	go run ./main.go

.PHONY: dev
dev: ## Run in development mode (with race detection)
	@echo "🔧 Starting development mode..."
	go run -race ./main.go

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
	go run ./main.go migrate-keys $(ARGS)

.PHONY: help
help: ## Display this help message
	@awk 'BEGIN {FS = ":.*?## "; printf "Usage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*?## / { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
