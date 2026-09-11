# tsm-hub

> 自制 Token Key + 智能路由的 LLM 网关：对外只暴露自己签发的 `sk-tr-…` Key，
> 内部把 OpenAI 兼容请求智能路由到配置好的多家上游模型。

- **后端**：Go 1.22，**零第三方依赖**（只用标准库 + modernc.org/sqlite 纯 Go 实现），单二进制
- **前端**：Angular 19（standalone + signals），构建产物 `embed` 进 Go 二进制
- **存储**：JSON 配置文件（原子写）+ JSONL 用量流水 + SQLite（记忆/审计日志），无需外部数据库
- **协议**：客户端 OpenAI 兼容（`/v1/chat/completions`、`/v1/models`…），含 SSE 流式透传；**上游协议适配层**支持 13 种协议（OpenAI/Anthropic/Azure/Gemini/Bedrock/SageMaker/Cohere/Mistral/HuggingFace/Replicate/Together/Fireworks/Groq）+ 12 个国内平台（均 OpenAI 兼容）
- **能力池**：内置通用工具 + Agent Skills 技能库 + MCP server + Docker 沙箱 + 会话记忆
- **智能路由**：优先级 failover、权重分流、smart 成本智能路由、auto 场景路由、确定性快路径
- **安全合规**：Key SHA-256 哈希存储、审计日志、用量自动清理、上游 Key `env:` 引用、[安全部署指南](./docs/SECURITY.md)
- **管理台**：18 个页面（概览/Providers/上游平台/路由表/Keys/模型/额度/用量/监控/技能库/MCP/工具/记忆/沙箱/快路径/应用推荐/测试/审计日志），深色模式、全局搜索、PWA

---

## 它解决什么

| 问题 | 做法 |
|------|------|
| 上游 API Key 散落在各处客户端，泄露就要全量轮换 | 对外只发自制 Key，上游 Key 只存在服务端；吊销一个自制 Key 不影响其他 |
| 想换模型 / 换厂商，要改所有客户端代码 | 客户端只认 `smart` / `cheap` 这类别名，映射关系在服务端路由表里 |
| 某家上游挂了，整条链路就断 | 按优先级 failover + 健康检查自动摘除，冷却后半开探测 |
| 不知道谁用掉了多少 token、多少钱 | 每个 Key 独立记账，支持 RPM / 总 token / 总金额 / 每日额度 |

---

## 快速开始

```bash
cd work/golang/tsm-hub

# 0) 拉取技能库子模块（resolve-skills，管理台「技能库」页用）
git submodule update --init --recursive

# 1) 编译（含 mock 上游，用于本地联调）
make build mock

# 2) 跑一轮端到端冒烟（自动起 mock 上游 + 网关 + 断言）
make smoke

# 3) 真正使用：复制配置模板，填入你的上游 Key
mkdir -p data && cp config.example.json data/config.json
$EDITOR data/config.json          # 必改：settings.admin_token、各 provider 的 api_key

# 4) 启动
./bin/tsm-hub -data ./data -addr :9070
```

打开 <http://localhost:9070> 进入管理台，用 `admin_token` 登录。

像用 OpenAI 一样调用（只换 Key、换 base_url、换 model 别名）：

```bash
curl http://localhost:9070/v1/chat/completions \
  -H "Authorization: Bearer sk-tr-xxxxxxxx..." \
  -H "Content-Type: application/json" \
  -d '{"model":"smart","messages":[{"role":"user","content":"讲个笑话"}]}'
```

Python SDK：

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:9070/v1", api_key="sk-tr-...")
print(client.chat.completions.create(model="smart", messages=[{"role": "user", "content": "hi"}]))
```

---

## 请求链路

```
客户端 (sk-tr-…)
   │
   ▼
