# tsm-hub

> **AI Capability Hub & LLM Gateway** — unify models, tools, skills, MCPs, sandbox, memory with discovery and promotion across 13+ providers.
> Expose only your own `sk-tr-…` keys to clients; internally route OpenAI-compatible requests to multiple configured upstream providers,
> with a built-in capability pool (generic tools + Agent Skills + MCP servers + Docker sandbox + session memory) that every downstream client gets for free.

- **Backend**: Go 1.26.11, single binary, stdlib-first — three direct dependencies: `modernc.org/sqlite` (pure-Go driver), `github.com/dop251/goja` (JS runtime for the Fastpath detector sandbox), and `github.com/fsnotify/fsnotify` (config hot-reload watcher)
- **Frontend**: Angular 19 (standalone + signals), build output `embed`ded into the Go binary
- **Storage**: JSON config (atomic writes) + JSONL usage logs + SQLite (memory/audit logs), no external DB required
- **Protocol**: OpenAI-compatible client API (`/v1/chat/completions`, `/v1/models`, …), with SSE streaming passthrough; **upstream protocol adapter layer** supports 13 protocols (OpenAI/Anthropic/Azure/Gemini/Bedrock/SageMaker/Cohere/Mistral/HuggingFace/Replicate/Together/Fireworks/Groq) + 12 China platforms (all OpenAI-compatible)
- **Capability pool**: built-in generic tools + Agent Skills library + MCP servers + Docker sandbox + session memory
- **Smart routing**: priority failover, weighted distribution, smart cost-aware routing, auto scene routing, deterministic fastpath
- **Security & compliance**: SHA-256 key hashing, audit logs (auto-redacted secrets), automatic usage cleanup, upstream key `env:` references, optional AES-256-GCM at-rest encryption of upstream keys (`TSM_HUB_MASTER_KEY`, fail-closed), config write file-lock + `SIGHUP` hot reload (multi-instance safe), outbound calls on timeout-guarded HTTP clients, native TLS/HTTPS, CORS middleware, startup security self-check (config permissions / plaintext key warnings / TLS warnings / master key validation), cost-warning badge in the admin console (`GET /api/admin/cost-warnings`), [Security Guide](./docs/SECURITY.md), [Privacy Policy](./docs/PRIVACY.md), [DPA Template](./docs/DPA.md)
- **Admin console**: 19 pages (Dashboard/Providers/Upstream Platforms/Routes/Keys/Models/Balances/Usage/Observability/Skills/MCP/Tools/Memory/Sandbox/Fastpath/Recommendations/Playground/Audit Logs/A2A Panel), dark mode, global search, PWA

---

## What it solves

| Problem | Solution |
|---------|----------|
| Upstream API keys scattered across clients, one leak means full rotation | Issue only self-made keys to clients; upstream keys live only on the server; revoking one self-made key doesn't affect others |
| Changing models/providers requires updating all client code | Clients only use aliases like `smart` / `cheap`; mappings live in the server-side route table |
| One upstream goes down, entire chain breaks | Priority-based failover + health check auto-removal, cooldown + half-open probing |
| No visibility into who used how many tokens / how much cost | Per-key independent accounting, supports RPM / total tokens / total cost / daily quota |

---

## Quick start

```bash
cd work/golang/tsm-hub

# 0) Pull skills submodule (resolve-skills, used by admin "Skills" page)
git submodule update --init --recursive

# 1) Build (includes mock upstream for local dev)
make build mock

# 2) Run end-to-end smoke (auto-starts mock upstream + gateway + assertions)
make smoke

# 3) For real use: copy config template, fill in your upstream keys
mkdir -p data && cp config.example.json data/config.json
$EDITOR data/config.json          # Must change: settings.admin_token, each provider's api_key

# 4) Start
./bin/tsm-hub -data ./data -addr :9070
```

Open <http://localhost:9070> for the admin console, log in with `admin_token`.

> **Docker Compose uses a different port**: `docker-compose.yml` maps container port
> `9070` to host **<http://127.0.0.1:19070>** (loopback only). When starting with
> `make docker-up`, replace `localhost:9070` in every example below with
> `127.0.0.1:19070`; `9070` applies only to `make run` / running the binary directly.
> If the host has `http_proxy` set, add `--noproxy '*'` to `curl` as well — otherwise
> the proxy returns 502 (this is why `make docker-status` used to always say
> "service not responding").

Call it just like OpenAI (only change the key, base_url, and model alias):

```bash
curl http://localhost:9070/v1/chat/completions \
  -H "Authorization: Bearer sk-tr-xxxxxxxx..." \
  -H "Content-Type: application/json" \
  -d '{"model":"smart","messages":[{"role":"user","content":"tell me a joke"}]}'
```

Python SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:9070/v1", api_key="sk-tr-...")
print(client.chat.completions.create(model="smart", messages=[{"role": "user", "content": "hi"}]))
```

---

## Request pipeline

```
Client (sk-tr-…)
   │
   ▼
┌──────────────────────── tsm-hub ────────────────────────┐
│ 1. Auth    sha256(Key) → memory index → enabled/expiry check │
│ 2. Rate limit  RPM sliding window + token/cost/daily quota   │
│ 3. Route    alias → candidate list: priority sort + weight    │
│            filter out cooling providers, keep one half-open slot│
│ 4. Adapt    protocol adapter: OpenAI→Anthropic/Azure/Gemini/…  │
│            request/response/stream format conversion + SigV4 sign│
│ 5. Forward  swap Authorization, rewrite model, inject stream_options │
│ 6. Respond  non-streaming full response; streaming SSE frame-by-frame │
│            response header X-LLM-Router-Provider: <provider_id> │
│ 7. Account  usage → memory aggregation + data/usage/YYYY-MM-DD.jsonl │
└────────────────────────────────────────────────────────────┘
   │                    │                    │
   ▼                    ▼                    ▼
OpenAI            DeepSeek / Tongyi         Anthropic / Gemini
(failover to next candidate on failure; consecutive failures → removal + cooldown + half-open probe)
```

---

## Directory structure

```
tsm-hub/
├── cmd/
│   ├── server/          # Gateway main program
│   └── mockupstream/    # Fake upstream for local dev (no real models)
├── internal/
│   ├── config/          # Startup params (flags + env vars)
│   ├── store/           # Config models, atomic persistence, memory index
│   ├── auth/            # Self-made key issuance/validation, admin sessions
│   ├── router/          # Route selection + health state machine
│   ├── proxy/           # OpenAI-compatible forwarding, SSE passthrough, usage collection, agent tool loop, MCP, memory, sandbox, fastpath
│   ├── quota/           # Rate limiting, usage aggregation, JSONL persistence
│   ├── api/             # HTTP routing (/v1/*, /api/admin/*)
│   ├── audit/           # Audit logs (SQLite storage, auto-redaction, auto-cleanup)
│   ├── skills/          # Agent Skills library loading and injection
│   └── web/             # embed frontend build output
├── web/                 # Angular admin console source
├── scripts/smoke.sh     # End-to-end smoke test
├── config.example.json  # Config template
└── data/                # Runtime data (config.json + usage/*.jsonl + memory.db + audit.db)
```

---

## Configuration

Configuration priority: **command-line flags > environment variables > `.env` file > `data/config.json`**.

### `.env` file support

Copy `.env.example` to `.env` and customize. The `.env` file is loaded automatically on startup
(current directory first, then project root). It will **not** override already-set environment variables.

Common `.env` variables:

```bash
# Basic
TSM_HUB_ADDR=:9070
TSM_HUB_DATA_DIR=./data
TSM_HUB_ADMIN_TOKEN=your-admin-token
TSM_HUB_LOG_LEVEL=info

# At-rest encryption (optional): master key for AES-256-GCM sealing of
# providers[].api_key in config.json. 32 bytes as 64-char hex or 44-char base64.
# Unset = keys stay as env:/plaintext (backward compatible); malformed key
# refuses to start (fail fast). Format validated at startup.
# TSM_HUB_MASTER_KEY=0123...ef

