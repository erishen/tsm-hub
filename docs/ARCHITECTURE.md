# tsm-hub 架构说明

## 1. 设计约束

| 约束 | 选择 | 代价 |
|------|------|------|
| 依赖 | Go 标准库 + modernc.org/sqlite（纯 Go，无 cgo） | 自己写路由分发、SSE 转发、限流窗口；不引入 chi / gin / redis |
| 存储 | JSON 配置 + JSONL 流水 + SQLite（记忆/审计日志） | 单机部署，多实例需要共享盘或后续迁移到 Postgres |
| 前端 | Angular 19 standalone + signals，产物 embed 进二进制 | 一个二进制交付；构建需要 Node |
| 协议 | 对外 OpenAI 兼容 API；上游协议适配器层支持 13 种协议（OpenAI/Anthropic/Azure/Gemini/Bedrock/SageMaker/Cohere/Mistral/HuggingFace/Replicate/Together/Fireworks/Groq）+ 12 家国内平台（均 OpenAI 兼容） | 适配器负责请求/响应格式转换、认证头构造、流式事件转换；新增协议只需实现 `ProtocolAdapter` 接口 |
| 安全 | Key SHA-256 哈希、审计日志、自动脱敏 | 增加 SQLite 依赖和存储开销 |

选择零运行时依赖的理由：网关是基础设施，依赖越少升级成本越低；且这个规模的问题（路由 + 限流 + 记账 + agent 工具循环）用标准库完全够。SQLite 选纯 Go 实现避免 cgo 跨平台编译问题。

## 2. 模块划分

```
cmd/server        启动、优雅退出、flag/环境变量、usage 启动清理
cmd/mockupstream  本地联调假上游（不接真实模型）
internal/config   进程参数（Listen / DataDir / AdminToken / LogLevel）
internal/store    配置模型 + 原子落盘 + 内存索引（providers / routes / keys / settings）
internal/auth     自制 Key 签发与校验、管理会话、暴力尝试防护
internal/router   路由选择（优先级 / 权重 / 延迟 / smart 成本评分）+ 健康状态机
internal/proxy    OpenAI 兼容转发、SSE 透传、usage 采集、failover、
                  agent 工具循环（内置工具 + MCP + 技能注入）、
                  会话记忆（SQLite）、Docker 沙箱、确定性快路径（fastpath + codegen）、
                  外部 tools/skills/mcps 自动发现与统计
internal/proxy/adapter  上游协议适配器层（13 种协议 + 12 家国内平台），
                        负责 OpenAI ↔ 各家协议的请求/响应/流式事件转换、认证头构造
internal/quota    限流（RPM 滑动窗口）+ 用量聚合 + JSONL 落盘 + 过期清理
internal/api      HTTP 层：/v1/* 代理、/api/admin/* 管理接口（40+ 端点）、静态前端
internal/audit    审计日志（SQLite 存储、自动脱敏、自动清理、查询过滤分页）
internal/skills   Agent Skills 技能库加载、Markdown 渲染、system prompt 注入
internal/web      embed Angular 产物 + SPA 回退
```

依赖方向单向：`api → proxy → router → store`，`quota` / `audit` / `skills` 只被 `proxy` / `api` 使用，没有循环。

## 3. 请求链路

```
HTTP 请求（最外层：panic 兜底中间件，生成 X-Request-ID 并写进响应头与日志）
  │
  ├─ /healthz           → 健康检查（不鉴权；带 degraded + providers[] 上游健康）
  ├─ /metrics           → Prometheus 文本指标（不鉴权）
  ├─ /v1/models         → 可用模型与路由别名（OpenAI 兼容，需自制 Key）
  ├─ /v1/tools          → 网关工具池目录（内置 + MCP + 条件工具，需自制 Key）
  ├─ /v1/mcps           → 挂载的 MCP server 列表（需自制 Key）
  ├─ /v1/skills         → 技能库清单（需自制 Key）
  ├─ /v1/*              → authenticate → limiter.Check（额度告警） → proxy.Handle
  ├─ /api/admin/login   → 口令换 session（同 IP 失败 5 次锁 10 分钟）
  ├─ /api/admin/*       → admin 中间件 → 各 handler（写操作自动记录审计日志）
  └─ /                  → 静态前端（SPA 回退到 index.html）
```

`proxy.Handle` 内部：

