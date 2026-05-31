# =============================================================================
# Frontend build stage
# =============================================================================
FROM node:26.2.0-alpine3.23 AS node-builder

ARG VERSION=1.0.0
WORKDIR /build

# Leverage Docker layer caching for dependencies
COPY ./web/package*.json ./
RUN npm ci --no-audit

COPY ./web .
RUN VITE_VERSION=${VERSION} npm run build


# =============================================================================
# Go build stage with PGO optimization
# =============================================================================
FROM golang:1.26.3-alpine3.23 AS go-builder

ARG VERSION=1.0.0
ARG TARGETARCH
ARG TARGETOS=linux
ARG TZ=Asia/Shanghai

# CPU Architecture Level: v2 (SSE4.2, POPCNT) for better compatibility (amd64 only)
# v2 is safe for most CPUs, v3 requires AVX2 which may not be available
ARG GOAMD64=v2

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    TZ=${TZ}

WORKDIR /build

# Install ca-certificates for HTTPS support in scratch image
# Alpine's ca-certificates package provides Mozilla's trusted CA bundle
RUN apk add --no-cache ca-certificates tzdata

# Leverage Docker layer caching for Go modules
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
COPY --from=node-builder /build/dist ./web/dist

# Copy PGO profile if available (will be provided by GitHub Actions)
# The profile is optional - build will work without it but won't be PGO-optimized
COPY default.pgo* ./

# Optimized build with PGO for best performance:
# - CGO_ENABLED=0: Static binary, no C dependencies
# - -tags go_json: High-performance JSON (goccy/go-json, 2-3x faster)
# - -trimpath: Remove file system paths from binary (smaller size)
# - -buildvcs=false: Skip VCS stamping for reproducible builds
# - -ldflags="-s -w -buildid=": Strip debug symbols, DWARF info, and Go build ID
# - GOAMD64=v2: Use SSE4.2/POPCNT instructions (safe for most CPUs)
# - PGO: Go compiler automatically detects default.pgo and applies profile-guided optimizations
#   providing 3-7% additional performance improvement through better inlining decisions
# Note: UPX compression NOT used to avoid antivirus false positives and startup latency
RUN echo "🔨 Building PGO-optimized binary..." && \
    if [ -f "default.pgo" ]; then \
        echo "✅ Using PGO profile: $(du -h default.pgo | cut -f1)"; \
        PGO_FLAG="-pgo=default.pgo"; \
    else \
        echo "⚠️  No PGO profile found, building without PGO optimization"; \
        PGO_FLAG="-pgo=off"; \
    fi && \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOAMD64=${GOAMD64} go build \
    ${PGO_FLAG} \
    -tags go_json \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w -buildid= -X gpt-load/internal/version.Version=${VERSION}" \
    -o gpt-load && \
    echo "✅ Build complete: $(ls -lh gpt-load | awk '{print $5}')"

# Build minimal health check utility for Docker health checks
# This is necessary because the final image uses scratch base (no wget/curl available)
# Implementation uses TCP dial instead of HTTP GET to minimize binary size:
# - HTTP client: ~5MB (includes TLS, HTTP/2, full HTTP stack)
# - TCP dial: ~500KB-1.5MB (only basic networking)
# For Docker healthcheck, TCP connectivity check is sufficient
RUN echo "🔨 Building health check utility..." && \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOAMD64=${GOAMD64} go build \
    -pgo=off \
    -tags go_json \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w -buildid=" \
    -o healthcheck ./cmd/healthcheck && \
    echo "✅ Health check utility built: $(ls -lh healthcheck | awk '{print $5}')"


# =============================================================================
# Minimal runtime image (scratch for smallest size)
# =============================================================================
FROM scratch AS final

ARG TZ=Asia/Shanghai

# Copy CA certificates for HTTPS connections (required for TLS/SSL)
# Without this, any HTTPS request will fail with "certificate signed by unknown authority"
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data for time.LoadLocation()
COPY --from=go-builder /usr/share/zoneinfo /usr/share/zoneinfo

WORKDIR /app

# Runtime optimization: Set memory limit to prevent OOM
# GOMEMLIMIT is set to 1GiB based on project complexity:
# - Multiple database drivers (SQLite, MySQL, PostgreSQL)
# - Redis caching layer
# - HTTP proxy with streaming support
# - Concurrent request handling
# - In-memory caching for access keys and model redirects
# - JSON processing with high-performance libraries
# Recommendation: Set to 90% of container memory limit in production
# For 2GB container: GOMEMLIMIT=1800MiB
# For 4GB container: GOMEMLIMIT=3600MiB
ENV GOMEMLIMIT=1GiB \
    TZ=${TZ}

COPY --from=go-builder /build/gpt-load .
COPY --from=go-builder /build/healthcheck .

EXPOSE 3001
ENTRYPOINT ["/app/gpt-load"]