┌──────────────────────── tsm-hub ────────────────────────┐
│ 1. 鉴权    sha256(Key) → 查内存索引 → 校验 enabled/过期      │
│ 2. 限流    RPM 滑动窗口 + token/金额/每日额度                │
│ 3. 路由    别名 → 候选列表：priority 排序 + weight 加权      │
│            过滤掉冷却中的 provider，保留一个半开探测位         │
│ 4. 协议适配 适配器：OpenAI→Anthropic/Azure/Gemini/…          │
│            请求/响应/流式格式转换 + AWS SigV4 签名            │
│ 5. 转发    换 Authorization、改写 model、注入 stream_options │
│ 6. 回传    非流式整包回传；流式按 SSE 帧逐帧转发             │
│            响应头带 X-LLM-Router-Provider: <provider_id>    │
│ 7. 记账    usage → 内存聚合 + data/usage/YYYY-MM-DD.jsonl   │
└────────────────────────────────────────────────────────────┘
   │                    │                    │
   ▼                    ▼                    ▼
OpenAI            DeepSeek / 通义         Anthropic / Gemini
（失败自动换下一个候选；连续失败 → 摘除 + 冷却 → 半开探测）
```

---

## 目录结构

```
tsm-hub/
├── cmd/
│   ├── server/          # 网关主程序
│   └── mockupstream/    # 本地联调用的假上游（不接真实模型）
├── internal/
│   ├── config/          # 启动参数（flag + 环境变量）
│   ├── store/           # 配置模型与原子落盘、内存索引
│   ├── auth/            # 自制 Key 签发/校验、管理会话
│   ├── router/          # 路由选择 + 健康状态机
│   ├── proxy/           # OpenAI 兼容转发、SSE 透传、usage 采集、agent 工具循环、MCP、记忆、沙箱、快路径
│   ├── quota/           # 限流、用量聚合、JSONL 落盘
│   ├── api/             # HTTP 路由（/v1/*、/api/admin/*）
│   ├── audit/           # 审计日志（SQLite 存储、自动脱敏、自动清理）
│   ├── skills/          # Agent Skills 技能库加载与注入
│   └── web/             # embed 前端产物
├── web/                 # Angular 管理台源码
├── scripts/smoke.sh     # 端到端冒烟
├── config.example.json  # 配置模板
└── data/                # 运行期数据（config.json + usage/*.jsonl）
```

---

## 配置

`data/config.json` 首次启动自动生成，结构见 `config.example.json`：

| 字段 | 说明 |
|------|------|
| `settings.listen` | 监听地址，可用 `-addr` 覆盖 |
| `settings.admin_token` | 管理台口令，可用 `-admin-token` / `$LLM_ROUTER_ADMIN_TOKEN` 覆盖 |
| `settings.fail_threshold` / `cooldown_sec` | 连续失败 N 次摘除，冷却 M 秒后半开探测 |
| `settings.pricing` | 每 1K token 单价（美元），用于估算成本；按「实际上游模型名 → 别名 → default」顺序取价 |
| `providers[]` | 上游：`base_url`（可带或不带 `/v1`）、`api_key`、`models`、`weight`、`priority` |
| `routes[]` | 对外模型名 → 候选列表；`strategy`: `failover`（按 priority 降级）/ `weighted`（按权重分流）|
| `keys[]` | 自制 Key，**只存 sha256 哈希** |
| `settings.skills_dir` | Agent Skills 技能库目录（支持相对路径）；空 = 不启用技能 |
| `settings.agent.*` | 网关 agent（通用工具 + 服务端执行循环），见下 |
| `settings.mcps` | 外部 MCP server（stdio 或 Streamable HTTP），其工具以 `mcp_<server>_<tool>` 注册进网关工具池 |
| `settings.audit_retention_days` | 审计日志保留天数（默认 90），过期自动清理 |
| `settings.usage_retention_days` | 用量流水保留天数（默认 0=不自动清理），启动时删除过期文件 |
| `settings.sandbox.*` | Docker 沙箱配置（execute_code 工具），见下 |

**网关 agent（内置通用工具）**：客户端请求**不传 `tools`** 时，网关自动附加内置工具池
并在服务端执行 `tool_calls` 循环，最终返回答案（流式请求同样内部跑完循环后按 SSE 回放）：

| 工具 | 说明 |
|------|------|
| `get_time` | 服务器当前本地时间 |
| `calc` | 数学表达式计算（四则 + 括号 + sqrt/pow/min/max/round/floor/ceil/sin/cos/tan/log/exp）|
| `fetch_url` | 抓取网页/API 文本（仅 http/https，**默认拒绝内网地址**防 SSRF）|
| `echo` | 回显文本 |
| `skill-run` | 加载网关技能库中指定技能的完整说明 |
| `remember` / `recall` | 按 key 隔离的键值记忆（进程内存）|
| `read_file` | 读取白名单根目录内文件（需配置 `agent.read_root` 才提供）|
| `csv_analyze` | 分析白名单根目录内 CSV 结构（需配置 `agent.read_root` 才提供）|
| `execute_code` | Docker 沙箱执行代码（需 `settings.sandbox.enabled=true` 且本机有 docker 才提供）|
| `query_exchange_rate` | 实时汇率查询（open.er-api.com 免费接口）|
| `system_info` | 服务器 OS/架构/CPU/内存/磁盘/运行时长 |

`settings.agent` 配置：

| 字段 | 说明 |
|------|------|
| `disabled` | `true` 关闭 agent（默认启用）|
| `max_rounds` | 工具循环最大轮数（默认 4）|
| `allow_private_url` | `fetch_url` 放行内网/环回地址（默认拒绝）|
| `read_root` | `read_file` 允许的根目录（空 = 不提供该工具）|
| `memory_file` | `remember` 持久化文件（空 = 仅内存）|

请求级开关：`X-Llm-Router-Agent: off` 请求头可对本请求关闭 agent；客户端自带
`tools` 时网关始终尊重客户端（纯透传，不注入、不执行）。

**MCP server（stdio + Streamable HTTP）**：`settings.mcps` 配置后，网关启动/首请求时连接该 server，
完成 MCP 握手并拉取工具列表，工具以 `mcp_<server>_<tool>` 命名加入 agent 工具池，
由网关在服务端执行（懒连接 + 失败自动重建）。常用 MCP（filesystem / sequential-thinking /
memory / serena）已预装，先跑一次 `make install-mcps` 即可开箱即用：

```json
"mcps": {
  "fs":     {"command": "./mcp/node_modules/.bin/mcp-server-filesystem", "args": ["/path/to/workspace"]},
  "think":  {"command": "./mcp/node_modules/.bin/mcp-server-sequential-thinking"},
  "memory": {"command": "./mcp/node_modules/.bin/mcp-server-memory"},
  "serena": {"command": "<uv-tool-dir>/serena-agent/bin/serena", "args": ["start-mcp-server"]},
  "remote": {"transport": "http", "url": "http://127.0.0.1:8787/mcp"}
}
```

`make install-mcps` 会：① `npm install` 到 `mcp/`（filesystem / sequential-thinking / memory /
github / brave-search / playwright）；② `uv tool install` serena（代码语义引擎，Python/uv）；
③ 可选装 playwright 浏览器内核。serena 的 `uv-tool-dir` 可查 `uv tool dir`。传输方式：
`command`（stdio，本地子进程）或 `transport: "http"` + `url`（远程 Streamable HTTP 端点，
响应支持 application/json 与 SSE）。工具的能力边界由你配置的 MCP server 决定（如 filesystem
可读写配置的目录）。

**Docker 沙箱（execute_code）**：`settings.sandbox.enabled=true` 时网关注册
`execute_code` 工具，把代码放入一次性 Docker 容器执行（支持 python /
javascript / shell / java / go / rust / c / cpp）。沙箱安全边界：禁网络
（`--network none`）、只读根文件系统（仅 /tmp 可写）、丢弃全部 capabilities、
禁提权、内存/CPU/进程数/文件描述符限制、超时自动 kill 并清理容器，执行完自动销毁。

```json
"sandbox": {"enabled": true, "timeout_seconds": 30, "memory_mb": 512, "cpus": 1, "max_output_kb": 100}
```

工具参数：`language`（含 py/js/sh/c++ 等别名）+ `code`（完整源码）+ 可选
`timeout`。需要联网的任务请用 `fetch_url` 等工具，沙箱内一律不可联网。

**确定性快路径（fastpath）**：算术、当前时间、日期计算、单位换算、数字统计、
进制转换、字数统计等纯代码可解的问题**直接返回，零模型调用、零上游消耗**，
响应头带 `X-Llm-Router-Fastpath: <method>`（provider 为 `fastpath`）：

| 匹配器 | 触发示例 |
|--------|----------|
| arithmetic | 计算 2+3 / 12×34 / 23 加 45 等于多少（支持中文运算符、幂、括号）|
| statistics | …的平均值 / 总和 / 最大最小 / 排序（数字列表）|
| unit_convert | 100 摄氏度是多少华氏度 / 5 公里等于多少英里 |
| date_math | 明天是几号 / N 天后的日期 / 两个日期相差几天 |
| base_convert | 255 的十六进制 / 十进制转二进制 |
| text_stats | 这段文字有多少字 |
| time | 现在几点 / 当前时间 |

**codegen + 晋升**：内置匹配器未命中时，网关会请 LLM 生成一个 JS 检测器
（`detect(text) -> string|null`），在 goja 沙箱中校验执行（无宿主 API、无 I/O、
无网络，3s 硬超时，禁 eval/Function），命中即持久化为插件
（`<data>/fastpath_plugins/`），后续同类问题**零模型直接命中**。管理台
「快路径」页可查看内置匹配器、测试任意问题、一键生成检测器、晋升插件为正式
检测器（移入 `<data>/fastpath_promoted/`，按 mtime 热重载，无需重启）。

```json
"fastpath": {"enabled": true, "codegen": true, "plugins_dir": ""}
```

**上游 Key 不写进配置文件**：`api_key` 支持 `env:OPENAI_API_KEY` 这种引用形式，
启动前把真实 Key 放到环境变量里即可（管理台回显会保留引用本身，不含密钥）。

路由表未命中时的兜底：直接找所有声明支持该模型的 enabled provider，按权重分流。

**smart 成本智能路由**：`strategy: "smart"` 时按「免费加分 + 单价档位 + 健康度 +
冷却」综合评分选候选，参数均可调（0 = 默认值）：

| 字段 | 默认 | 说明 |
|------|------|------|
| `smart.free_bonus` | 100 | 免费模型的基础加分 |
| `smart.half_open_penalty` | 30 | 半开（冷却后探测期）候选扣分 |
| `smart.throttle_sec` | 60 | 上游 429 后的冷却秒数，期间不选该 provider |
| `smart.unavailable_sec` | 1800 | 上游 404「模型不存在」的标记时长 |
| `smart.price_tiers` | `[{0,60},{0.5,40},{2,20},{10,5}]` | 单价分档加分：prompt 单价 ≤ max 的档得 score（高→低匹配） |

**场景路由（auto 无脑调用）**：客户端 `model` 不传或传 `"auto"` 时，网关按请求
内容自动分类并走对应场景路由（`chat` / `reason` / `code` / `fast`），响应头
`X-Llm-Router-Scene` 返回命中信号。管理台「路由表」可分别配置各场景的候选。
无 `auto` 场景路由时回退到 `smart` 通配路由。

**Key 技能注入**：`keys[].inject_skills` 控制该 Key 请求时向 system 注入技能：
`""`（默认，不注入）/ `"list"`（技能清单，供模型判断何时调用）/ `"all"`（全部
技能全文）/ 其他字符串（单个技能名）。技能源目录由 `settings.skills_dir` 指定。

**可观测性**：每次请求写一条流水（`data/usage/YYYY-MM-DD.jsonl`），除用量外还
记录归因字段：

| 字段 | 说明 |
|------|------|
| `scene` | auto 分流命中的场景（chat/reason/code/fast；非 auto 为空）|
| `fastpath` | 快路径命中标记（`arithmetic` / `unit_convert` / `plugin:xxx` 等）|
| `attempt` | 实际尝试的第几个候选（>1 表示发生过 failover）|

管理 API `GET /api/admin/observability/overview` 返回聚合视图：`today/week/month`
总览（请求数/错误率/平均延迟/成本/failover 次数）、按 provider 归因、按场景分布、
按天趋势。管理台「监控」页直接展示。历史流水（旧字段缺失）自动兼容，无需迁移。

**审计日志**：所有管理操作（创建/修改/删除 Provider、路由、Key、MCP、设置等）自动记录，
存储在 `data/audit.db`（SQLite），包含时间、操作类型、对象类型、对象 ID、详情（自动脱敏
`api_key`/`token`/`password`/`secret`）、操作者、客户端 IP、User-Agent。默认保留 90 天，
可通过 `settings.audit_retention_days` 配置。管理台「审计日志」页可按对象类型、操作类型、
对象 ID 过滤查询和分页。

**外部发现与晋升机制**：网关自动记录调用方请求中声明的 `tools`、`skills`、`mcps`，
在管理台「工具」「技能库」「MCP」页面展示为外部候选。管理员可查看使用统计，择优"录用"
为网关内置能力（工具晋升为内置工具或快路径插件，技能接入技能库，MCP 接入网关工具池）。
这样外部调用方的最佳实践可以沉淀为网关的通用能力，后续调用方无需重复声明。

---

## 对外可编程接口

给**调用方程序**的只读能力发现接口：用任意自制 Key（`Authorization: Bearer sk-tr-…`）即可读取，
让客户端动态发现网关挂了什么模型、工具、MCP、技能，无需本地扫描：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v1/models` | 可用模型与路由别名（OpenAI 兼容）|
| GET | `/v1/tools` | 网关工具池目录：内置 + 条件 + MCP（OpenAI function schema）|
| GET | `/v1/mcps` | 挂载的 MCP server：名称 / 传输 / 连接状态 / 工具（只读，不含 env/command 等敏感配置）|
| GET | `/v1/skills` | 技能库清单；`/v1/skills/<name>` 取单个技能全文 |