1. 解析 body 取 `model` / `stream` / `tools`；
2. **fastpath 前置**：算术/时间/单位换算/统计等纯代码可解问题直接返回，零模型调用；
3. **auto 场景路由**：model 为空或 `"auto"` 时，按请求内容自动分类（chat/reason/code/fast）；
4. `router.Pick(model)` 得到有序候选（failover / weighted / smart 成本智能）；
5. 逐个候选 `attempt`：
   - 重写 body（`model` 改成上游模型名；流式注入 `stream_options.include_usage`）；
   - **协议转换**：根据 Provider.Protocol 选择适配器，把 OpenAI 格式请求体转换为目标协议格式（如 Anthropic/Gemini/Azure 等），构造认证头与上游 URL；
   - 换 `Authorization`，合并 provider 自定义 header；
   - 上游 5xx / 网络错误 / 流式首帧前断流 → 记 provider 失败、换下一个候选；
     **4xx 客户端错误不记失败**（避免坏请求把健康上游摘除）；
   - 非流式：整包读完，**适配器把目标协议响应转换回 OpenAI 格式**，再回传（便于解析 usage）；
   - 流式：逐帧转发，**适配器把目标协议 SSE 事件转换为 OpenAI 格式**，从含 `usage` 的最后一帧取 token 数；
   - 成功响应带 `X-LLM-Router-Provider: <provider_id>` 头（流式、非流式都有）；
6. **agent 工具循环**：客户端未传 `tools` 时，网关自动附加内置工具池 + MCP 工具，
   在服务端执行 `tool_calls` 循环（默认最多 4 轮），最终返回答案；流式请求内部跑完循环后按 SSE 回放；
7. **外部发现**：记录客户端请求中声明的 tools/skills/mcps，用于后续择优录用；
8. `quota.Record` 记账（内存聚合 + JSONL 落盘；按「实际上游模型名 → 别名 → default」顺序取价）。

**重试边界**：只有"还没有向客户端写出任何字节"才换候选。流式一旦写出首帧就不再重试，否则会产生重复内容。

## 4. 路由策略

```
route.model 命中路由表？
  ├─ 是 → 用路由表 targets（strategy = failover | weighted | smart）
  └─ 否 → 兜底：所有 enabled 且声明支持该模型的 provider，按 weight 分流
```

候选排序：

1. 过滤掉 `enabled=false` 与冷却中的 provider；
2. 若全部冷却中，放行一个"半开"候选做探测（避免整体不可用）；
3. `failover`：按 `priority` 升序分组，组内按权重随机；
4. `weighted`：所有候选同池，按 `weight × 延迟因子` 加权随机排序；
5. `smart`：按可配置的多维度综合评分选候选（策略模式 + 各维度权重）。

**smart 策略模式**（`settings.smart.strategy_mode`）：

| 模式 | 成本权重 | 稳定性权重 | 延迟权重 | 适用场景 |
|------|---------|-----------|---------|---------|
| `cost_first` | 100 | 40 | 30 | 成本敏感场景，免费/低价优先 |
| `stability_first` | 30 | 100 | 50 | 生产服务，可靠性优先 |
| `task_aware` | 50 | 80 | 70 | 混合负载，平衡质量与速度 |
| `balanced` | 60 | 70 | 70 | 通用场景，各维度均衡 |

各维度评分：
- **成本**：免费模型加分 + 单价档位加分（可配置 `free_bonus` / `price_tiers`）
- **稳定性**：历史成功率 `(requests - errors) / requests` × 100，连续失败惩罚；新 provider 给 70 分鼓励探索
- **延迟**：EWMA 延迟，100ms 以内 100 分，1000ms 以上 0 分；无数据给 60 分
- **通用惩罚**：半开候选扣分，429 限流期间大幅降权（-1000）

各维度权重可通过 `settings.smart.cost_weight` / `stability_weight` / `latency_weight` 单独覆盖（0 表示用策略默认值）。也可通过环境变量 `TSM_HUB_SMART_STRATEGY` / `TSM_HUB_SMART_*_WEIGHT` 或 `.env` 文件配置。

延迟因子：`w' = w × 1000 / (1000 + latency_ewma_ms)`，即延迟 1s 时权重减半，让快节点自然多拿流量。半开候选额外乘 0.1。

