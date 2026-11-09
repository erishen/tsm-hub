# mcp-git：带 git 的 supergateway 桥镜像（供 @cyanheads/git-mcp-server 使用）。
#
# 为什么需要：git-mcp-server 只是包装器，真正的 git 命令要跑在容器里。基础镜像若没装
# git，所有 git_* 工具都会返回 "Git command not found"。
#
# 工作区挂载：由 docker-compose.yml 把宿主工作区挂进容器同路径，
# 且是 `:rw` —— git 的 add/commit 需要写 .git。
#
FROM node:20-alpine

# 2026-11-09 由 mcp-bridge（bookworm-slim + apt git）换成本镜像自带 alpine：
#   371MB → 200MB，容器常驻内存 380MB → 135MB（实测）。
# alpine 的 apk git 与 Ubuntu/macOS 的 git 行为一致，init/add/commit 已实测通过。
RUN apk add --no-cache git ca-certificates \
 && npm install -g supergateway --registry=https://registry.npmmirror.com \
 && npm cache clean --force

# 沿用上游 entrypoint（supergateway），启动参数由 compose 的 command 传入。

# 必须显式声明：基础镜像（node:20-alpine）的 ENTRYPOINT 是 docker-entrypoint.sh，
# 不写这一行时 compose 的 command(--stdio ...) 会被当成 node 的参数直接报错。
ENTRYPOINT ["supergateway"]
