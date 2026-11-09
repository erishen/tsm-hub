# mcp-gateway：自研 C 网关，把 stdio MCP server 包成 Streamable HTTP，
# 替代 npm 的 supergateway（后者在镜像里占 66.6MB，本镜像不含它）。
#
# 构建：docker build -t mcp-gateway:latest -f deploy/mcp-gateway.Dockerfile deploy/
# 运行：docker run mcp-gateway:latest 8000 "node /mcp/node_modules/<server>.js"
#
# 并行能力：每条 HTTP 连接一个线程；一个 mcp-session-id 一个子进程；
# 会话内多条请求按内部 id 混流收发（慢调用不堵同会话的其它请求）。
# 未实现：GET /mcp 的 SSE 下行推流（协议里的可选通道），请求回 405。

FROM alpine:3.20 AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY mcp_gateway.c /src/mcp_gateway.c
RUN gcc -O2 -std=c11 -Wall -Wextra -o /mcp_gateway /src/mcp_gateway.c

FROM node:20-alpine
COPY --from=build /mcp_gateway /usr/local/bin/mcp_gateway
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/mcp_gateway"]