**auto 场景路由**：客户端 `model` 不传或传 `"auto"` 时，网关按请求内容自动分类并走对应场景路由（`chat` / `reason` / `code` / `fast`），响应头 `X-Llm-Router-Scene` 返回命中信号。无 `auto` 场景路由时回退到 `smart` 通配路由。

## 5. 上游协议适配器层

网关对外只暴露 OpenAI 兼容 API，但上游 Provider 可能使用不同协议。适配器层（`internal/proxy/adapter/`）负责在请求发送前和响应返回后进行格式转换，让调用方无需关心上游协议差异。

### 5.1 支持的协议

| 协议 | 适配器文件 | 说明 |
|------|-----------|------|
| OpenAI | `adapter_openai.go` | 默认纯透传，绝大多数国内平台兼容此协议 |
| Anthropic | `adapter_anthropic.go` | Claude API 协议转换 |
| Azure OpenAI | `adapter_azure.go` | Azure 部署名映射、API 版本处理 |
| Google Gemini | `adapter_gemini.go` | Gemini API 协议转换、URL 中传 key |
| AWS Bedrock | `adapter_bedrock.go` | AWS SigV4 签名、InvokeModel API |
| AWS SageMaker | `adapter_sagemaker.go` | SageMaker 端点调用、IAM 签名 |
| Cohere | `adapter_cohere.go` | Cohere Chat API 协议转换 |
| Mistral | `adapter_mistral.go` | Mistral AI API 协议转换 |
| Hugging Face | `adapter_huggingface.go` | HF Inference API / TGI 协议转换 |
| Replicate | `adapter_replicate.go` | Replicate 预测 API、异步轮询 |
| Together AI | `adapter_together.go` | Together AI API 协议转换 |
| Fireworks AI | `adapter_fireworks.go` | Fireworks AI API 协议转换 |
| Groq | `adapter_groq.go` | Groq API 协议转换 |

国内平台（alibailian / sensenova / agnes / deepseek / openrouter / tokenrouter / bai / kimi 等）均使用 OpenAI 兼容协议，走默认适配器纯透传。

### 5.2 适配器接口

```go
type ProtocolAdapter interface {
    Name() string                                    // 适配器名称（与 Provider.Protocol 对应）
    UpstreamURL(provider, clientPath) string        // 构造上游请求 URL
    AuthHeader(provider) (key, value string)        // 认证请求头（key 为空表示不设置，如 Gemini 在 URL 中传 key）
    ConvertRequest(body, upstreamModel) (converted, extraHeaders, err)  // OpenAI → 目标协议
    ConvertResponse(body) ([]byte, error)           // 目标协议 → OpenAI（非流式）
    ConvertStreamEvent(data) (converted, skip, err) // 目标协议 SSE → OpenAI SSE（流式）
    StreamDoneEvent() string                         // 目标协议流结束事件标识
}
```

可选接口 `RequestSigner` 用于 AWS SigV4 等需要在请求发送前做签名的场景。

### 5.3 适配器注册表

- 启动时各适配器通过 `init()` 调用 `RegisterAdapter()` 注册到全局 `adapters` map；
- `GetAdapter(provider)` 根据 `Provider.Protocol` 字段返回对应适配器；
- 未设置 `Protocol` 或协议未知时，返回 OpenAI 适配器（默认纯透传）；
- 新增协议只需实现 `ProtocolAdapter` 接口并注册，无需修改 proxy 核心逻辑。

### 5.4 流式转换的特殊处理

流式请求的协议转换比非流式更复杂：
- 适配器逐帧接收上游 SSE 事件，调用 `ConvertStreamEvent()` 转换为 OpenAI 格式；
- 部分协议（如 Anthropic）的流式事件结构与 OpenAI 差异较大，需要累积多帧才能组装成一条 OpenAI delta；
- `skip=true` 表示该帧无需转发（如心跳、元数据帧）；
- 适配器通过 `StreamDoneEvent()` 识别上游流结束信号，确保正确转发 `[DONE]`。

## 6. 健康状态机

```
        成功                    连续失败 ≥ fail_threshold
healthy ──────► healthy   healthy ─────────────────────► cooling(down_until)
   ▲                                                          │
   │  半开探测成功（failures 清零）                             │ 冷却 cooldown_sec 结束
   └────────────────────  half-open  ◄────────────────────────┘
```

