# mcp-gateway 的 git 变体：网关 + apk git（替代 mcp-bridge-git:latest）。
# 与 mcp-gateway.Dockerfile 共用同一份 mcp_gateway.c，因此同样带 GET /metrics。
# 构建：docker build -t mcp-gateway-git:latest -f deploy/mcp-gateway-git.Dockerfile deploy/
FROM alpine:3.20 AS build
RUN apk add --no-cache gcc musl-dev
COPY mcp_gateway.c /src/mcp_gateway.c
RUN gcc -O2 -std=c11 -Wall -Wextra -o /mcp_gateway /src/mcp_gateway.c

FROM node:20-alpine
RUN apk add --no-cache git ca-certificates
COPY --from=build /mcp_gateway /usr/local/bin/mcp_gateway
ENTRYPOINT ["/usr/local/bin/mcp_gateway"]