# Smart routing strategy
TSM_HUB_SMART_STRATEGY=stability_first  # cost_first / stability_first / task_aware / balanced
TSM_HUB_SMART_COST_WEIGHT=50
TSM_HUB_SMART_STABILITY_WEIGHT=100
TSM_HUB_SMART_LATENCY_WEIGHT=70
```

Legacy prefixes `LLM_ROUTER_*` are also supported for migration.

### `config.json`

`data/config.json` is auto-generated on first start, see `config.example.json` for structure:

| Field | Description |
|-------|-------------|
| `settings.listen` | Listen address, can be overridden by `-addr` |
| `settings.admin_token` | Admin console password, can be overridden by `-admin-token` / `$LLM_ROUTER_ADMIN_TOKEN` |
| `settings.fail_threshold` / `cooldown_sec` | Remove after N consecutive failures, half-open probe after M seconds cooldown |
| `settings.pricing` | Price per 1K tokens (USD) for cost estimation; matched by "actual upstream model name → alias → default" order |
| `providers[]` | Upstream: `base_url` (with or without `/v1`), `api_key`, `models`, `weight`, `priority` |
| `routes[]` | External model name → candidate list; `strategy`: `failover` (priority-based degradation) / `weighted` (weighted distribution) |
| `keys[]` | Self-made keys, **only sha256 hash stored** |
| `settings.skills_dir` | Agent Skills library directory (supports relative paths); empty = skills disabled |
| `settings.agent.*` | Gateway agent (generic tools + server-side execution loop), see below |
| `settings.mcps` | External MCP servers (stdio or Streamable HTTP), their tools register as `mcp_<server>_<tool>` in the gateway tool pool |
| `settings.audit_retention_days` | Audit log retention days (default 90), auto-cleanup expired entries |
| `settings.usage_retention_days` | Usage log retention days (default 0 = no auto-cleanup), delete expired files on startup |
| `settings.tls.*` | Native TLS/HTTPS: `enabled` / `cert_file` / `key_file`; when enabled the gateway listens directly on HTTPS (no reverse proxy needed). Startup validates cert files and warns if TLS is disabled in production. |
| `settings.cors.*` | CORS middleware: `enabled` / `allowed_origins` / `allowed_methods` / `allowed_headers` / `allow_credentials` / `max_age`; disabled by default (secure default — prevents unauthorized cross-origin calls). Preflight OPTIONS requests are handled automatically. |
| `settings.sandbox.*` | Docker sandbox config (execute_code tool), see below |

**Gateway agent (built-in generic tools)**: When the client request **does not include `tools`**, the gateway automatically appends the built-in tool pool
and executes the `tool_calls` loop server-side, ultimately returning the answer (streaming requests also run the loop internally then replay via SSE):

| Tool | Description |
|------|-------------|
| `get_time` | Server current local time |
| `calc` | Math expression evaluation (arithmetic + parentheses + sqrt/pow/min/max/round/floor/ceil/sin/cos/tan/log/exp) |
| `fetch_url` | Fetch webpage/API text (http/https only, **internal addresses rejected by default** to prevent SSRF) |
| `echo` | Echo text |
| `skill-run` | Load full description of a specified skill from the gateway skill library |
| `remember` / `recall` | Key-isolated key-value memory (SQLite-persisted) |
| `read_file` | Read files within whitelisted root directory (only available if `agent.read_root` is configured) |
| `csv_analyze` | Analyze CSV structure within whitelisted root directory (only available if `agent.read_root` is configured) |
| `execute_code` | Docker sandbox code execution (only available if `settings.sandbox.enabled=true` and docker is installed) |
| `query_exchange_rate` | Real-time exchange rate query (open.er-api.com free API) |
| `system_info` | Server OS/arch/CPU/memory/disk/uptime |
| `tool_search` | Search the full tool catalog by keywords (miss-fallback for dynamic injection, see below) |

`settings.agent` config:

| Field | Description |
|-------|-------------|
| `disabled` | `true` to disable agent (enabled by default) |
| `max_rounds` | Max tool loop rounds (default 4) |
| `allow_private_url` | `fetch_url` allow internal/loopback addresses (rejected by default) |
| `read_root` | Root directory allowed for `read_file` (empty = tool not available) |
| `memory_file` | `remember` persistence file (empty = memory only) |
| `dynamic_tools` | Enable dynamic tool injection: send only "resident core + top-K request-relevant tools + `tool_search`" to save tokens (off by default) |
| `dynamic_top_k` | Max number of relevant tools injected dynamically (default 8, max 20) |
| `core_tools` | Resident tool names in dynamic mode; empty = `calc/fetch_url/get_time/remember/recall/skill-run` |

Dynamic injection: when `dynamic_tools` is on, the gateway scores the tool catalog against the user's request text
(English tokens + Chinese bigrams matched against tool names/descriptions) and sends only the core set plus the
top-K most relevant schemas. If the model needs a tool that is not in the list, it calls `tool_search` to retrieve
the full catalog and then invokes tools by name. Small pools (≤ core + topK + 4) fall back to full injection.
Composes with per-key `tools_allow`/`mcps_allow` (allowlist filters first, dynamic selection picks from the rest).

Request-level switch: `X-Llm-Router-Agent: off` header can disable agent for this request; when client provides
`tools`, the gateway always respects the client (pure passthrough, no injection, no execution).

**MCP servers (stdio + Streamable HTTP)**: After configuring `settings.mcps`, the gateway connects to the server on startup/first request,
completes MCP handshake and fetches the tool list. Tools are named `mcp_<server>_<tool>` and added to the agent tool pool,
executed server-side by the gateway (lazy connection + auto-reconnect on failure). Common MCPs (filesystem / sequential-thinking /
memory / serena) are pre-installed, run `make install-mcps` once for out-of-the-box use:

```json
"mcps": {
  "fs":     {"command": "./mcp/node_modules/.bin/mcp-server-filesystem", "args": ["/path/to/workspace"]},
  "think":  {"command": "./mcp/node_modules/.bin/mcp-server-sequential-thinking"},
  "memory": {"command": "./mcp/node_modules/.bin/mcp-server-memory"},
  "serena": {"command": "<uv-tool-dir>/serena-agent/bin/serena", "args": ["start-mcp-server"]},
  "remote": {"transport": "http", "url": "http://127.0.0.1:8787/mcp"}
}
```

`make install-mcps` will: ① `npm install` into `mcp/` (filesystem / sequential-thinking / memory /
github / brave-search / playwright); ② `uv tool install` serena (code semantic engine, Python/uv);
③ optionally install playwright browser kernel. serena's `uv-tool-dir` can be found via `uv tool dir`. Transport:
`command` (stdio, local subprocess) or `transport: "http"` + `url` (remote Streamable HTTP endpoint,
response supports application/json and SSE). Tool capability boundaries are determined by the MCP server you configure (e.g. filesystem
can read/write configured directories).

**Docker sandbox (execute_code)**: When `settings.sandbox.enabled=true`, the gateway registers
the `execute_code` tool, executing code in a disposable Docker container (supports python /
javascript / shell / java / go / rust / c / cpp). Sandbox security boundaries: no network
(`--network none`), read-only root filesystem (only /tmp writable), drop all capabilities,
no privilege escalation, memory/CPU/process/file descriptor limits, timeout auto-kill and container cleanup, auto-destroy after execution.

```json
"sandbox": {"enabled": true, "timeout_seconds": 30, "memory_mb": 512, "cpus": 1, "max_output_kb": 100}
```

Tool params: `language` (including aliases like py/js/sh/c++) + `code` (full source) + optional
`timeout`. For tasks requiring network access, use tools like `fetch_url`; no network access inside the sandbox.

**Deterministic fastpath**: Pure-code-solvable problems like arithmetic, current time, date calculation, unit conversion, number statistics,
base conversion, word count, etc. **return directly, zero model calls, zero upstream cost**,
response header `X-Llm-Router-Fastpath: <method>` (provider is `fastpath`):

| Matcher | Trigger example |
|---------|-----------------|
| arithmetic | calculate 2+3 / 12×34 / 23 plus 45 (supports Chinese operators, power, parentheses) |
| statistics | average of … / sum / max-min / sort (number list) |
| unit_convert | 100 Celsius to Fahrenheit / 5 km to miles |
| date_math | what date is tomorrow / date after N days / days between two dates |
| base_convert | 255 in hex / decimal to binary |
| text_stats | how many characters in this text |
| time | what time is it / current time |

**codegen + promotion**: When built-in matchers don't hit, the gateway asks an LLM to generate a JS detector
(`detect(text) -> string|null`), validated and executed in a goja sandbox (no host API, no I/O,
no network, 3s hard timeout, no eval/Function). On hit, it's persisted as a plugin
(`<data>/fastpath_plugins/`), and subsequent similar problems **hit directly with zero model calls**. Admin console
"Fastpath" page can view built-in matchers, test any query, one-click generate detectors, promote plugins to formal
detectors (moved to `<data>/fastpath_promoted/`, hot-reloaded by mtime, no restart needed).

```json
"fastpath": {"enabled": true, "codegen": true, "plugins_dir": ""}
```

**Upstream keys not written to config file**: `api_key` supports reference form like `env:OPENAI_API_KEY`,
put the real key in an environment variable before startup (admin console echoes the reference itself, no secret).

Fallback when route table doesn't match: directly find all enabled providers declaring support for that model, distribute by weight.

**Configurable smart routing**: When `strategy: "smart"`, candidates are selected by multi-dimensional composite scoring.
The strategy mode and dimension weights are fully configurable (0 = use the strategy's built-in weights):

| Field | Built-in | Description |
|-------|---------|-------------|
| `smart.strategy_mode` | configurable | Strategy mode: `cost_first` / `stability_first` / `task_aware` / `balanced` |
| `smart.cost_weight` | per mode | Cost dimension weight (0-200) |
| `smart.stability_weight` | per mode | Stability/success-rate dimension weight (0-200) |
| `smart.latency_weight` | per mode | Latency dimension weight (0-200) |
| `smart.free_bonus` | 100 | Base bonus for free models |
| `smart.half_open_penalty` | 30 | Penalty for half-open (post-cooldown probing) candidates |
| `smart.throttle_sec` | 60 | Cooldown seconds after upstream 429, provider not selected during this period |
| `smart.unavailable_sec` | 1800 | Mark duration for upstream 404 "model not found" |
| `smart.price_tiers` | `[{0,60},{0.5,40},{2,20},{10,5}]` | Price tier bonus: prompt price ≤ max tier gets score (match high→low) |

**Strategy modes**:

| Mode | Cost | Stability | Latency | Use case |
|------|------|-----------|---------|----------|
| `cost_first` | 100 | 40 | 30 | Cost-sensitive workloads, free/cheap models preferred |
| `stability_first` | 30 | 100 | 50 | Production services where reliability matters most |
| `task_aware` | 50 | 80 | 70 | Mixed workloads, balance quality and speed |
| `balanced` | 60 | 70 | 70 | General purpose, balanced across all dimensions |

Stability score is calculated from historical success rate (`(requests - errors) / requests`) and consecutive failure count.
Latency score uses EWMA-smoothed latency (100ms = 100 points, 1000ms+ = 0 points).
New providers with no history get a neutral score (70 stability, 60 latency) to encourage exploration.

**Scene routing (auto brainless call)**: When client `model` is not provided or is `"auto"`, the gateway automatically
classifies by request content and routes to the corresponding scene (`chat` / `reason` / `code` / `fast`), response header
`X-Llm-Router-Scene` returns the hit signal. Admin "Routes" page can configure candidates for each scene separately.
Falls back to `smart` wildcard route when no `auto` scene route exists.

**Key skill injection**: `keys[].inject_skills` controls skill injection into system for that key's requests:
`""` (default, no injection) / `"list"` (skill list, for model to decide when to call) / `"all"` (all
skill full text) / other string (single skill name). Skill source directory specified by `settings.skills_dir`.

**Observability**: Each request writes one log entry (`data/usage/YYYY-MM-DD.jsonl`), in addition to usage it also
records attribution fields:

| Field | Description |
|-------|-------------|
| `scene` | auto routing hit scene (chat/reason/code/fast; empty for non-auto) |
| `fastpath` | fastpath hit marker (`arithmetic` / `unit_convert` / `plugin:xxx`, etc.) |
| `attempt` | Which candidate actually tried (>1 means failover occurred) |

Admin API `GET /api/admin/observability/overview` returns aggregated view: `today/week/month`
overview (request count/error rate/avg latency/cost/failover count), by-provider attribution, by-scene distribution,
daily trend. Admin "Observability" page displays directly. Historical logs (missing old fields) are auto-compatible, no migration needed.

**Audit logs**: All management operations (create/update/delete Providers, Routes, Keys, MCP, Settings, etc.) are automatically recorded,
stored in `data/audit.db` (SQLite), including timestamp, action type, object type, object ID, detail (auto-redacted
`api_key`/`token`/`password`/`secret`), operator, client IP, User-Agent. Default retention 90 days,
configurable via `settings.audit_retention_days`. Admin "Audit Logs" page supports filtering by object type, action type,
object ID, and pagination.

**External discovery & promotion mechanism**: The gateway automatically records `tools`, `skills`, `mcps` declared in client requests,
displayed as external candidates in admin "Tools", "Skills", "MCP" pages. Admins can view usage statistics and selectively "adopt"
as gateway built-in capabilities (tools promoted to built-in tools or fastpath plugins, skills integrated into skill library, MCP integrated into gateway tool pool).
This way best practices from external clients can be **promoted** into gateway generic capabilities, subsequent clients don't need to re-declare.

---

## Client-facing programmable interfaces

Read-only capability discovery interfaces for **client programs**: accessible with any self-made key (`Authorization: Bearer sk-tr-…`),
allowing clients to dynamically discover what models, tools, MCPs, skills the gateway has, no local scanning needed:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/v1/models` | Available models and route aliases (OpenAI-compatible) |
| GET | `/v1/tools` | Gateway tool pool catalog: built-in + conditional + MCP (OpenAI function schema) |
| GET | `/v1/mcps` | Mounted MCP servers: name / transport / connection status / tools (read-only, no sensitive config like env/command) |
| GET | `/v1/skills` | Skill library list; `/v1/skills/<name>` for single skill full text |