```bash
curl http://localhost:9070/v1/tools -H "Authorization: Bearer <你的Key>"
curl http://localhost:9070/v1/mcps  -H "Authorization: Bearer <你的Key>"
curl http://localhost:9070/v1/skills -H "Authorization: Bearer <你的Key>"
```

---

## 管理 API

全部需要 `X-Admin-Token: <admin_token>`（或先 `POST /api/admin/login` 换 `X-Session-Token`）。
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/admin/login` | 用 admin token 换会话 token（同 IP 错 5 次锁 10 分钟）|
| GET | `/api/admin/overview` | 概览：provider/路由/Key 数量、今日与累计用量 |
| GET/POST | `/api/admin/providers` | Provider 列表 / 新增或更新 |
| POST | `/api/admin/providers/probe` | 探测上游模型列表与额度 |
| GET | `/api/admin/providers/balances` | 批量查询各 Provider 额度/余额（带缓存）|
| DELETE | `/api/admin/providers/{id}` | 删除（同时清理路由表里的相关候选）|
| GET/POST | `/api/admin/routes` | 路由表列表 / 新增或更新 |
| DELETE | `/api/admin/routes/{model}` | 删除路由 |
| GET/POST | `/api/admin/keys` | Key 列表 / 签发（**响应里一次性返回明文**）|
| POST | `/api/admin/keys/{id}/toggle` | 启停 |
| PATCH | `/api/admin/keys/{id}` | 更新 Key 名称/模型白名单/配额 |
| GET | `/api/admin/keys/{id}/plaintext` | 重看新建 Key 明文（创建后 2 分钟内有效，只显示一次）|
| DELETE | `/api/admin/keys/{id}` | 删除 |
| GET | `/api/admin/models/catalog` | 模型目录：所有 Provider 的模型汇总，按类型分类、标注免费/付费 |
| POST | `/api/admin/models/refresh` | 刷新模型目录（重新探测所有 Provider）|
| GET | `/api/admin/models/recommendations` | 应用推荐：根据当前模型池推荐适合的应用场景 |
| GET | `/api/admin/usage?days=7&limit=50` | 按天 / 模型 / Key 聚合 + 最近流水 |
| POST | `/api/admin/usage/clear` | 清空全部用量流水 |
| GET | `/api/admin/observability/overview` | 可观测性：总览/按 Provider 归因/按场景分布/按天趋势 |
| GET | `/api/admin/health` | Provider 实时健康（延迟 EWMA、失败数、冷却到期时间）|
| GET/POST | `/api/admin/settings` | 查看 / 修改全局设置（超时、阈值、价格表、审计/用量保留天数）|
| GET | `/api/admin/skills` | 技能库清单 |
| GET | `/api/admin/skills/{name}` | 单个技能全文（Markdown）|
| GET/POST | `/api/admin/mcps` | MCP server 列表 / 新增或更新 |
| DELETE | `/api/admin/mcps/{name}` | 删除 MCP server |
| GET | `/api/admin/tools` | 网关工具池目录（内置 + MCP + 条件工具）|
| POST | `/api/admin/tools/invoke` | 一键测试工具执行 |
| GET | `/api/admin/fastpath` | 快路径状态：内置匹配器 + 插件列表 |
| POST | `/api/admin/fastpath/{name}/promote` | 晋升插件为正式检测器（可选 fastpath 插件或 Tools）|
| DELETE | `/api/admin/fastpath/{name}` | 删除快路径插件 |
| POST | `/api/admin/fastpath/generate` | 手动触发 codegen：对 query 生成检测器并验证 |
| GET | `/api/admin/external-tools` | 外部调用方使用的工具候选（自动发现，可择优录用）|
| POST | `/api/admin/external-tools/{name}/adopt` | 录用外部工具为网关内置工具 |
| DELETE | `/api/admin/external-tools/{name}` | 删除/忽略外部工具候选 |
| GET | `/api/admin/external-skills/candidates` | 外部技能候选列表 |
| GET | `/api/admin/external-mcps/candidates` | 外部 MCP 候选列表（自动发现调用方声明的 MCP）|
| POST | `/api/admin/external-mcps/{server}/adopt` | 接入外部 MCP server |
| DELETE | `/api/admin/external-mcps/{server}` | 忽略/删除外部 MCP 候选 |
| GET | `/api/admin/sandbox/status` | Docker 沙箱状态（是否可用、镜像、配置）|
| GET | `/api/admin/memory` | 会话记忆列表（remember/recall 数据，SQLite 持久化）|
| DELETE | `/api/admin/memory` | 清空全部会话记忆 |
| GET | `/api/admin/audit-logs` | 审计日志查询（支持 object/action/object_id 过滤 + 分页）|

另有探测端点（都不需要鉴权）：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 存活探针：`status`/`uptime_s`/`requests` + `degraded`/`providers[]` 上游健康 |
| GET | `/metrics` | Prometheus 文本格式：HTTP 状态计数、配额拒绝计数、上游健康/请求/错误/延迟 EWMA |

CLI 签发 Key 的等价操作：

```bash
curl -X POST http://localhost:9070/api/admin/keys \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"prod","models":["smart"],"quota":{"rpm":60,"max_cost_usd":20}}'
# → {"ok":true,"id":"…","key":"sk-tr-…","warning":"请立即保存…"}
```

---

## 开发

```bash
make build         # 编译服务端
make mock          # 编译 mock 上游
make test          # 单测 + 端到端（内置 mock，不需要外网）
make vet fmt       # 静态检查与格式化
make smoke         # 端到端冒烟脚本
make web-install   # 安装前端依赖（pnpm 优先，没装则用 npm）
make web-dev       # Angular dev server :4200（/api 代理到 :9070）
make web-build     # 构建前端到 internal/web/dist/browser（供 embed）
make web-check     # 不依赖 node_modules 的 TS 语法自检
```

### 本地联调（前后端一起起）

```bash
make dev           # 先杀掉 :9070/:4200 上的残留进程，再重新编译并后台起后端 + Angular dev server
make dev-logs      # tail -f .dev/*.log
make dev-status    # 看这两个端口现在是谁在监听
make dev-stop      # 只停不停编译，端口和 .dev/*.pid 都会被清掉
```

`make dev` 会：清端口 → `go build` → 起后端（`-data ./data`，日志 `.dev/router.log`）→ 起 `pnpm start`
（日志 `.dev/web.log`）→ 轮询 `/healthz` 与 `:4200` 直到就绪再打印 URL；前端依赖没装会自动先装一次。
端口可用 `make dev ROUTER_PORT=9080 WEB_PORT=4300` 改。

> ⚠️ `dev-stop` 是**按端口** `kill -9` 的：如果 :9070/:4200 上跑的是别的服务，也会被一起杀掉，
> 执行前可用 `make dev-status` 确认。

> macOS 15 上 Go 二进制需要 `CGO_ENABLED=1 -ldflags=-linkmode=external`，
> 否则 dyld 会报 `missing LC_UUID`。Makefile 已统一处理。

---

## 容器部署

```bash
make web-install && make web-build   # 先构建管理台（产物会被打进镜像）
docker compose up -d --build          # 或 docker build -t tsm-hub . && docker run ...
```

镜像里只编译 Go（不需要在镜像内联网装 Node），前端产物从宿主机 `internal/web/dist/browser`
拷进去；跳过前端构建也能跑，只是管理台是占位页。数据通过 `./data` 卷持久化。

## 测试

```bash
make test    # 全部包
make cover   # 覆盖率
```

| 包 | 覆盖内容 |
|----|----------|
| `internal/api` | 端到端：鉴权、failover、SSE、限流、Key 生命周期、/v1/models、配置落盘、provider 响应头、4xx 不熔断、上游计价、登录限流、413、panic 兜底、healthz 健康、env Key、/metrics（18 例）|
| `internal/proxy` | 请求体改写、`stream_options` 注入、SSE usage 解析、URL 拼接（转发主链路由 `internal/api` 的端到端用例覆盖）|
| `internal/router` | 优先级排序、权重偏好、延迟降权、健康摘除与半开放行 |
| `internal/store` | 原子写、更新回滚、删除 provider 时清理路由、Key CRUD |
| `internal/quota` | 聚合与回放、RPM 窗口、四类额度拒绝、TopKeys、最近流水 |
| `internal/auth` | Key 格式、Bearer 解析、常量时间比较、会话过期 |

`make smoke` 另有一套脚本级端到端（真实起进程 + curl 断言）。

## 注意事项

- **自制 Key 明文只出现一次**：签发接口返回后服务端只留哈希，丢了只能重新签发。
- **上游 Key 建议用 `env:VAR` 引用**，避免密钥落盘；直接用明文也支持（部署简单）。
- 启动时会自检：管理口令仍是默认占位符、或还没配任何 provider，都会打 WARN 日志。
- **额度告警**：Key 用量达到任一上限的 80% 时打一条 warn 日志（同类型只提示一次，
  回落线下后重置），适合接日志监控做成本管控。
- **`data/` 目录含上游 API Key 与用量流水**，已写入 `.gitignore`，不要提交到公开仓库。
- **成本是估算值**：按 `settings.pricing` 的价格表计算，流式场景依赖上游返回 usage
  （已自动注入 `stream_options.include_usage`）；上游不返回时 token 记为 0。
- 上游 5xx 会自动换下一个候选；**已经向客户端写出过字节的流式响应不会重试**
  （否则会造成内容重复），只会记录错误。
- **4xx 不计入 provider 失败**：只有网络错误 / 5xx / 流式首帧前断流才触发熔断，
  避免「上下文超长 / 内容审核拒绝」这类客户端问题把健康上游摘除。
- 管理台编辑 provider 时回显的 `api_key` 是脱敏值，后端检测到省略号会保留原 Key，
  不会用脱敏串覆盖真实 Key（前端后端双重保护）。
- 每个响应都带 `X-Request-ID`，日志同 ID 可关联；panic 会自动兜底回 500 并带上该 ID。
- 请求体超过 `settings.max_body_bytes` 直接返回 413（不截断后报误导性错误）。
- `/healthz` 额外带 `degraded` 与 `providers[]` 字段，可区分「网关挂」与「某上游被摘除」。