- 失败计数在成功一次后清零，避免偶发抖动导致永久摘除；
- 只有**上游服务端故障**（网络错误 / 5xx / 流式首帧前断流）才计入失败，
  4xx 这类客户端错误不摘除 provider；
- `latency_ms` 用 EWMA（`0.7×old + 0.3×new`）平滑，直接进路由权重；
- 只做被动探测（靠真实请求），不主动发心跳，避免产生额外费用；
- 上游 429（限流）短冷却（默认 60s），404（模型不存在）长冷却（默认 1800s）。

## 7. 配额与记账

- **RPM**：内存滑动窗口（保留最近 1 分钟的时间戳），超限返回 429；
- **额度**：总额度（token / USD）与每日额度，超限返回 402；
- **记账**：每次请求写一条 JSONL 到 `data/usage/YYYY-MM-DD.jsonl`，
  进程启动时回放最近 90 天重建内存聚合（损坏行跳过，不阻塞启动）；
- **自动清理**：配置 `usage_retention_days` 后，启动时自动删除过期流水文件；
- **成本**：`prompt/1000 × input_per_1k + completion/1000 × output_per_1k`，
  价格表在 `settings.pricing`，按「实际上游模型名 → 请求别名 → default」顺序取价。
  这是估算，不是账单。免费模型成本记 0。

## 8. 能力池架构

网关在代理之上叠加了一层"能力池"，让走 tsm-hub 的客户端无需自行配置通用工具、MCP、技能：

### 7.1 内置通用工具

客户端请求**不传 `tools`** 时，网关自动附加以下工具并在服务端执行循环：

| 工具 | 说明 | 安全边界 |
|------|------|----------|
| `get_time` | 服务器当前本地时间 | 无风险 |
| `calc` | 数学表达式计算 | 纯计算，无 I/O |
| `fetch_url` | 抓取网页/API 文本 | 默认拒绝内网地址防 SSRF，可配置放行 |
| `echo` | 回显文本 | 无风险 |
| `skill-run` | 加载技能库指定技能全文 | 只读技能目录 |
| `remember` / `recall` | 按 key 隔离的键值记忆 | SQLite 持久化，按 keyID 隔离命名空间 |
| `read_file` / `csv_analyze` | 读取白名单目录内文件 | 仅在配置 `agent.read_root` 时提供，不可越界 |
| `execute_code` | Docker 沙箱执行代码 | 仅在 `sandbox.enabled=true` 时提供，禁网络/只读根/丢弃 capabilities |
| `query_exchange_rate` | 实时汇率查询 | 调用公开免费 API |
| `system_info` | 服务器 OS/CPU/内存/磁盘 | 只读系统信息 |

### 7.2 MCP Server

`settings.mcps` 配置后，网关启动/首请求时连接 MCP server（stdio 子进程或 Streamable HTTP 远程），
完成握手并拉取工具列表，以 `mcp_<server>_<tool>` 命名加入 agent 工具池，由网关在服务端执行。
懒连接 + 失败自动重建。常用 MCP（filesystem / sequential-thinking / memory / serena）预装。

### 7.3 Agent Skills

`settings.skills_dir` 指向技能库目录（如 resolve-skills/skills），网关加载技能清单，
支持 `list`（技能清单注入 system prompt）/ `all`（全部技能全文）/ 单个技能名三种注入模式，
由 `keys[].inject_skills` 按 Key 配置。`skill-run` 工具可按需加载单个技能全文。

### 7.4 确定性快路径（fastpath）

算术、当前时间、日期计算、单位换算、数字统计、进制转换、字数统计等纯代码可解问题
**直接返回，零模型调用、零上游消耗**。内置匹配器未命中时，可由 LLM 生成 JS 检测器
（goja 沙箱校验，无宿主 API/无 I/O/无网络/3s 超时/禁 eval），命中即持久化为插件，
后续同类问题零模型直接命中。管理台可测试、生成、晋升（可选 fastpath 插件或 Tools）、删除。

### 7.5 会话记忆

`remember` / `recall` 工具提供按 Key 隔离的键值记忆，SQLite 持久化（`data/memory.db`），
网关重启后记忆保留。命名空间 `ns = "mem:<keyID>:<key>"`，不同 Key 的记忆互相隔离。

### 7.6 Docker 沙箱