```bash
curl http://localhost:9070/v1/tools -H "Authorization: Bearer <your-key>"
curl http://localhost:9070/v1/mcps  -H "Authorization: Bearer <your-key>"
curl http://localhost:9070/v1/skills -H "Authorization: Bearer <your-key>"
```

> Tolerance: some clients append the capability segment to the chat path
> (`GET /v1/chat/completions/tools`, etc.). GET has no chat semantics on those
> paths, so the gateway normalizes them to the canonical endpoints above and
> serves the catalog directly instead of proxying upstream (which would only
> answer 404/405 to a GET). Likewise, non-POST methods on the plain chat path
> get a local 405 (`Allow: POST`) without burning an upstream call.

---

## A2A & peer 面（lume 集群接入）

tsm-hub 可作为 **A2A 能力节点**接入 lume 集群（lume-a2a 协议），把「工具 / 技能 /
MCP / 用量 / provider 健康」暴露给 Lume 侧的 agent，供跨系统协商与发现。

| 端点 | 认证 | 说明 |
|------|------|------|
| `GET /.well-known/agent-card.json` | 公开（A2A 发现规范） | 节点名 + `tsm.*` 能力目录（skills[]） |
| `POST /a2a` | 白名单（仅只读 capability） | A2A JSON-RPC：`message/send` / `tasks/get` / `tasks/cancel` |
| `GET /peer/health` | peer 签名 | 身份 + 健康（与 Lume 侧同名端点逐字段对齐） |
| `POST /peer/invoke` | peer 签名 | 按 tool 名执行一个 `tsm.*` 能力（body 与 Lume 侧同形） |
| `POST /peer/relay` | peer 签名 | 410 Gone（多跳转发已退役，端点保留并说明原因） |

**能力（全部只读）**：`tsm.overview`（服务概况）/ `tsm.capabilities`（工具+技能目录）/
`tsm.skill`（技能全文，缺 name 回 `input-required`）/ `tsm.usage`（用量统计）/
`tsm.model_health`（provider 健康快照）/ `tsm.routes`（路由表，不含凭据）。
写入/执行仍只走 Admin API（admin token）——A2A 面不提供任何变更能力。

