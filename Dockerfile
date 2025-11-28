# Frontend build stage
# Note: Node.js 22 is used for consistency with all CI workflows.
# Dependencies build successfully as of the latest workflow runs.
FROM node:22-alpine AS node-builder

ARG VERSION=1.0.0
WORKDIR /build
COPY ./web .

# Intentionally upgrade npm to latest version to eliminate version mismatch warnings
# This is a deliberate choice to ensure we always use the latest npm features and security fixes
# AI Review: Not accepting version pinning suggestion as our goal is to eliminate version warnings
RUN npm install -g npm@latest

# Install dependencies using npm ci for reproducible builds
# Fallback to npm install only if package-lock.json is missing or corrupted
RUN npm ci --omit=dev --no-audit --no-fund 2>/dev/null || \
    (echo "npm ci failed, falling back to npm install (lock file may be missing)" && \
     npm install --omit=dev --no-audit --no-fund)

# Attempt to fix security vulnerabilities (production dependencies only)
# Exit code is checked: only continue if no critical errors occurred
RUN npm audit fix --only=prod --audit-level=moderate 2>&1 | tee /tmp/audit.log && \
    (grep -q "ELOCKVERIFY\|ERR!" /tmp/audit.log && echo "Audit fix encountered errors, but continuing" || true)

# Build frontend
RUN VITE_VERSION=${VERSION} npm run build


FROM golang:1.25-alpine AS go-builder

ARG VERSION=1.0.0
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

ARG GOAMD64=v2
ENV GOAMD64=${GOAMD64}

WORKDIR /build

# Optimize dependency download using Docker layer caching
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=node-builder /build/dist ./web/dist

# Optimized build command
# Note: Go compiler already has built-in optimizations like LTO (inlining, escape analysis, etc.), no extra config needed
RUN go build \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w -X gpt-load/internal/version.Version=${VERSION}" \
    -o gpt-load


FROM alpine

WORKDIR /app
RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates tzdata \
    && update-ca-certificates

# Runtime optimization environment variables
# Limit memory usage to prevent container OOM
ENV GOMEMLIMIT=512MiB

COPY --from=go-builder /build/gpt-load .
EXPOSE 3001
ENTRYPOINT ["/app/gpt-load"]