`execute_code` 工具在一次性 Docker 容器中执行代码（python/js/shell/java/go/rust/c/cpp），
安全边界：禁网络（`--network none`）、只读根文件系统（仅 /tmp 可写）、丢弃全部 capabilities、
禁提权、内存/CPU/进程数/文件描述符限制、超时自动 kill 并清理容器，执行完自动销毁。

## 9. 外部发现与晋升机制

网关自动记录调用方请求中声明的 `tools`、`skills`、`mcps`，在管理台展示为外部候选：

- **外部工具候选**：记录调用方声明的工具名和使用次数，管理员可查看详情，择优"录用"为网关内置工具或快路径插件；
- **外部技能候选**：记录调用方声明的技能名，可接入技能库；
- **外部 MCP 候选**：记录调用方声明的 MCP server，可接入网关工具池（需配置启动命令或 URL）。

这样外部调用方的最佳实践可以沉淀为网关的通用能力，后续调用方无需重复声明。被忽略的候选进入忽略列表，可手动恢复。

## 10. Key 安全

- 明文格式 `sk-tr-<48 hex>`，创建接口返回一次，2 分钟内可补看，超窗后服务端只存哈希；
- 库中只存 `sha256(明文)`，用 `constant-time` 比较；
- 管理 API 返回的 provider `api_key` 一律脱敏（`abcd…wxyz`）；
  配置为 `env:VAR` 引用的回显引用本身（不含密钥），真实 Key 由环境变量注入；
- 编辑 provider 时若 api_key 字段带脱敏省略号，后端不会覆盖原值；
- **额度告警**：`limiter.CheckWarn` 在请求通过限流后检查用量是否 ≥ 任一上限的 80%，
  跨线只提示一次（回落线下后重置），写 warn 日志供成本监控；
- 日志中只打印 key.Prefix（前缀）和 key.ID（哈希 ID），不打印完整 Key。

## 11. 审计日志

所有管理写操作（创建/修改/删除 Provider、路由、Key、MCP、设置、清空 usage/记忆等）自动记录审计日志：

- **存储**：`data/audit.db`（SQLite，WAL 模式）；
- **记录内容**：时间、操作类型（create/update/delete/toggle/clear/upsert）、对象类型（provider/route/key/mcp/settings/usage/memory/fastpath）、对象 ID、详情、操作者（admin token 前缀或 session ID）、客户端 IP、User-Agent；
- **自动脱敏**：详情中的 `api_key`/`token`/`password`/`secret` 字段和 `sk-` 开头 Key 自动替换为 `***`；
- **自动清理**：默认保留 90 天，可通过 `audit_retention_days` 配置，每次写入后异步清理过期记录；
- **查询**：管理 API `GET /api/admin/audit-logs` 支持按对象类型/操作类型/对象 ID 过滤 + 分页，管理台「审计日志」页展示。

## 12. 持久化格式

`data/config.json`（原子写：临时文件 → fsync → chmod 600 → rename）：

```jsonc
{
  "version": 1,
  "settings": {
    "listen": ":9070",
    "admin_token": "...",
    "default_timeout_ms": 60000,
    "max_body_bytes": 10485760,
    "fail_threshold": 3,
    "cooldown_sec": 120,
    "pricing": { "default": {"input_per_1k": 0.001, "output_per_1k": 0.002} },
    "smart": { "free_bonus": 100, "throttle_sec": 60 },
    "skills_dir": "resolve-skills/skills",
    "agent": { "max_rounds": 4 },
    "mcps": { "fs": {"command": "...", "args": ["..."]} },
    "sandbox": { "enabled": true, "timeout_seconds": 30 },
    "audit_retention_days": 90,
    "usage_retention_days": 0
  },
  "providers": [ /* id, base_url, api_key, models, weight, priority ... */ ],
  "routes":    [ /* model, strategy, targets[], remark */ ],
  "keys":      [ /* id, prefix, hash, quota, inject_skills ... */ ]
}
```

`data/usage/2026-09-07.jsonl`（每行一条请求）：

```json
{"ts":"...","key_id":"...","model":"smart","provider_id":"backup",
 "upstream_model":"gpt-4o","prompt_tokens":9,"completion_tokens":5,
 "total_tokens":14,"cost_usd":0.0001,"latency_ms":312,"stream":false,"status":200,
 "scene":"chat","fastpath":"","attempt":1,"failover":[]}
```