**签名与 lume-core/lib/peer.lume 字节级一致**（HMAC-SHA256 hex、同一 scope 拼接、
derived 模式 `key_for(node)=hmac_sha256(PEER_ROOT,"peer:"+node)`），配置见
[`.env.example`](./.env.example) 的 A2A 段：

```bash
TSM_HUB_PEER_NAME=tsm-hub        # 本节点名
TSM_HUB_PEER_ROOT=<与 Lume 同值>   # derived 模式（推荐，防冒充）
# TSM_HUB_PEER_SHARED=<...>        # shared 模式（向后兼容，无法防冒充）
# TSM_HUB_PEER_REGISTRY=./data/peers-identity.json
```

- 未配任何凭据 → peer 签名面 **fail-closed**（整面拒绝，不放行）；A2A 标准面仍挂载（公开发现）。
- derived 模式下节点必须在注册表里（与 Lume 侧 `.data/peers-identity.json` 同路径惯例，
  两侧可共用一份文件）；未登记节点 / 冒充 / 时间窗外 / 错误签名一律拒。
- Agent Card 的 `url` 取监听地址；绑 0.0.0.0 时回落 127.0.0.1，跨机部署设
  `TSM_HUB_A2A_PUBLIC_URL`。

### 接入 lume 集群（实操步骤）

1. **两端同一把根密钥**：`TSM_HUB_PEER_ROOT` 必须与 lume 节点 `.env` 的 `PEER_ROOT`
   完全同值；容器里用 `printenv TSM_HUB_PEER_ROOT` 确认确实注入（compose 只注入显式列出的变量）。
2. **注册表互登记对方节点名**（最易漏的一步）：
   - tsm-hub 的 `data/peers-identity.json` 的 `peers` 要含 lume 节点的**实际运行名**
     （即对方 `.env` 的 `PEER_NAME`；本项目为 `resolve-lume`，不要写 `lume-a2a` 这类历史别名）；
   - lume 的 `.data/peers-identity.json` 的 `peers` 要含 `tsm-hub`（或在其 `PEER_REG_DIR`
     自注册目录放 `tsm-hub.json`）。
   - 缺任一边都是单向通，报错为 `unknown peer '<name>' (not in registry)`：看到这行先核
     节点名，不要去核密钥。
3. **生效方式不同**：lume 侧 `known()` 每次请求重读注册表（改完即生效）；tsm-hub 侧启动时
   加载，改完必须 `docker compose restart tsm-hub`。
4. **验收**：未签名 `/peer/health` → `missing auth params`；用真实节点名签一次 GET 应得
   `ok:true, mode:"derived"`；再用编造名签一次应得 `unknown peer`——两条链都覆盖才算通。

---

## Admin API

All require `X-Admin-Token: <admin_token>` (or first `POST /api/admin/login` to get `X-Session-Token`).

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/admin/login` | Exchange admin token for session token (5 wrong attempts from same IP locks for 10 minutes) |
| GET | `/api/admin/overview` | Overview: provider/route/key counts, today and cumulative usage |
| GET | `/api/admin/cost-warnings` | Cost quota warnings: enabled keys whose usage ratio ≥ 80% (max_tokens / max_cost_usd / daily_tokens), sorted by ratio desc; powers the admin console red badge (click → `/keys`) |
| GET/POST | `/api/admin/providers` | Provider list / create or update |
| POST | `/api/admin/providers/probe` | Probe upstream model list and balance |
| GET | `/api/admin/providers/balances` | Batch query each provider's balance/quota (with cache) |
| DELETE | `/api/admin/providers/{id}` | Delete (also cleans related candidates in route table) |
| GET/POST | `/api/admin/routes` | Route table list / create or update |
| DELETE | `/api/admin/routes/{model}` | Delete route |
| GET/POST | `/api/admin/keys` | Key list / issue (**response returns plaintext once**) |
| POST | `/api/admin/keys/{id}/toggle` | Enable/disable |
| PATCH | `/api/admin/keys/{id}` | Update key name/model whitelist/quota |
| GET | `/api/admin/keys/{id}/plaintext` | Re-view newly created key plaintext (valid within 2 minutes of creation, shown only once) |
| DELETE | `/api/admin/keys/{id}` | Delete |
| GET | `/api/admin/models/catalog` | Model catalog: all providers' models aggregated, categorized by type, marked free/paid |
| POST | `/api/admin/models/refresh` | Refresh model catalog (re-probe all providers) |
| GET | `/api/admin/models/recommendations` | App recommendations: recommend suitable app scenarios based on current model pool |
| GET | `/api/admin/usage?days=7&limit=50` | Daily / by-model / by-key aggregation + recent logs |
| POST | `/api/admin/usage/clear` | Clear all usage logs |
| GET | `/api/admin/observability/overview` | Observability: overview / by-provider attribution / by-scene distribution / daily trend |
| GET | `/api/admin/health` | Provider real-time health (latency EWMA, failure count, cooldown expiry) |
| GET/POST | `/api/admin/settings` | View / modify global settings (timeout, threshold, price table, audit/usage retention days) |
| GET | `/api/admin/skills` | Skill library list |
| GET | `/api/admin/skills/{name}` | Single skill full text (Markdown) |
| GET/POST | `/api/admin/mcps` | MCP server list / create or update |
| DELETE | `/api/admin/mcps/{name}` | Delete MCP server |
| GET | `/api/admin/tools` | Gateway tool pool catalog (built-in + MCP + conditional tools) |
| POST | `/api/admin/tools/invoke` | One-click test tool execution |
| GET | `/api/admin/fastpath` | Fastpath status: built-in matchers + plugin list |
| POST | `/api/admin/fastpath/{name}/promote` | Promote plugin to formal detector (optional fastpath plugin or Tools) |
| DELETE | `/api/admin/fastpath/{name}` | Delete fastpath plugin |
| POST | `/api/admin/fastpath/generate` | Manually trigger codegen: generate detector for query and validate |
| GET | `/api/admin/external-tools` | External client-used tool candidates (auto-discovered, can be selectively adopted) |
| POST | `/api/admin/external-tools/{name}/adopt` | Adopt external tool as gateway built-in tool |
| DELETE | `/api/admin/external-tools/{name}` | Delete/ignore external tool candidate |
| GET | `/api/admin/external-skills/candidates` | External skill candidate list |
| GET | `/api/admin/external-mcps/candidates` | External MCP candidate list (auto-discovered MCP declared by clients) |
| POST | `/api/admin/external-mcps/{server}/adopt` | Adopt external MCP server |
| DELETE | `/api/admin/external-mcps/{server}` | Ignore/delete external MCP candidate |
| GET | `/api/admin/sandbox/status` | Docker sandbox status (availability, image, config) |
| GET | `/api/admin/memory` | Session memory list (remember/recall data, SQLite-persisted) |
| DELETE | `/api/admin/memory` | Clear all session memory |
| GET | `/api/admin/audit-logs` | Audit log query (supports object/action/object_id filtering + pagination) |

Additional probe endpoints (no auth required):

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe: `status`/`uptime_s`/`requests` + `degraded`/`providers[]` upstream health |
| GET | `/metrics` | Prometheus text format: HTTP/quota counters, **HTTP total-duration (split by flow) and streaming first-byte histograms**, upstream health/latency/**failure classes**, cumulative token/cost usage, health state machine, MCP/A2A/runtime snapshots |

CLI equivalent for issuing a key:

```bash
curl -X POST http://localhost:9070/api/admin/keys \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"prod","models":["smart"],"quota":{"rpm":60,"max_cost_usd":20}}'
# → {"ok":true,"id":"…","key":"sk-tr-…","warning":"Please save immediately…"}
```

---

## Development

```bash
make build         # Build server
make mock          # Build mock upstream
make test          # Unit tests + e2e (built-in mock, no internet needed)
make vet fmt       # Static check and formatting
make smoke         # End-to-end smoke script
make web-install   # Install frontend deps (pnpm preferred, falls back to npm)
make web-dev       # Angular dev server :4200 (/api proxied to :9070)
make web-build     # Build frontend to internal/web/dist/browser (for embed)
make web-check     # TS syntax self-check without node_modules
```

> **Frontend lockfile**: `web/pnpm-lock.yaml` is the single source of truth;
> `make web-lock-check` verifies every dependency specifier against `web/package.json`.
> On machines without pnpm, `make web-install` falls back to npm and generates
> `web/package-lock.json` — that file is ignored by the repo `.gitignore` and must not
> be committed, otherwise two lockfiles for one `package.json` will silently drift.
> For reproducible builds pin the package manager explicitly: `make PM=pnpm web-install`.

### Local dev (start frontend + backend together)

```bash
make dev           # First kill residual processes on :9070/:4200, then rebuild and start backend + Angular dev server in background
make dev-logs      # tail -f .dev/*.log
make dev-status    # See who's listening on those ports
make dev-stop      # Stop only, don't kill build, ports and .dev/*.pid cleaned
```

`make dev` will: clean ports → `go build` → start backend (`-data ./data`, logs `.dev/router.log`) → start `pnpm start`
(logs `.dev/web.log`) → poll `/healthz` and `:4200` until ready then print URL; frontend deps auto-install once if missing.
Ports can be changed with `make dev ROUTER_PORT=9080 WEB_PORT=4300`.

> ⚠️ `dev-stop` is **port-based** `kill -9`: if other services are running on :9070/:4200, they'll be killed too,
> use `make dev-status` to confirm before executing.

> On macOS 15, Go binaries need `CGO_ENABLED=1 -ldflags=-linkmode=external`,
> otherwise dyld reports `missing LC_UUID`. Makefile handles this uniformly.

---

## Container deployment

### Quick start

```bash
make docker-up    # Build frontend + image, start container in background
```

Visit http://localhost:9070 for the admin console.

### Makefile commands

| Command | Description |
|---------|-------------|
| `make docker-build` | Build Docker image (auto-builds frontend first) |
| `make docker-up` | Build and start container in background (mounts `./data`) |
| `make docker-down` | Stop and remove container (keeps `./data` volume) |
| `make docker-stop` | Stop container (keep it, can restart with `docker-start`) |
| `make docker-start` | Start a previously stopped container |
| `make docker-restart` | Restart container |
| `make docker-logs` | Follow container logs (Ctrl-C to exit) |
| `make docker-status` | Show container status + health check |

### Manual docker run

```bash
docker build -t tsm-hub .
docker run -d --name tsm-hub \
  -p 9070:9070 \
  -v "$(pwd)/data:/data" \
  -e TSM_HUB_LOG_LEVEL=info \
  tsm-hub
