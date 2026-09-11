# 多阶段构建：只编译 Go，前端产物由宿主机事先构建好（避免镜像内联网装 Node）。
#
# 用法：
#   make web-install && make web-build   # 先构建 Angular 管理台
#   docker build -t tsm-hub .
#   docker run --rm -p 9070:9070 -v "$(pwd)/data:/data" -e TSM_HUB_ADMIN_TOKEN=xxx tsm-hub
#
# 如果跳过前端构建，镜像依然可用（API 正常），只是管理台显示占位页。

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=docker
# alpine 上是 musl，external linkmode 同样需要 CGO。
RUN CGO_ENABLED=1 go build \
    -ldflags "-X main.version=${VERSION} -linkmode=external" \
    -o /out/tsm-hub ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S tsmhub && adduser -S tsmhub -G tsmhub

COPY --from=builder /out/tsm-hub /usr/local/bin/tsm-hub

USER tsmhub
EXPOSE 9070
VOLUME ["/data"]

ENTRYPOINT ["tsm-hub", "-data", "/data"]
CMD ["-addr", ":9070"]
