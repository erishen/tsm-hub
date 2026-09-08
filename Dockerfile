# 多阶段构建：只编译 Go，前端产物由宿主机事先构建好（避免镜像内联网装 Node）。
#
# 用法：
#   make web-install && make web-build   # 先构建 Angular 管理台
#   docker build -t llm-router .
#   docker run --rm -p 9070:9070 -v "$(pwd)/data:/data" -e LLM_ROUTER_ADMIN_TOKEN=xxx llm-router
#
# 如果跳过前端构建，镜像依然可用（API 正常），只是管理台显示占位页。

FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=docker
# alpine 上是 musl，external linkmode 同样需要 CGO。
RUN CGO_ENABLED=1 go build \
    -ldflags "-X main.version=${VERSION} -linkmode=external" \
    -o /out/llm-router ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S llmrouter && adduser -S llmrouter -G llmrouter

COPY --from=builder /out/llm-router /usr/local/bin/llm-router

USER llmrouter
EXPOSE 9070
VOLUME ["/data"]

ENTRYPOINT ["llm-router", "-data", "/data"]
CMD ["-addr", ":9070"]
