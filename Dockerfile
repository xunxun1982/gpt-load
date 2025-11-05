FROM node:20-alpine AS builder

ARG VERSION=1.0.0
WORKDIR /build
COPY ./web .
# 升级 npm 到最新版本
RUN npm install -g npm@latest
RUN npm install
RUN VITE_VERSION=${VERSION} npm run build


FROM golang:alpine AS builder2

ARG VERSION=1.0.0
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

WORKDIR /build

# 利用 Docker 层缓存优化依赖下载
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=builder /build/dist ./web/dist

# 优化的编译命令
# 注意: Go 编译器已内置类似 LTO 的优化（内联、逃逸分析等），无需额外配置
RUN go build \
    -trimpath \
    -ldflags="-s -w -X gpt-load/internal/version.Version=${VERSION}" \
    -o gpt-load


FROM alpine

WORKDIR /app
RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates tzdata \
    && update-ca-certificates

# 运行时优化环境变量
# 限制内存使用，防止容器 OOM
ENV GOMEMLIMIT=512MiB

COPY --from=builder2 /build/gpt-load .
EXPOSE 3001
ENTRYPOINT ["/app/gpt-load"]
