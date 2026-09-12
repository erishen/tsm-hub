# MCP stdio -> Streamable HTTP 桥接镜像。
# 官方 stdio MCP server（filesystem/memory/sequential-thinking）只讲 stdio，
# tsm-hub 容器里没有 node、也看不到宿主路径，所以由本镜像把 stdio server
# 包成 Streamable HTTP（supergateway），tsm-hub 用 transport=http 远程调用。
# 具体跑哪个 server / 允许哪些目录由 docker-compose.yml 的 command 指定。
FROM node:20-bookworm-slim

# 国内环境走 npmmirror，避免 npmjs 直连 ECONNRESET。
RUN npm install -g supergateway --registry=https://registry.npmmirror.com

ENTRYPOINT ["supergateway"]
