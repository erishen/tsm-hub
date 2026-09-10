# 安全与合规指南

## 数据安全

### API Key 存储

**对外签发的 Token Key（sk-tr- 开头）**：
- 服务端只存储 SHA-256 哈希，不存明文
- 创建时只展示一次明文，2 分钟内可补看，超窗后无法再查看
- 泄露后可立即在管理台吊销，不影响其他 Key

**上游 Provider API Key**：
- ⚠️ **强烈建议使用 `env:` 前缀引用环境变量**，避免明文写入 `config.json`
  ```json
  { "api_key": "env:DEEPSEEK_API_KEY" }
  ```
- 直接填写明文时，`config.json` 权限为 `600`（仅所有者可读写），但仍存在备份泄露、误提交 git 等风险
- 管理台展示时自动脱敏（前4位****后4位）

### 用量数据

- 只记录元数据（模型、token 数、状态码、延迟），**不记录请求内容**（messages/prompt/content）
- 支持自动清理：在设置中配置 `usage_retention_days`（天），启动时自动删除过期流水
- 可在「用量」页面一键清空全部历史数据

### 审计日志

- 所有管理操作（创建/修改/删除 Provider、路由、Key、MCP、设置等）自动记录
- 记录内容：时间、操作类型、对象类型、对象 ID、详情（自动脱敏敏感字段）、操作者、客户端 IP、User-Agent
- 默认保留 90 天，可通过 `audit_retention_days` 配置
- 在「审计日志」页面可按对象类型、操作类型、对象 ID 过滤查询

## 部署安全

### HTTPS / TLS

⚠️ **网关本身只监听 HTTP，生产环境必须通过反向代理提供 HTTPS**。

**Nginx 示例**：
```nginx
server {
    listen 443 ssl http2;
    server_name llm-router.example.com;

    ssl_certificate     /etc/ssl/certs/llm-router.crt;
    ssl_certificate_key /etc/ssl/private/llm-router.key;
    ssl_protocols       TLSv1.2 TLSv1.3;

    location / {
        proxy_pass http://127.0.0.1:9070;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 300s;  # SSE 流式响应需要长超时
    }
}
```

**Caddy 示例**（自动 HTTPS）：
```caddy
llm-router.example.com {
    reverse_proxy 127.0.0.1:9070
}
```

### 管理接口鉴权

- 所有 `/api/admin/*` 接口都需要 Admin Token（`X-Admin-Token` 头）或登录会话（`X-Session-Token`）
- ⚠️ **生产环境必须修改默认 Admin Token**，通过环境变量设置：
  ```bash
  export LLM_ROUTER_ADMIN_TOKEN="your-strong-random-token"
  ```
- 登录失败有 IP 级别的暴力尝试防护（连续失败后临时拒绝）

### 网络隔离

- 建议管理端口（默认 9070）只监听 `127.0.0.1`，通过反向代理对外暴露
- 或使用防火墙限制管理端口的访问来源 IP
- 对外提供 OpenAI 兼容 API 的端口可单独配置

## 合规建议

### 数据保留

| 数据类型 | 默认保留 | 可配置 | 说明 |
|---------|---------|--------|------|
| 审计日志 | 90 天 | `audit_retention_days` | 管理操作追溯 |
| 用量流水 | 不自动清理 | `usage_retention_days` | token/成本统计 |
| 会话记忆 | 永久（SQLite） | 手动清空 | remember/recall 工具数据 |
| 上游 Key | 永久 | 手动删除 | Provider 配置 |

### 隐私保护

- 网关不记录请求内容，只记录元数据
- 日志中自动脱敏 Key（只打印前缀和哈希 ID）
- 审计日志详情中自动脱敏 `api_key`/`token`/`password`/`secret` 字段
- 支持一键清空用量数据和会话记忆

### 被遗忘权

- 吊销并删除某个 Token Key：管理台「Token Keys」页面删除
- 清空该 Key 的用量数据：目前按整体清空，后续可支持按 Key 清理
- 删除会话记忆：管理台「记忆」页面清空

## 检查清单

部署前请确认：

- [ ] Admin Token 已修改为强随机值（非默认 `123456`）
- [ ] 上游 Provider API Key 使用 `env:` 引用环境变量
- [ ] 通过反向代理提供 HTTPS（Nginx/Caddy）
- [ ] 管理端口未直接暴露公网（或有防火墙限制）
- [ ] `config.json` 权限为 `600`
- [ ] `config.json` 已加入 `.gitignore`
- [ ] 配置了用量数据自动清理（`usage_retention_days`）
- [ ] 配置了审计日志保留天数（`audit_retention_days`）
- [ ] 定期备份 `data/` 目录（config.json + usage/ + memory.db + audit.db）