```

### How it works

- Only Go is compiled in the image (no Node.js needed in-image); frontend output is copied from host `internal/web/dist/browser` (auto-synced by `make web-build`).
- `make docker-build` and `make docker-up` automatically run `web-build` first, ensuring the admin console is always up-to-date.
- Data is persisted via the `./data` volume mount — all config, usage logs, memory, and audit data survive container restarts and removals.
- The container runs as non-root user `tsmhub` for security.
- Health check: `GET /healthz` returns provider health status and uptime.

### Environment variables

You can configure the container via environment variables (see `.env.example` for full list):

```bash
docker run -d \
  -e TSM_HUB_ADMIN_TOKEN=your-secret-token \
  -e TSM_HUB_SMART_STRATEGY=stability_first \
  -e TSM_HUB_SMART_STABILITY_WEIGHT=100 \
  -v "$(pwd)/data:/data" \
  -p 9070:9070 \
  tsm-hub
```

Or use a `.env` file with docker compose (already supported — just create `.env` from `.env.example`).

## Monitoring (Prometheus & Grafana)

The gateway is its own exporter — `GET /metrics` returns standard Prometheus text format
(`text/plain; version=0.0.4`, with `# HELP` / `# TYPE`), no auth and no sidecar required.
The five C-based MCP bridges expose their own `/metrics` (`mcp_gateway_*`) too.
`docker-compose.yml` ships a **Prometheus + Alertmanager + DingTalk bridge + Grafana +
blackbox-exporter** stack on the same default compose network, wired together with
container DNS names.

```bash
# 1) set a strong admin password and the DingTalk robot in .env (see .env.example):
#    GRAFANA_ADMIN_PASSWORD=<strong-password>
#    DINGTALK_WEBHOOK_URL / DINGTALK_WEBHOOK_SECRET (leave empty = log-only, no push)
# 2) start only the monitoring services (does not touch the running tsm-hub container)
docker compose up -d prometheus alertmanager dingtalk-bridge grafana blackbox-exporter
```

| | URL | Notes |
|---|-----|-------|
| Prometheus | <http://127.0.0.1:19092> | `/-/healthy`, `/targets`, `/alerts`; dual retention cap 30d / 2GB (whichever hits first) |
| Alertmanager | <http://127.0.0.1:19093> | grouping, silences, inhibition; unauthenticated UI, loopback only |
| Grafana | <http://127.0.0.1:19091> | datasource + "tsm-hub 网关监控" dashboard auto-provisioned |
| DingTalk bridge | no host port | reachable only as `dingtalk-bridge:8060` inside the compose network |
| blackbox-exporter | no host port | only `blackbox-exporter:9115` inside compose; `/probe` is unauthenticated, never publish it to the host |
| 5 MCP bridges | no host port | `mcp-{fs,memory,think,fetch,git}:8000`, container DNS only |

**Image sizes and resource limits** (2026-11-15): the five monitoring images total
about **1.01GB -> 0.92GB**. The DingTalk bridge moved from `python:3.11-slim` (150MB) to
`python:3.11-alpine` (58.9MB): it only needs the Python stdlib, and Alpine's bundled CA
certificates are sufficient for the signed HTTPS webhook. Grafana 11.4.0 has no Alpine
tag (only the default 479MB and a larger `-ubuntu` 539MB), and Prometheus / Alertmanager /
blackbox-exporter each ship a single official image, so those four stay as-is. All five
services now have `mem_limit` / `cpus` set to 4-8x their measured RSS (512MiB + 0.5 CPU
for Prometheus and Grafana, 128-192MiB + 0.25 CPU for the rest) to cap runaway growth
without affecting normal scraping, and Prometheus also has
`--storage.tsdb.retention.size=2GB` to bound TSDB disk usage. Note that `docker compose images`
shows 136MB for four MCP bridges and 150MB for one, but they share the same image layers —
only one copy is stored on disk.

**Grafana login**: open <http://127.0.0.1:19091/>, user `admin`, password is
`GRAFANA_ADMIN_PASSWORD` from `.env` (compose falls back to `admin` when unset).
`GF_SECURITY_ADMIN_PASSWORD` only applies when the `grafana-data` volume is first
initialized — once that volume exists, editing `.env` and restarting does **not**
change the password. Change it in the UI, or run:

```bash
docker compose exec grafana grafana cli admin reset-admin-password '<new-password>'
```

The dashboard is the **default home dashboard** (via
`GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH`), so logging in lands on it directly.
It has **39 panels** (v9):

| Panels | Coverage |
|--------|----------|
| 1–13 | scrape `up`, uptime, request rate, overall error rate, healthy provider **ratio**, firing alerts, per-provider error rate / QPS / EWMA latency / health, HTTP status rate, quota denials, firing alert timeline |
| 14–20 | token usage, cumulative cost (own 24h window), cooldown remaining, half-open and consecutive failures, scene request rate and tool source, MCP/A2A snapshot, Go runtime / config entities |
| 21–25 | Alertmanager `up`, DingTalk delivery failures, active alerts, active silences, delivery vs rule-firing trend |
| **26–29** | **HTTP duration histogram p50/p95/p99, request rate by route/method, blackbox probe success, probe duration** |
| **30–35** | **MCP bridge up count, probe successes, active sessions, 5xx count, plus bridge session/slot/inflight and request/error rate trends** |
| **36–39** | **Upstream failure classes (quota / auth / rate_limit / …), streaming vs buffered request rate, first-byte p50/p95/p99, and HTTP total-duration p95 split by flow** |

There are four variables at the top (`Provider`, `Scene`, `MCP 桥`, `Route`;
multi-select, All by default), plus Prometheus / Alertmanager UI shortcuts.
Quota denials uses `or vector(0)` so an empty metric family shows 0 instead of a
blank panel. Edit the JSON in the repo — the file is mounted read-only and the
provider reloads it every 30s.

