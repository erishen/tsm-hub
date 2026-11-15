# tsm-hub

> 自制 Token Key + 智能路由的 LLM 网关：对外只暴露自己签发的 `sk-tr-…` Key，
> 内部把 OpenAI 兼容请求智能路由到配置好的多家上游模型。

- **后端**：Go 1.25，单二进制、标准库优先——直接依赖只有两个：`modernc.org/sqlite`（纯 Go 驱动）与 `github.com/dop251/goja`（Fastpath 检测器的 JS 沙箱）
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

> **Docker Compose 部署的端口不一样**：`docker-compose.yml` 把容器内 `9070` 映射到宿主
> **<http://127.0.0.1:19070>**（只绑回环）。用 `make docker-up` 启动时，下面所有示例的
> `localhost:9070` 都要换成 `127.0.0.1:19070`；`9070` 只适用于上面 `make run`／直接跑
> 二进制的本地直连方式。本机若设了 `http_proxy`，`curl` 还要加 `--noproxy '*'`，
> 否则请求会被代理接走返回 502（这也是 `make docker-status` 曾经一直显示「服务未响应」的原因）。

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
| `tool_search` | 按关键词检索完整工具目录（动态注入模式的漏选兜底，见下）|

`settings.agent` 配置：

| 字段 | 说明 |
|------|------|
| `disabled` | `true` 关闭 agent（默认启用）|
| `max_rounds` | 工具循环最大轮数（默认 4）|
| `allow_private_url` | `fetch_url` 放行内网/环回地址（默认拒绝）|
| `read_root` | `read_file` 允许的根目录（空 = 不提供该工具）|
| `memory_file` | `remember` 持久化文件（空 = 仅内存）|
| `dynamic_tools` | 开启动态工具注入：只下发「常驻核心 + 按请求检索的 top-K 相关工具 + `tool_search`」，大幅省 token（默认关闭）|
| `dynamic_top_k` | 动态注入的相关工具上限（默认 8，最大 20）|
| `core_tools` | 动态模式常驻工具名；空 = `calc/fetch_url/get_time/remember/recall/skill-run`|

动态注入说明：`dynamic_tools` 开启后，agent 按用户请求文本对工具目录做关键词
打分（英文 token + 中文 bigram 匹配工具名/描述），只把核心集与最相关的 top-K
工具 schema 发给上游；模型发现需要的工具不在列表时，先调 `tool_search`
检索完整目录，再按返回的名称与参数直接调用。工具池较小（≤ 核心 + topK + 4）
时自动退回全量注入。与 per-key `tools_allow`/`mcps_allow` 白名单可叠加
（白名单先过滤，动态检索在剩余池子里选）。

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

