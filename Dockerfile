FROM node:22-alpine AS node-builder

ARG VERSION=1.0.0
WORKDIR /build
COPY ./web .
RUN npm install
RUN VITE_VERSION=${VERSION} npm run build


FROM golang:1.25-alpine AS go-builder

ARG VERSION=1.0.0
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
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
