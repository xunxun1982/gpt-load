# Frontend build stage
# Note: Node.js 22 is used for consistency with all CI workflows.
# Dependencies build successfully as of the latest workflow runs.
FROM node:22-alpine AS node-builder

ARG VERSION=1.0.0
WORKDIR /build
# Upgrade npm to latest stable version before copying source files
# This leverages Docker layer caching - this layer only rebuilds when npm version changes
RUN npm install -g npm@11.6.4
COPY ./web .
# Install dependencies and auto-fix security vulnerabilities
# Note: npm audit fix automatically applies non-breaking security patches
RUN npm install && npm audit fix
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

# Optimized build command with go_json tag for high-performance JSON (goccy/go-json)
# Note: Go compiler already has built-in optimizations like LTO (inlining, escape analysis, etc.), no extra config needed
RUN go build \
    -tags go_json \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w -X gpt-load/internal/version.Version=${VERSION}" \
    -o gpt-load


FROM alpine

WORKDIR /app

# Install minimal runtime dependencies
# MCP service runtimes (Node.js, Python, etc.) will be installed on-demand
RUN apk upgrade --no-cache \
    && apk add --no-cache \
        ca-certificates \
        tzdata \
        curl \
        bash \
    && update-ca-certificates

# Runtime optimization environment variables
ENV GOMEMLIMIT=512MiB
# Ensure npm/npx can find global packages (if Node.js is installed later)
ENV PATH="/root/.npm-global/bin:/root/.local/bin:$PATH"
# Set npm global directory (avoids permission issues when Node.js is installed)
ENV NPM_CONFIG_PREFIX="/root/.npm-global"

COPY --from=go-builder /build/gpt-load .
EXPOSE 3001
ENTRYPOINT ["/app/gpt-load"]