Every scrape target is a **container DNS name** (`tsm-hub:9070`, `mcp-*:8000`,
`blackbox-exporter:9115`), not `127.0.0.1:19070` — inside the Prometheus container
that address is its own loopback. The three UI ports bind to `127.0.0.1` only;
the bridges, probe and DingTalk bridge publish no host port at all.

| File | Purpose |
|------|---------|
| `deploy/prometheus.yml` | scrape config (15s) + `rule_files` + `alerting.alertmanagers`; 7 jobs: `tsm-hub` / `prometheus` / `alertmanager` / `mcp-bridges` / `blackbox-exporter` / `blackbox-healthz` / `blackbox-mcp`. The `tsm-hub` job has guards (`sample_limit: 5000`, `label_limit: 32`, `label_value_length_limit: 128`) and a metric-name allowlist; new jobs have their own keep rules |
| `deploy/prometheus.rules.yml` | **12 alerts**: 7 business alerts (target down, upstream error rate >30% with >=5 requests / 5m, provider removed, quota denials, EWMA latency >30s gated on fresh successes, **401/403 auth failures**, **streaming first-byte p95 >10s**) + 2 notification-pipeline self-checks + 3 blackbox checks (`/healthz` failure, MCP bridge `/health` failure, probe itself down). 402 quota exhaustion / 429 throttling are routine on free tiers, so they are dashboard-only, deliberately not alerts |
| `deploy/alertmanager.yml` | grouping / dedup / inhibition / resolved notices; webhook receiver points at `dingtalk-bridge:8060` |
| `deploy/blackbox.yml` | two probe modules: `http_healthz` (requires `status=ok` in `/healthz` JSON) and `http_2xx` (MCP bridge `/health`) |
| `deploy/dingtalk_bridge.py` | stdlib only: Alertmanager webhook -> DingTalk markdown + HMAC signing; see below |
| `deploy/mcp_gateway.c` | `GET /metrics` for the C bridges, emitting `mcp_gateway_*`; both bridge Dockerfiles share this source |
| `deploy/grafana/provisioning/` | Prometheus datasource (`uid: prometheus`) + dashboard provider |
| `deploy/grafana/dashboards/tsm-hub.json` | 39 panels (v9, panel 36 now documents the counter-scope caveat), four variables; cost panel uses its own 24h window |

**Metric groups** (all names below omit the common `tsm_hub_` prefix; every group reuses
existing in-memory aggregates — no new state, no business-path changes):

| Group | Metrics | Source |
|-------|---------|--------|
| Core 7 | `uptime_seconds`, `http_requests_total{status}`, `quota_denials_total{code}`, `upstream_{healthy,requests_total,errors_total,latency_ms}{provider}` | `Server` counters + `router.Tracker.Snapshot()` |
| HTTP duration histograms | `http_request_duration_seconds_{bucket,sum,count}{route,method,flow,le}`, `http_first_byte_seconds_{bucket,sum,count}{route,method,le}` | the `elapsed` value already computed in `withLogging` plus `statusRecorder`'s first `Flush()` timestamp; 13 buckets (10ms → 120s) + `+Inf`, no new instrumentation. `flow=stream\|buffered` separates SSE long-lived connections from one-shot responses; the first-byte histogram only has streaming samples |
| Upstream failure classes | `upstream_error_classes_total{provider,class}` | `router.Tracker` increments this at the same place as `errors_total`; `class` is one of 8 fixed enums (quota / auth / rate_limit / model_not_found / bad_request / server_error / network / other), error bodies never become labels |
| Health state machine | `upstream_{half_open,consecutive_failures,cooldown_remaining_seconds,last_success_timestamp_seconds}{provider}` | Same `ProviderHealth` used by the admin Providers page |
| Usage / cost | `usage_{requests_total,tokens_total,cost_usd_total,errors_total,failovers_total,avg_latency_ms}{provider}`, `usage_scene_{requests_total,tokens_total}{scene}`, `failover_candidates_total{provider}`, `tool_calls_total{source}` | `quota.Recorder`, replayed from `data/usage/*.jsonl` at startup (survives restarts) |
| Subsystems / runtime | `mcp_servers{state}`, `mcp_tools`, `a2a_{tasks_active,capabilities,peer_mode}`, `config_entities{entity}`, `runtime_{goroutines,heap_alloc_bytes,cpu_cores}` | `Proxy.MCPStatuses()`, `A2A.AdminOverview()`, `store.List*()`, `runtime.MemStats` |

Two more metric sets **do not use the `tsm_hub_` prefix**:

| Source | Metrics | Notes |
|--------|---------|-------|
| 5 C-based MCP bridges (`GET /metrics`) | `mcp_gateway_{up,uptime_seconds,sessions,sessions_max,sse_active,inflight}`, `mcp_gateway_http_{requests_total{route},responses_total{status}}`, `mcp_gateway_sessions_{created,closed}_total` | `mcp_server` is a fixed enum from compose; `/metrics` does not consume a session slot, so a slow RPC cannot block a scrape |
| blackbox-exporter | `probe_success`, `probe_duration_seconds`, `probe_http_status_code`, … | `instance` is a short name from config (`tsm-hub-healthz`, `mcp-fs`, …); the full URL only travels as `__param_target` and never becomes a label |

Caveats:

- Always query counters with `rate()` / `increase()`, not raw values. HTTP/quota counters,
  **both histograms and `upstream_error_classes_total`** are in-memory and reset on restart
  (not part of the SQLite health snapshot); `upstream_requests_total` / `errors_total` may
  be restored from that snapshot; `usage_*` is replayed from `data/usage/*.jsonl`, so the
  cost panel plots the cumulative value directly. Failure classes share the exact `++` site
  with `errors_total`, so every failure counted in the health tracker lands in exactly one
  class; model-404 (`ModelFault`) and client aborts are excluded from health tracking by
  design and therefore do not appear here either.
  **Do not compare the two absolute values**: `errors_total` restores its historical
  cumulative value from the SQLite snapshot (e.g. still 1720 after a restart), while the
  class counters reset to zero on restart, so "sum of classes == total errors" only holds
  within a single process lifetime. Always debug with `increase()`, never raw counters.
- **Three different "latency" metrics**: `upstream_latency_ms` is an **EWMA gauge** for a
  single upstream attempt (no p95/p99 of its own); `http_request_duration_seconds` is a
  **histogram of whole HTTP requests**; `http_first_byte_seconds` is the histogram up to
  the **first `Flush()` (first byte)** of a streaming response — what users actually wait
  for before the first token. All histograms are queried with
  `histogram_quantile(0.95, sum by (le) (rate(..._bucket[10m])))`. They only cover routes
  wrapped by `withLogging` (`/v1/*`, `/api/admin/*`, `/a2a`); `/healthz`, `/metrics`,
  `/peer/*` and static assets are deliberately excluded so a `/metrics` scrape cannot
  inflate its own counters. Total duration carries a `flow` label: SSE includes the whole
  stream generation (minutes), so filter on `flow="buffered"` for ordinary-request p95;
  panel 39 draws both lines. The first-byte histogram has streaming samples only, because
  buffered responses never call `Flush()`. On a cold start with no business traffic the
  families emit HELP/TYPE but no samples, so a blank panel is expected. If counters do not
  change at all inside the window, `rate()` is 0 everywhere and `histogram_quantile()`
  returns **NaN** — normal Prometheus behaviour under low traffic, verified to recover as
  soon as requests keep arriving (not a broken instrumentation).
- The EWMA is updated **only on a successful upstream call**. `internal/router/health.go`
  applies a **15-minute lazy TTL**: `Latency()` / `Health()` / `Snapshot()` return 0 once
  a provider has had no success for 15m, so a stale value restored from the SQLite health
  snapshot can no longer masquerade as current latency or skew `Router.Pick` downranking.
  The alert `TSMHubUpstreamLatencyHigh` additionally requires fresh successes in the last
  15m as a defensive second guard.
- In blackbox probing, `degraded=true` **still counts as success**: an unhealthy provider
  is a provider-level issue handled by `TSMHubProviderUnhealthy`; the probe only answers
  "is the gateway responding at all". `TSMHubBlackboxExporterDown` watches the probe
  itself — without it, an all-absent `probe_success` cannot be attributed.
- Cumulative token / cost / scene / tool-source metrics exist, but there are deliberately
  **no per-key, per-model or per-tool dimensions** — those are high-cardinality. Per-key
  detail stays in `data/usage/*.jsonl` and `/api/admin/usage`.
