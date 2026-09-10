#!/usr/bin/env bash
# 预装 llm-router 网关的 MCP server 依赖，使平台开箱即用。
# 用法：./scripts/install-mcps.sh   （或 make install-mcps）
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 1/3 安装 npm 系 MCP server（filesystem / sequential-thinking / memory / github / brave-search / playwright）"
(cd mcp && npm install)

echo "==> 2/3 安装 serena（代码语义引擎，Python/uv）"
if ! command -v uv >/dev/null 2>&1; then
  echo "!! 未检测到 uv，跳过 serena（npm 系 MCP 不受影响）。安装 uv: curl -LsSf https://astral.sh/uv/install.sh | sh"
else
  uv tool install --from git+https://github.com/oraios/serena serena-agent 2>/dev/null || echo "!! serena 已安装或安装失败，跳过（可手动 uv tool install --from git+https://github.com/oraios/serena serena-agent）"
fi

echo "==> 3/3 可选：playwright 浏览器内核（约 300MB，浏览器自动化用）"
if command -v npx >/dev/null 2>&1; then
  (cd mcp && npx playwright install chromium 2>/dev/null) || echo "!! playwright 内核未安装，浏览器自动化暂不可用：cd mcp && npx playwright install chromium"
fi

echo "==> 完成。MCP 配置见 data/config.json 的 settings.mcps；重启网关生效。"
