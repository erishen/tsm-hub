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

### 会话记忆

- `remember` / `recall` 工具提供按 Key 隔离的键值记忆，SQLite 持久化（`data/memory.db`）
- 命名空间按 keyID 隔离，不同 Key 的记忆互相不可见
- 可在「记忆」页面一键清空全部记忆数据
- 记忆数据可能包含用户通过工具写入的敏感信息，定期清理或不启用该工具

## 能力池安全

### 内置工具安全边界

| 工具 | 安全风险 | 防护措施 |
|------|----------|----------|
| `fetch_url` | SSRF（服务端请求伪造） | 默认拒绝内网/环回地址，可配置 `agent.allow_private_url` 放行 |
| `read_file` / `csv_analyze` | 任意文件读取 | 仅在配置 `agent.read_root` 时提供，不可越界读取 |
| `execute_code` | 代码执行逃逸 | Docker 沙箱：禁网络、只读根、丢弃 capabilities、禁提权、资源限制、超时自动销毁 |
| `remember` / `recall` | 敏感信息持久化 | 按 Key 隔离命名空间，可手动清空 |
| `system_info` | 信息泄露 | 只读系统信息，不含敏感配置 |

**请求级开关**：`X-Llm-Router-Agent: off` 请求头可对本请求关闭 agent 工具循环；客户端自带
`tools` 时网关始终尊重客户端（纯透传，不注入、不执行）。

### MCP Server 安全

- MCP 工具由网关在服务端执行，客户端无法直接调用 MCP server 的原始接口
- stdio 模式的 MCP server 以网关子进程运行，继承网关的系统权限；**只接入可信的 MCP server**
- Streamable HTTP 模式的远程 MCP server 通过 HTTP 调用，确保网络可达且可信
- MCP 工具的能力边界由 MCP server 自身决定（如 filesystem 可读写配置的目录），接入前评估其权限范围
- 管理台展示 MCP server 时不回显 `env`/`command` 等敏感配置（只读接口不含这些字段）

### Docker 沙箱安全

`execute_code` 工具的安全边界：

- **禁网络**：`--network none`，容器内不可联网（需要联网的任务请用 `fetch_url` 等工具）
- **只读根文件系统**：仅 `/tmp` 可写，系统目录不可篡改
- **丢弃全部 capabilities**：`--cap-drop all`，无特权操作
- **禁提权**：`--security-opt no-new-privileges`
- **资源限制**：内存（默认 512MB）、CPU（默认 1 核）、进程数、文件描述符限制
- **超时自动 kill**：默认 30s 超时，超时后自动 kill 并清理容器
- **执行完自动销毁**：一次性容器，执行完立即删除，不残留

### 技能库安全

- 技能文件从 `settings.skills_dir` 目录加载，**只加载 Markdown 文本，不执行任何代码**
- 技能内容通过 system prompt 注入或 `skill-run` 工具返回给模型，由模型决定如何使用
- 技能文件可能包含指令，确保只加载可信来源的技能
- 技能注入模式按 Key 配置（`keys[].inject_skills`），可针对不同 Key 启用/禁用

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

### CORS（跨域资源共享）

- 网关本身**不配置 CORS**，浏览器跨域请求会被阻止（这是安全的默认行为，防止恶意网站调用 API）
- 如果需要从浏览器端直接调用网关 API（如前端应用直连），请在反向代理层配置 CORS：

**Nginx CORS 配置示例**：
```nginx
location / {
    # 允许的源（生产环境请指定具体域名，不要用 *）
    add_header Access-Control-Allow-Origin "https://your-app.example.com" always;
    add_header Access-Control-Allow-Methods "GET, POST, PUT, PATCH, DELETE, OPTIONS" always;
    add_header Access-Control-Allow-Headers "Authorization, Content-Type, X-Admin-Token, X-Session-Token" always;
    add_header Access-Control-Max-Age 86400 always;

    # 预检请求直接返回 204
    if ($request_method = OPTIONS) {
        return 204;
    }

    proxy_pass http://127.0.0.1:9070;
    # ... 其他 proxy 配置
}
```

### 速率限制

- **客户端 Key 限流**：每个自制 Key 可配置 `quota.rpm`（每分钟请求数），超限返回 429
- **上游 Provider 限流**：上游返回 429 时，网关自动将该 Provider 冷却一段时间（默认 60s），期间不选该 Provider，避免持续触发上游限流
- **管理登录暴力防护**：同 IP 连续失败 5 次登录，锁定 10 分钟（429）
- **请求体大小限制**：超过 `settings.max_body_bytes` 直接返回 413，不截断后处理
- **建议**：在反向代理层额外配置全局速率限制（如 Nginx `limit_req_zone`），作为网关限流的补充

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

### 基础安全
- [ ] Admin Token 已修改为强随机值（非默认 `123456`）
- [ ] 上游 Provider API Key 使用 `env:` 引用环境变量
- [ ] 通过反向代理提供 HTTPS（Nginx/Caddy）
- [ ] 管理端口未直接暴露公网（或有防火墙限制）
- [ ] `config.json` 权限为 `600`
- [ ] `config.json` 已加入 `.gitignore`
- [ ] 配置了用量数据自动清理（`usage_retention_days`）
- [ ] 配置了审计日志保留天数（`audit_retention_days`）
- [ ] 定期备份 `data/` 目录（config.json + usage/ + memory.db + audit.db）

### 能力池安全
- [ ] `fetch_url` 工具未放行内网地址（除非确有需要）
- [ ] `read_file` / `csv_analyze` 工具的 `read_root` 配置为最小必要目录
- [ ] `execute_code` 沙箱已启用资源限制（内存/CPU/超时）
- [ ] 只接入了可信的 MCP server（评估其权限范围）
- [ ] 技能库目录只包含可信来源的技能文件
- [ ] 不需要 agent 工具循环的 Key 可通过 `X-Llm-Router-Agent: off` 关闭

### 网络与限流
- [ ] 如需浏览器跨域调用，已在反向代理层配置 CORS（指定具体域名，不用 `*`）
- [ ] 已为高流量 Key 配置 `quota.rpm` 限流
- [ ] 反向代理层已配置全局速率限制（作为网关限流的补充）
- [ ] SSE 流式响应的代理超时已设置足够长（建议 300s+）