- `/metrics` is **unauthenticated** and exposes provider names, request/error counts,
  latency and cost magnitude. Keep it on loopback; never rebind 9070/19070 to `0.0.0.0`.
  New labels may only be IDs from the existing provider list or a finite in-code enum
  (`scene` / `source` / `state` / `mode` / `entity` / `kind` / `status` / `code` /
  `route` / `method` / `flow` / `class` / `le`); API keys, `key_id`, raw URL paths,
  model/tool names, session/task IDs, upstream error text and client IPs are forbidden.
  `class` comes only from `classifyUpstreamError()` parsing the fixed `upstream <3-digit
  status>` prefix — the error body (which may contain URLs or key fragments) is never
  exported; `flow` is only ever `stream` or `buffered`. `metricRoute()`
  folds all admin subpaths into `admin` and all `/v1/*` into `inference`; `metricMethod()`
  lowercases and allows only 7 standard methods, everything else becomes `other`.
  `internal/api/metrics_test.go` checks every label name and value against that
  whitelist (values must match `[a-z0-9_-]+`; `le` is validated separately as a number
  or `+Inf`); the `sample_limit` and metric-name allowlist in `deploy/prometheus.yml`
  are a second guard. Verify with `go test ./...` and `promtool check config`.
  The C bridge `/metrics` likewise emits only fixed enums — no `g_cmd` (which contains
  host absolute paths), session id, URL or request body.

After editing rules, hot-reload without restarting:
`curl --noproxy '*' -X POST http://127.0.0.1:19092/-/reload`;
after editing `deploy/alertmanager.yml`:
`curl --noproxy '*' -X POST http://127.0.0.1:19093/-/reload`.

### DingTalk alert notifications

```
Prometheus (rules) -> alertmanager:9093 -> dingtalk-bridge:8060 -> DingTalk group robot
                      group/dedup/inhibit    translate + HMAC sign
```

Alertmanager's webhook payload is not a DingTalk message, and DingTalk's "signed"
security mode requires a fresh `timestamp` + `HMAC-SHA256(secret, ts+"\n"+secret)`
on every request. `deploy/dingtalk_bridge.py` (stdlib only, no published host port)
does both. Of the 12 alerts, `TSMHubAlertmanagerDown` and
`TSMHubDingtalkNotificationsFailing` are **self-referential**: when the notification
pipeline is down they cannot be delivered either — dashboard panels 21–25 exist for
exactly that case. The 3 blackbox alerts are likewise undeliverable if the probe
itself dies; `up{job="blackbox-exporter"}` and panels 28–31 cover that gap.

**Setup**: secrets live only in `.env` (gitignored, keep mode `0600`).
`DINGTALK_WEBHOOK_URL` / `DINGTALK_WEBHOOK_SECRET` point at one DingTalk group
robot; copy the two values from your existing alerting setup (variable names may
differ there). The signing algorithm is the standard DingTalk HMAC-SHA256 scheme.
`DINGTALK_KEYWORD=告警` covers robots using
a custom-keyword rule (the word appears in both title and body) — change it if your
robot uses a different keyword. `DINGTALK_DRY_RUN=1` logs only, without pushing.

> `docker compose config` echoes resolved env vars (including the webhook token).
> Always validate with `docker compose config --quiet`; never paste its output anywhere.

**Dry run first**: set `DINGTALK_DRY_RUN=1` in `.env`, then
`docker compose up -d dingtalk-bridge` (a recreate is required to inject the new value)
before sending a test alert. Flip it back to `0` and recreate once verified.

**Send a test alert** (real push; `group_wait: 30s`, so it lands within ~30s):

```bash
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
end=$(date -u -v+2M +%Y-%m-%dT%H:%M:%SZ)   # macOS; Linux: date -u -d '+2 minutes' ...
curl --noproxy '*' -X POST http://127.0.0.1:19093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d "[{\"labels\":{\"alertname\":\"TSMHubNotificationTest\",\"severity\":\"info\"},\"annotations\":{\"summary\":\"DingTalk pipeline smoke test\",\"description\":\"Safe to ignore; the alert ends in 2 minutes\"},\"startsAt\":\"$now\",\"endsAt\":\"$end\"}]"
```

The group first receives the "firing" message, then a "resolved" message once the
alert ends (`endsAt` expires or it is resolved explicitly) — `send_resolved: true`
is enabled. The resolved notice is **not immediate**: Alertmanager delays it by its
grouping policy, about 1–2 minutes end-to-end in our test. That is deliberate, so a
flapping upstream cannot spam the group with resolve/re-fire pairs. A single request
can also be dry-run inside the container with an `X-Dingtalk-Dry-Run: 1` header.