> 容错：个别客户端把能力段拼到 chat 路径之后（`GET /v1/chat/completions/tools`
> 等）。GET 在这些路径上没有 chat 语义，网关会归一到上面的正式端点直接返回目录，
> 不再透传上游（上游对 GET 只会回 404/405）。同理，非 POST 方法打到纯 chat
> 路径会本地直接返回 405（`Allow: POST`）。

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
| GET | `/metrics` | Prometheus 文本格式：HTTP/配额计数、**HTTP 总耗时（按 flow 拆分）与流式首字节耗时直方图**、上游健康/延迟/**失败类别**、累计 token/成本用量、健康状态机、MCP/A2A/运行时快照 |

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

## 监控（Prometheus & Grafana）

网关自身就是 exporter：`GET /metrics` 返回标准 Prometheus 文本格式
（`text/plain; version=0.0.4`，带 `# HELP` / `# TYPE`），不鉴权、无需 sidecar。
5 个 C 版 MCP 桥也各自暴露 `/metrics`（`mcp_gateway_*`）。
`docker-compose.yml` 已带一套 **Prometheus + Alertmanager + 钉钉桥接器 + Grafana
+ blackbox-exporter**，与 tsm-hub 同处 compose 默认网络，全部用容器 DNS 互联。

```bash
# 1) .env 里设强口令与钉钉机器人（见 .env.example）：
#    GRAFANA_ADMIN_PASSWORD=<强口令>
#    DINGTALK_WEBHOOK_URL / DINGTALK_WEBHOOK_SECRET（不配则告警只记日志、不推送）
# 2) 只启动监控服务，不会动正在运行的 tsm-hub 容器
docker compose up -d prometheus alertmanager dingtalk-bridge grafana blackbox-exporter
```

| | 地址 | 说明 |
|---|------|------|
| Prometheus | <http://127.0.0.1:19092> | `/-/healthy`、`/targets`、`/alerts`；数据保留 30 天 |
| Alertmanager | <http://127.0.0.1:19093> | 告警分组、静默、抑制；UI 无鉴权，只绑回环 |
| Grafana | <http://127.0.0.1:19091> | 数据源与「tsm-hub 网关监控」面板自动注册 |
| 钉钉桥接器 | 无宿主端口 | 仅 compose 网络内 `dingtalk-bridge:8060`，外部不可达 |
| blackbox-exporter | 无宿主端口 | 仅 compose 网络内 `blackbox-exporter:9115`；`/probe` 不鉴权，绝不发布到宿主 |
| 5 个 MCP 桥 | 无宿主端口 | `mcp-{fs,memory,think,fetch,git}:8000`，仅容器 DNS 可达 |

**Grafana 登录**：打开 <http://127.0.0.1:19091/>，用户名 `admin`，密码取 `.env` 里的
`GRAFANA_ADMIN_PASSWORD`；未设置时回落默认口令 `admin`。
⚠️ `GF_SECURITY_ADMIN_PASSWORD` **只在 `grafana-data` 卷首次初始化时生效**，卷已存在后
改 `.env` 再重启不会改密码。要改请在 UI 里改，或执行：

```bash
docker compose exec grafana grafana cli admin reset-admin-password '<新口令>'
```

面板已设为**默认首页**（`GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH`），登录后直接落到
「tsm-hub 网关监控」，不用再去 Dashboards 里找。共 **39 个面板**（v9）：

| 区间 | 面板 |
|---|---|
| 1–13 | 抓取状态 `up`、进程时长、请求速率、整体错误率、健康 Provider **比例**、firing 告警数，各 provider 的错误率/QPS/延迟 EWMA/健康状态、HTTP 状态码速率、配额拒绝、触发中的告警 |
| 14–20 | token 消耗、累计成本（单独看 24h 窗口）、冷却剩余、半开态与连续失败、场景请求速率与工具来源、MCP/A2A 快照、Go 运行时与配置实体 |
| 21–25 | Alertmanager 在线、钉钉投递失败数、AM 活跃告警、生效静默、投递成功/失败与规则 firing 趋势 |
| **26–29** | **HTTP 耗时直方图 p50/p95/p99、请求速率 by route/method、黑盒探测成功率、黑盒探测耗时** |
| **30–35** | **MCP 桥在线数、探测成功数、活跃会话、5xx 计数，以及桥的会话/槽位/在飞 RPC 与请求/错误速率趋势** |
| **36–39** | **上游失败类别（quota / auth / rate_limit / …）、流式与非流式请求速率、首字节延迟 p50/p95/p99、按 flow 拆分的 HTTP 总耗时 p95** |

顶部有 `Provider`、`Scene`、`MCP 桥`、`Route` 四个变量（多选，默认 All），
以及 Prometheus / Alertmanager UI 的快捷链接。配额拒绝用 `or vector(0)` 兜底——
`/metrics` 在无拒绝记录时不输出该 family，否则面板会长期空白。
改面板请改仓库里的 JSON：文件是只读挂载，provider 每 30s 自动重载。

抓取目标全是**容器 DNS**（`tsm-hub:9070`、`mcp-*:8000`、`blackbox-exporter:9115`），
不要写 `127.0.0.1:19070` —— 在 Prometheus 容器里那是它自己的 loopback。
三个 UI 端口都只绑 `127.0.0.1`；桥、探针、钉钉桥接器完全不发布宿主端口。

| 文件 | 作用 |
|------|------|
| `deploy/prometheus.yml` | 抓取配置（15s）+ `rule_files` + `alerting.alertmanagers`；7 个 job：`tsm-hub` / `prometheus` / `alertmanager` / `mcp-bridges` / `blackbox-exporter` / `blackbox-healthz` / `blackbox-mcp`。`tsm-hub` job 有三条护栏（`sample_limit: 5000`、`label_limit: 32`、`label_value_length_limit: 128`）与指标名白名单；新 job 各有自己的 keep 规则 |
| `deploy/prometheus.rules.yml` | **12 条告警**：7 条业务告警（target 掉线、上游错误率 >30% 且 5m 内至少 5 次请求、provider 被摘除、配额拒绝、EWMA 延迟 >30s 且近 15m 有成功样本、**401/403 认证失败**、**流式首字节 p95 >10s**）+ 2 条通知链路自监控 + 3 条黑盒探测（`/healthz` 失败、MCP 桥 `/health` 失败、探针自身掉线）。402 额度耗尽 / 429 限流是免费池常态，刻意只做面板不告警 |
| `deploy/alertmanager.yml` | 分组/去重/抑制/恢复通知；webhook receiver 指向 `dingtalk-bridge:8060` |
| `deploy/blackbox.yml` | 两个 probe module：`http_healthz`（校验 `/healthz` 的 JSON 含 `status=ok`）、`http_2xx`（MCP 桥 `/health`） |
| `deploy/dingtalk_bridge.py` | 仅标准库：Alertmanager webhook → 钉钉 markdown + 加签；见下节 |
| `deploy/mcp_gateway.c` | C 版桥的 `GET /metrics`，输出 `mcp_gateway_*`；两个桥 Dockerfile 共用这份源码 |
| `deploy/grafana/provisioning/` | Prometheus 数据源（`uid: prometheus`）+ 面板自动加载 |
| `deploy/grafana/dashboards/tsm-hub.json` | 39 个面板（v9，面板 36 补了分类计数口径说明）、4 个变量；成本面板单独看 24h |

**指标分层**（下表指标名均省略统一前缀 `tsm_hub_`；全部复用已有内存聚合，不新增状态、不改业务路径）：

| 组 | 指标 | 数据源 |
|---|---|---|
| 核心 7 个 | `tsm_hub_uptime_seconds`、`http_requests_total{status}`、`quota_denials_total{code}`、`upstream_{healthy,requests_total,errors_total,latency_ms}{provider}` | `Server` 计数 + `router.Tracker.Snapshot()` |
| HTTP 耗时直方图 | `http_request_duration_seconds_{bucket,sum,count}{route,method,flow,le}`、`http_first_byte_seconds_{bucket,sum,count}{route,method,le}` | `withLogging` 里已算好的 `elapsed` + `statusRecorder` 首次 `Flush()` 时刻，无新增业务埋点；13 个桶（10ms → 120s）+ `+Inf`。`flow=stream\|buffered` 把 SSE 长连接与一次性响应分开，首字节直方图只收流式样本 |
| 上游失败类别 | `upstream_error_classes_total{provider,class}` | `router.Tracker` 在 `ReportFailure` / `ReportThrottle` 里与 `errors_total` **同源累加**；`class` 是 8 个固定枚举（quota / auth / rate_limit / model_not_found / bad_request / server_error / network / other），错误正文绝不入标签 |
| 健康状态机 | `upstream_{half_open,consecutive_failures,cooldown_remaining_seconds,last_success_timestamp_seconds}{provider}` | 同上 `ProviderHealth`（管理台 Providers 页同款字段） |
| 用量 / 成本 | `usage_{requests_total,tokens_total,cost_usd_total,errors_total,failovers_total,avg_latency_ms}{provider}`、`usage_scene_{requests_total,tokens_total}{scene}`、`failover_candidates_total{provider}`、`tool_calls_total{source}` | `quota.Recorder`，启动时从 `data/usage/*.jsonl` 回放，重启不清零 |
| 子系统 / 运行时 | `mcp_servers{state}`、`mcp_tools`、`a2a_{tasks_active,capabilities,peer_mode}`、`config_entities{entity}`、`runtime_{goroutines,heap_alloc_bytes,cpu_cores}` | `Proxy.MCPStatuses()`、`A2A.AdminOverview()`、`store.List*()`、`runtime.MemStats` |

另外两套指标**不带 `tsm_hub_` 前缀**，别在面板里找错：

| 来源 | 指标 | 说明 |
|---|---|---|
| 5 个 C 版 MCP 桥 `GET /metrics` | `mcp_gateway_{up,uptime_seconds,sessions,sessions_max,sse_active,inflight}`、`mcp_gateway_http_{requests_total{route},responses_total{status}}`、`mcp_gateway_sessions_{created,closed}_total` | `mcp_server` 标签是 compose 里写死的 5 个枚举值；`/metrics` 不占会话槽位，慢 RPC 不会挡住抓取 |
| blackbox-exporter | `probe_success`、`probe_duration_seconds`、`probe_http_status_code` 等 | `instance` 用配置里的短名（`tsm-hub-healthz` / `mcp-fs`…），完整 URL 只作 `__param_target` 内部传参，不进标签 |

注意事项：

- counter 一律用 `rate()` / `increase()` 查询，别用裸值。HTTP/配额计数、**两个直方图、
  `upstream_error_classes_total`** 都是纯内存累计、重启归零且不进 SQLite 健康快照；
  上游的 `requests_total` / `errors_total` 可能从 SQLite 快照恢复；`usage_*` 由
  `data/usage/*.jsonl` 回放，进程重启不清零，所以成本面板直接画累计值。
  失败类别与 `errors_total` 同源（同一处 `++`），每次计入健康的失败恰好落到一个类别；
  模型 404 / 客户端断开按原设计不计入健康，因此也不出现在分类里。
  ⚠️ **两个数字的绝对值不要直接比较**：`errors_total` 会把 SQLite 快照里的历史累计
  值恢复回来（例如重启后仍是 1720），而分类计数重启即归零，所以「分类之和 = 总错误数」
  只在进程从未重启时才成立；排查故障请一律看 `increase()` 增量，别看裸累计值。
- **三个「延迟」别混淆**：`upstream_latency_ms` 是上游单次尝试的 **EWMA gauge**（无 p95/p99）；
  `http_request_duration_seconds` 是网关整个 HTTP 请求的**总耗时直方图**；
  `http_first_byte_seconds` 是流式请求到**首次 Flush（首字节）**的直方图，
  这才是用户等首 token 的体感。三者一律用
  `histogram_quantile(0.95, sum by (le) (rate(..._bucket[10m])))` 算分位数。
  直方图只覆盖 `withLogging` 包裹的路由（`/v1/*`、`/api/admin/*`、`/a2a`）；
  `/healthz`、`/metrics`、`/peer/*` 与前端静态资源不包裹，避免 Prometheus
  每次抓 `/metrics` 都自指放大计数。总耗时带 `flow` 标签：SSE 会把整个流生成过程
  算进去（可达数分钟），用 `flow="buffered"` 过滤才能得到「普通请求」的真实 p95；
  面板 39 把两条线画在一起。首字节直方图只有流式样本，因为非流式响应从不 Flush。
  冷启动无业务请求时 family 只有 HELP/TYPE、没有样本，面板空白属正常；若窗口内
  counter 完全没有变化，`rate()` 全为 0，`histogram_quantile()` 会返回 **NaN**
  （Prometheus 的正常行为，实测持续流量后即恢复），不是埋点坏了。
- ⚠️ EWMA **只在一次上游成功时更新**。`internal/router/health.go` 已加 **15 分钟惰性 TTL**：
  超过 15m 没有成功请求时，`Latency()` / `Health()` / `Snapshot()` 一律返回 0，
  从 SQLite 健康快照恢复的陈旧值不会再被当成实时延迟、也不会再让 `Router.Pick`
  的延迟降权长期误判。`TSMHubUpstreamLatencyHigh` 告警还额外要求「近 15m 有成功
  样本」作为双保险。
- 黑盒探测里 `degraded=true` **仍算成功**：上游被摘除是 provider 级问题，由
  `TSMHubProviderUnhealthy` 负责；黑盒只回答「整台网关是否应答」。
  `TSMHubBlackboxExporterDown` 单独盯探针自身，否则 `probe_success` 全 absent 时
  分不清是探针挂了还是目标挂了。
- 有累计 token / 成本 / 场景 / 工具来源指标，但**刻意没有按 Key、按 model、按 tool 名**的
  维度：这些是高基数字段。按 Key 的明细仍在 `data/usage/*.jsonl` 与 `/api/admin/usage`。
- `/metrics` **不鉴权**，会暴露 provider 名、请求/错误数、延迟与成本量级。保持只绑回环，
  不要把 9070/19070 改绑 `0.0.0.0`。新增标签只允许「与现有 provider 列表同源的 ID」
  或「代码内有限枚举」（`scene` / `source` / `state` / `mode` / `entity` / `kind` /
  `status` / `code` / `route` / `method` / `flow` / `class` / `le`）；密钥、`key_id`、
  原始 URL path、model/tool 名、session/task ID、上游错误文本、client_ip 一律禁止入标签。
  `class` 只由 `classifyUpstreamError()` 解析固定前缀 `upstream <3 位状态码>` 得到，
  错误正文（可能含 URL / Key 片段）从不参与导出；`flow` 只取 `stream` / `buffered`。
  `route` 经 `metricRoute()` 折叠（管理台所有子路径 → `admin`，`/v1/*` → `inference`），
  `method` 经 `metricMethod()` 转小写且只放行 7 个标准方法，其余 → `other`。
  `internal/api/metrics_test.go` 会把标签名与标签值逐行过白名单（值必须是
  `[a-z0-9_-]+`，`le` 单独校验为数值或 `+Inf`），`deploy/prometheus.yml` 的
  `sample_limit` 与指标名白名单是第二道工程护栏；两者都在 `go test ./...` /
  `promtool check config` 里可验。C 桥的 `/metrics` 同样只输出固定枚举标签，
  不导出 `g_cmd`（含宿主绝对路径）、sid、URL 或请求体。

改完告警规则可热加载，无需重启：
`curl --noproxy '*' -X POST http://127.0.0.1:19092/-/reload`；
改完 `deploy/alertmanager.yml` 同理：
`curl --noproxy '*' -X POST http://127.0.0.1:19093/-/reload`。

### 钉钉告警通知

```
Prometheus (rules) → alertmanager:9093 → dingtalk-bridge:8060 → 钉钉群机器人
                     分组/去重/抑制/恢复      协议转换 + 加签
```

Alertmanager 的 webhook 协议与钉钉机器人不兼容，且钉钉「加签」要求每个请求带
`timestamp` + `HMAC-SHA256(secret, ts+"\n"+secret)` 动态签名，
所以加了 `deploy/dingtalk_bridge.py`（仅 Python 标准库、不发布宿主端口）。
12 条告警里 `TSMHubAlertmanagerDown` / `TSMHubDingtalkNotificationsFailing` 是
**自指告警**：通知链路本身挂掉时它们也送不出去，靠 Grafana 的通知链路面板（21–25）发现。
黑盒探测的 3 条规则在探针自身挂掉时也送不出去，由
`up{job="blackbox-exporter"}` 与面板 28–31 兜底。

**配置**：密钥只放 `.env`（已 gitignore，权限保持 `0600`）。
`DINGTALK_WEBHOOK_URL` / `DINGTALK_WEBHOOK_SECRET` 与
`work/sale/daoye-engine` 的 `DAOYE_WEBHOOK_*` 是同一个群机器人，直接复制那两个值即可；
加签算法与那边 `daoye_engine/notify.py` 同款。
`DINGTALK_KEYWORD=告警` 适配「自定义关键词」安全设置（标题与正文都会带上关键词），
若群里设的是别的词要改；`DINGTALK_DRY_RUN=1` 只记日志不推送。

> ⚠️ `docker compose config` 会回显解析后的环境变量（含 webhook token）。
> 校验配置一律用 `docker compose config --quiet`，不要把输出贴到日志/issue 里。

**先 dry-run**：把 `.env` 的 `DINGTALK_DRY_RUN` 改 `1`，
`docker compose up -d dingtalk-bridge`（必须 recreate 才注入新值），
再发测试告警；桥只写日志、不打扰群。确认后改回 `0` 再 up 一次。

**发一条测试告警**（真实推送，`group_wait: 30s`，约 30 秒内到群）：

```bash
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
end=$(date -u -v+2M +%Y-%m-%dT%H:%M:%SZ)   # macOS；Linux 用 date -u -d '+2 minutes' ...
curl --noproxy '*' -X POST http://127.0.0.1:19093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d "[{\"labels\":{\"alertname\":\"TSMHubNotificationTest\",\"severity\":\"info\"},\"annotations\":{\"summary\":\"钉钉通知链路联调测试\",\"description\":\"收到后可忽略，告警 2 分钟后结束\"},\"startsAt\":\"$now\",\"endsAt\":\"$end\"}]"
```

群里会先收到「触发」消息；`send_resolved: true` 已启用，告警结束（`endsAt` 到期或
主动 resolve）后会再收到「已恢复」消息，但**不是立刻**——Alertmanager 按分组策略
延迟发出，端到端实测约 1~2 分钟。这是刻意的，避免上游抖动时「恢复/又挂」来回刷群。
单次不发群也可在容器内给桥发 `X-Dingtalk-Dry-Run: 1` 头，验证消息排版。

**日常操作**：Alertmanager UI（<http://127.0.0.1:19093/#/silences>）可建静默；
已配 `TSMHubDown` 抑制所有 `warning|info`（网关整体掉线时不再刷上游连带告警）；
`group_by: [alertname, provider]` 合并同类，`repeat_interval: 4h` 长期故障才重发。

| 现象 | 排查 |
|------|------|
| 群完全没消息 | ① `curl --noproxy '*' http://127.0.0.1:19093/api/v2/alerts` 看 AM 是否收到；② `docker compose logs dingtalk-bridge` 看是否 `dry-run` / `configured=false`；③ Grafana 底部「通知投递」面板 |
| `errcode=310000` | 关键词或加签不匹配：核对 `DINGTALK_KEYWORD` 与 secret 是否与钉钉机器人当前设置一致 |
| `errcode=300005` | token 无效 / 机器人被删，重新复制 URL |
| 恢复消息迟迟不来 | 通常要等 1~2 分钟（分组策略）；若一直不来，查 `send_resolved` 是否被关、AM 是否在 resolve 前重启 |
| `up{job="alertmanager"}=0` | 通知已中断；此时告警只在 Prometheus 侧 firing，恢复后会收到 resolved |
| `probe_success` 全为 0 或空 | 先看 `up{job="blackbox-exporter"}`：为 0 是探针挂了，为 1 才是目标真的不通 |
| MCP 桥面板（30–35）空白 | 桥镜像仍是旧 C 二进制（没有 `GET /metrics`）。`docker compose build mcp-fs mcp-git` 后 `docker compose up -d` 重建 5 桥；`make alert-check` 不覆盖这一步 |
| HTTP p95 面板空白 | 新进程还没收到任何经过 `withLogging` 的业务请求，直方图 family 只有 HELP/TYPE；发一次 `/v1/models` 即出现 |
| 首字节 / `flow=stream` 面板空白 | 只有调用过 `Flush()` 的流式响应才会产生首字节样本与 `flow="stream"` 序列；发一次带 `"stream":true` 的 `/v1/chat/completions`，或确认客户端没有降级成非流式 |
| 失败类别全是 0 | `upstream_error_classes_total` 只统计**计入健康状态机**的失败；模型 404（`ModelFault`）与客户端断开（`ClientAbort`）按设计不计入，去看配额拒绝 / 用量错误指标 |

**隐私边界**：URL / secret 只经 `.env` 插值注入桥接器容器，不写任何 yml、面板或提交；
桥接器日志只打 `scheme://host/path?...`，异常文本经 `redact()` 去掉
`access_token` / `sign` / secret 字面量；消息内容只取
`alertname / severity / provider / summary / description / 开始时间`，
告警 labels 里的 `api_key`、`session_id`、`generatorURL` 等一律不输出
（`make alert-check` 里的 `deploy/test_dingtalk_bridge.py` 有门禁断言）。
Alertmanager UI 同样无鉴权，19093 只绑回环。

## 配置隐私门禁

`data/config.json` 及所有 `config.json.bak-*` 权限必须是 `0600`，且每个
`providers[].api_key` 必须写成 `env:TSM_HUB_KEY_*` 引用，不允许明文上游 key。
历史备份保留作回滚点，但已就地脱敏。

```bash
make privacy-check                                    # 只读；权限/明文 key 不合格则 exit 1
python3 -S scripts/config-privacy-check.py --redact   # 只脱敏 *.bak-*，绝不动现用 config.json
```

`--redact` 把明文替换成 `env:TSM_HUB_KEY_<ID>`，备份恢复后仍能用当前 `.env` 取密钥。
若要迁移现用配置里的明文 key：值写进 `.env` → `api_key` 改成 `env:TSM_HUB_KEY_<ID>`
→ `docker-compose.yml` 的 `tsm-hub` 服务加同名变量 → `docker compose up -d tsm-hub`
（recreate 注入新变量即可，不需要重建镜像）。

---

## 测试

```bash
make test         # 全部 Go 包（先跑 fmt-check 门禁）
make fmt-check    # gofmt -l 非空即失败
make cover        # 覆盖率（自动回退到 ~/sdk 下完整 Go 1.25 SDK；可用 GO=... 指定）
make privacy-check # config.json* 权限与明文 key 门禁
make alert-check  # 钉钉桥单测 + MCP 桥 /metrics 冒烟 + 面板结构 + AM/Prometheus 配置（配置校验需监控栈已 up）
```

| 包 | 覆盖内容 |
|----|----------|
| `internal/api` | 端到端：鉴权、failover、SSE、限流、Key 生命周期、/v1/models、配置落盘、provider 响应头、4xx 不熔断、上游计价、登录限流、413、panic 兜底、healthz 健康、env Key、/metrics 指标门禁与标签白名单、HTTP 直方图格式/路由折叠/flow 分离/首字节 family/nil 安全/端点隐私、`withLogging` 流式 Flush 埋点、失败类别 0 值矩阵与错误正文不泄漏 |
| `internal/proxy` | 请求体改写、`stream_options` 注入、SSE usage 解析、URL 拼接（转发主链路由 `internal/api` 的端到端用例覆盖）|
| `internal/router` | 优先级排序、权重偏好、延迟降权、健康摘除与半开放行、**失败类别枚举/前缀解析/与 errors 同源计数/深拷贝** |
| `internal/store` | 原子写、更新回滚、删除 provider 时清理路由、Key CRUD |
| `internal/quota` | 聚合与回放、RPM 窗口、四类额度拒绝、TopKeys、最近流水 |
| `internal/auth` | Key 格式、Bearer 解析、常量时间比较、会话过期 |
| `deploy` | 钉钉桥接器 16 例：加签向量、告警/恢复消息排版、errcode 判定、URL 与 secret 脱敏；`test_mcp_gateway_metrics.sh` 本机编译 C 桥并实测 `/metrics` 的 10 个 family、计数与隐私边界（`make alert-check`）|

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

## 相关文章
- [tsm-hub：把 LLM、Tools、MCP、Skills 收进一个统一网关](https://erishen.cn/tsm_hub/)

## 许可证

[MIT](./LICENSE)