`data/memory.db`（SQLite）：会话记忆键值存储，按 keyID 隔离命名空间。

`data/audit.db`（SQLite）：审计日志，见第 10 节。

`data/fastpath_plugins/`、`data/fastpath_promoted/`：快路径插件 JS 文件，按 mtime 热重载。

## 13. 容器化部署

### 13.1 构建策略

采用多阶段构建（`Dockerfile`）：
- **构建阶段**：`golang:1.25-alpine`，编译 Go 二进制（CGO_ENABLED=1，external linkmode 兼容 musl）
- **运行阶段**：`alpine:3.20`，仅包含二进制 + ca-certificates + tzdata，以非 root 用户 `tsmhub` 运行

前端构建产物不在镜像内编译（避免镜像内装 Node.js），而是由宿主机 `make web-build` 构建后同步到 `internal/web/dist/browser/`，通过 `go:embed` 嵌入二进制。`make docker-build` 和 `make docker-up` 会自动先运行 `web-build`，确保管理台始终是最新版本。

### 13.2 数据持久化

容器通过卷挂载 `./data:/data` 持久化所有数据：
- `config.json` — 全部配置（providers / routes / keys / settings）
- `usage/YYYY-MM-DD.jsonl` — 用量流水
- `memory.db` — SQLite 会话记忆
- `audit.db` — SQLite 审计日志
- `unavailable.json` — 模型不可用冷却状态
- `fastpath_plugins/`、`fastpath_promoted/` — 快路径插件

容器删除和重建不会丢失数据，只需保持 `./data` 目录完整。

### 13.3 配置注入

优先级：**命令行 flag > 环境变量 > .env 文件 > data/config.json**

容器支持通过环境变量覆盖配置（见 `.env.example`）：
- 基础配置：`TSM_HUB_ADDR`、`TSM_HUB_DATA_DIR`、`TSM_HUB_ADMIN_TOKEN`、`TSM_HUB_LOG_LEVEL`
- 智能路由：`TSM_HUB_SMART_STRATEGY`（cost_first / stability_first / task_aware / balanced）、`TSM_HUB_SMART_*_WEIGHT`
- 上游密钥：`OPENAI_API_KEY`、`DEEPSEEK_API_KEY` 等（配合 config.json 中的 `env:VAR_NAME` 引用）

`docker compose` 自动加载项目根目录的 `.env` 文件。

### 13.4 健康检查与运维

- **健康检查端点**：`GET /healthz`，返回所有 provider 健康状态、请求数、运行时间
- **docker compose healthcheck**：每 30s 检查一次，超时 5s，重试 3 次
- **Makefile 运维命令**：`docker-up` / `docker-down` / `docker-stop` / `docker-start` / `docker-restart` / `docker-logs` / `docker-status`
- **配置热加载**：`config.json` 变更后自动热重载（fsnotify），无需重启容器

### 13.5 安全设计

- 容器以非 root 用户 `tsmhub` 运行，最小权限原则
- 数据目录挂载需确保容器内用户有读写权限
- 生产环境建议启用 TLS（`settings.tls`）或前置反向代理（Nginx / Caddy）
- Admin Token 建议通过环境变量注入，不写入 config.json

## 14. 管理台架构

Angular 19 standalone + signals，18 个页面，全部懒加载：

| 分组 | 页面 |
|------|------|
| 网关 | Providers、路由表、Token Keys |
| 模型与额度 | 模型目录、应用推荐、额度查询 |
| 能力池 | 技能库、MCP、工具、记忆、沙箱、快路径 |
| 观测 | 用量、监控、审计日志 |
| 调试 | 测试（Playground） |

管理台特性：深色模式（localStorage 持久化，首次跟随系统）、全局搜索（Ctrl+K 或 `/`，并行搜索六类资源）、
批量操作（providers/routes/keys 复选框批量启用/禁用/删除）、导出 CSV（usage/observability）、
自动刷新开关（dashboard/usage）、PWA（manifest + 图标，支持添加到主屏幕）、键盘快捷键。

## 15. 已知边界与后续

见 `TODO.md`。主要待办：多实例下的共享状态、Key 速率限制确认、Provider API Key 加密存储、
按 Key 的用量清理、流式 token 估算兜底、Prometheus 延迟分位指标。