**Day-to-day**: create silences in the Alertmanager UI
(<http://127.0.0.1:19093/#/silences>); `TSMHubDown` inhibits all `warning|info`
alerts so a gateway outage does not flood the group with downstream symptoms;
`group_by: [alertname, provider]` merges related alerts and `repeat_interval: 4h`
repeats unresolved ones every 4 hours.

| Symptom | Check |
|---------|-------|
| No message at all | ① `curl --noproxy '*' http://127.0.0.1:19093/api/v2/alerts`; ② `docker compose logs dingtalk-bridge` for `dry-run` / `configured=false`; ③ delivery panels at the bottom of the dashboard |
| `errcode=310000` | keyword or signature mismatch — re-check `DINGTALK_KEYWORD` and the secret against the robot settings |
| `errcode=300005` | invalid token / robot removed — copy the URL again |
| Resolved notice is late | usually 1–2 min (grouping policy); if it never arrives, check `send_resolved` and whether Alertmanager restarted before the resolve |
| `up{job="alertmanager"}=0` | notifications are down; alerts stay firing in Prometheus only, a resolved notice arrives after recovery |
| `probe_success` all 0 or absent | check `up{job="blackbox-exporter"}` first: 0 means the probe died, 1 means a target is really unreachable |
| MCP bridge panels (30–35) blank | bridge images still run the old C binary without `GET /metrics`; run `docker compose build mcp-fs mcp-git` then `docker compose up -d` (not covered by `make alert-check`) |
| HTTP p95 panel blank | no business request has passed through `withLogging` since restart; the histogram family has HELP/TYPE but no samples. Send one `/v1/models` request |
| First-byte / `flow=stream` panels blank | only responses that called `Flush()` produce first-byte samples and `flow="stream"` series; send a `/v1/chat/completions` with `"stream":true`, or check that the client did not downgrade to buffered mode |
| All failure classes are 0 | `upstream_error_classes_total` only counts failures tracked by the health state machine; model-404 (`ModelFault`) and client aborts (`ClientAbort`) are excluded by design — check quota denials / usage errors instead |

**Privacy boundary**: URL / secret are interpolated from `.env` into the bridge
container only — never into yml, dashboards or commits. The bridge logs just
`scheme://host/path?...`, and `redact()` strips `access_token` / `sign` / secret
literals from exceptions. Messages contain only `alertname / severity / provider /
summary / description / startsAt`; high-cardinality or sensitive labels such as
`api_key`, `session_id` and `generatorURL` are never rendered
(`deploy/test_dingtalk_bridge.py`, run by `make alert-check`, asserts this).
The Alertmanager UI is also unauthenticated — keep 19093 on loopback.

## Config privacy gate

`data/config.json` and every `config.json.bak-*` must be mode `0600`, and every
`providers[].api_key` must be an `env:TSM_HUB_KEY_*` reference or `enc:v1:…`
ciphertext (AES-256-GCM at rest, enabled by `TSM_HUB_MASTER_KEY`) — never a
literal upstream key. Historical backups are kept as rollback points but
redacted in place.

```bash
make privacy-check                                    # read-only; exit 1 on bad perms / plaintext key
python3 -S scripts/config-privacy-check.py --redact   # redact *.bak-* only (never the live config)
```

`--redact` replaces literal keys with `env:TSM_HUB_KEY_<ID>` so a restored backup
keeps working with the current `.env`. To migrate a literal key in the live config:
put the value in `.env`, set `api_key` to `env:TSM_HUB_KEY_<ID>`, add the same
variable to the `tsm-hub` service in `docker-compose.yml`, then
`docker compose up -d tsm-hub` (recreate to inject the variable; no rebuild needed).

---

## Testing

```bash
make check         # Aggregate local gate: fmt-check + test + vet + privacy-check + tree-privacy-check + dashboard-check + web-lock-check (no containers needed)
make test          # All Go packages (runs the fmt-check gate first)
make fmt-check     # Fails on any gofmt -l output
make cover         # Coverage (auto-falls back to a complete Go 1.26.11 SDK under ~/sdk; override with GO=...)
make cover-check   # Run cover, then enforce the COVER_MIN floor (self-tests the parser first)
make vulncheck     # Full govulncheck report (pinned version; needs network for the Go vuln DB)
make vulncheck-gate # CI gate: all reachable vulns fail (VULN_STRICT=1 by default)
make privacy-check # config.json* permissions + plaintext key gate
make tree-privacy-check # Prospective commit content: credentials, build paths, private IDs, private IPs
make dashboard-check # Dashboard structure + PromQL shape for both dashboards (stdlib only, no containers)
make web-lock-check # pnpm lock matches package.json; npm fallback lock is not tracked
make install-hooks # Enable the versioned pre-commit guard (disable with make uninstall-hooks)
make alert-check   # DingTalk unit tests + MCP bridge /metrics smoke + dashboard static + live PromQL queries + AM/Prometheus config (needs the stack up)
```

### CI vs local guards

`.github/workflows/ci.yml` runs four parallel jobs on a clean Linux runner for every
push to `main`, every PR, and manual `workflow_dispatch`:

- `gates`: `make check` (all seven gates), `make test-race`, `make build`;
- `image`: `docker build` proving the alpine/musl + CGO + external-linkmode path and
  the `//go:embed` placeholder directory still produce a release image (the Dockerfile
  compiles Go only, so CI needs no Node/npm);
- `coverage`: `make cover-check` — regenerates `coverage.out` and enforces the
  `COVER_MIN` floor (a missing/empty/malformed profile is a hard failure). The floor
  is set from the clean-clone measurement, where frontend-asset tests SKIP because
  `internal/web/dist/browser` only has `.gitkeep`;
- `vuln`: `make vulncheck-gate` with a pinned `govulncheck` version. **All reachable
  vulnerabilities fail the job**, including standard-library ones: `go.mod` pins Go
  1.26.11 (the patch level that fixes the 12 advisories left behind by the 1.25
  branch), so `VULN_STRICT=1` is the default and stays green. `VULN_STRICT=0` is
  only a temporary escape hatch if the toolchain ever lags; the raw JSON report is
  uploaded as an artifact.

Permissions are reduced to `contents: read`, and `pull_request_target` is deliberately
avoided so test code from a fork PR can never execute in a write-capable context.
The Go version comes from `go.mod`; Python is pinned to 3.12. See
[SECURITY.md](./SECURITY.md) for how to report a vulnerability.

The local pre-commit guard (`make install-hooks`) is versioned, but `core.hooksPath`
lives in `.git/config` and **is not distributed with a clone** -- run `make install-hooks`
once after cloning. CI is the backstop for collaboration and PRs.

CI intentionally does not run `make alert-check` (11 monitoring containers plus a running
Prometheus for `--live` expression parsing); it stays manual. The `github-actions` entry
in `.github/dependabot.yml` now has a workflow to keep up to date.

| Package | Coverage |
|---------|----------|
| `internal/api` | E2E: auth, failover, SSE, rate limiting, key lifecycle, /v1/models, config persistence, provider response header, 4xx no circuit break, upstream pricing, login rate limit, 413, panic fallback, healthz health, env key, /metrics gating and label whitelist, histogram format / route folding / flow split / first-byte family / nil safety / endpoint privacy, `withLogging` streaming Flush instrumentation, zero-value error-class matrix and no error-body leakage; admin helper pure functions: token redaction, provider ID validation, upstream quota/suggestion JSON parsing, MCP server-name extraction |
| `internal/proxy/adapter` | Registry / URL / auth / request-response conversion / stream-event contracts for all 13 protocol adapters, AWS SigV4 helpers, and the 25-platform catalog; coverage 8.9% -> **86.6%** |
| `internal/proxy` | Request body rewrite, `stream_options` injection, SSE usage parsing, URL concatenation (forwarding main chain covered by `internal/api` e2e tests); agent message parsing, last user text, allowlist sets, calculator function bounds |
| `cmd/server` | Config file permissions and plaintext-key warnings, env references / encrypted secrets excluded from the count, console URL normalization, log levels |
| `internal/router` | Priority sorting, weight preference, latency penalty, health removal and half-open pass, **error-class enums / prefix parsing / same-site counting with errors / deep copy** |
| `internal/store` | Atomic write, update rollback, route cleanup on provider deletion, Key CRUD |
| `internal/quota` | Aggregation and replay, RPM window, four quota rejections, TopKeys, recent logs, lazy cleanup of stale key entries and rate-limit regression |
| `internal/auth` | Key format, Bearer parsing, constant-time comparison, session expiry |
| `deploy` | 16 DingTalk bridge unit tests: signing vectors, alert/resolved message layout, errcode handling, URL and secret redaction; `test_mcp_gateway_metrics.sh` compiles the C bridge locally and checks all 10 `/metrics` families, counters and the privacy boundary (`make alert-check`) |

`make smoke` has an additional script-level e2e suite (real process startup + curl assertions).

## Notes

- **Self-made key plaintext appears only once**: after issuance API returns, server only keeps hash; if lost, must re-issue.
- **Upstream keys recommended to use `env:VAR` references**, avoiding secrets on disk; plaintext also supported (simpler deployment).
- **Startup security self-check**: if admin password is still default placeholder, no providers configured, `config.json` is group/other-readable (not 600), plaintext upstream API keys are detected (recommend `env:` references), or TLS is not enabled, WARN logs are printed on startup.
- **Quota alerts**: When key usage reaches 80% of any limit, a warn log is printed (same type only alerted once,
  resets after falling back below line), suitable for connecting to log monitoring for cost control.
- **`data/` directory contains upstream API keys and usage logs**, already in `.gitignore`, don't commit to public repos.
- **Cost is estimated**: calculated from `settings.pricing` price table, streaming scenarios depend on upstream returning usage
  (`stream_options.include_usage` auto-injected); when upstream doesn't return, tokens recorded as 0.
- Upstream 5xx automatically fails over to next candidate; **streaming responses that have already written bytes to client won't retry**
  (otherwise causes content duplication), only logs the error.
- **4xx not counted as provider failure**: only network errors / 5xx / stream disconnection before first frame trigger circuit break,
  avoiding "context too long / content moderation rejection" client-side issues removing healthy upstreams.
- When editing provider in admin console, echoed `api_key` is redacted; backend detects ellipsis and preserves original key,
  won't overwrite real key with redacted string (frontend-backend dual protection).
- Every response carries `X-Request-ID`, logs with same ID can be correlated; panic auto-falls back to 500 with that ID.
- Request body exceeding `settings.max_body_bytes` directly returns 413 (no misleading error after truncation).
- `/healthz` additionally carries `degraded` and `providers[]` fields, can distinguish "gateway down" vs "some upstream removed".

---

## Related documentation

| Document | Description |
|----------|-------------|
| [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | System architecture, request pipeline, component design |
| [SECURITY.md](./SECURITY.md) | Vulnerability reporting policy, response SLA, scope, safe harbor |
| [docs/SECURITY.md](./docs/SECURITY.md) | Security deployment guide: TLS, CORS, key management, hardening checklist |
| [docs/PRIVACY.md](./docs/PRIVACY.md) | Privacy policy: data collection, usage, storage, data subject rights |
| [docs/DPA.md](./docs/DPA.md) | Data Processing Agreement template (for B2B deployments) |
| [docs/PRIVACY_AUDIT.md](./docs/PRIVACY_AUDIT.md) | Privacy compliance audit report with findings and remediation |
| [docs/TODO.md](./docs/TODO.md) | Roadmap and planned features |

## Related Articles
- [tsm-hub: Bringing LLMs, Tools, MCPs, and Skills into One Unified Gateway](https://erishen.cn/tsm_hub-en/)

## License

[MIT](./LICENSE)
