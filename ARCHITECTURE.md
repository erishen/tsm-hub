# llm-router 架构说明

## 1. 设计约束

| 约束 | 选择 | 代价 |
|------|------|------|
| 依赖 | 只用 Go 标准库 | 自己写路由分发、SSE 转发、限流窗口；不引入 chi / gin / redis |
| 存储 | JSON 配置 + JSONL 流水 | 单机部署，多实例需要共享盘或后续迁移到 SQLite |
| 前端 | Angular 19 standalone，产物 embed 进二进制 | 一个二进制交付；构建需要 Node |
| 协议 | OpenAI 兼容子集 | 只透传 `/v1/*`，不解析各家私有字段 |

选择零依赖的理由：网关是基础设施，依赖越少升级成本越低；且这个规模的问题（路由 + 限流 + 记账）用标准库完全够。

## 2. 模块划分

```
cmd/server        启动、优雅退出、flag/环境变量
internal/config   进程参数（Listen / DataDir / AdminToken / LogLevel）
internal/store    配置模型 + 原子落盘 + 内存索引（providers / routes / keys）
internal/auth     自制 Key 签发与校验、管理会话
internal/router   路由选择（优先级 / 权重 / 延迟）+ 健康状态机
internal/proxy    OpenAI 兼容转发、SSE 透传、usage 采集、failover
internal/quota    限流（RPM 滑动窗口）+ 用量聚合 + JSONL 落盘
internal/api      HTTP 层：/v1/* 代理、/api/admin/* 管理接口、静态前端
internal/web      embed Angular 产物 + SPA 回退
```

依赖方向单向：`api → proxy → router → store`，`quota` 只被 `proxy` / `api` 使用，没有循环。

## 3. 请求链路

```
HTTP 请求（最外层：panic 兜底中间件，生成 X-Request-ID 并写进响应头与日志）
  │
  ├─ /healthz           → 健康检查（不鉴权；带 degraded + providers[] 上游健康）
  ├─ /metrics           → Prometheus 文本指标（不鉴权）
  ├─ /v1/*              → authenticate → limiter.Check（额度告警） → proxy.Handle
  ├─ /api/admin/login   → 口令换 session（同 IP 失败 5 次锁 10 分钟）
  ├─ /api/admin/*       → admin 中间件 → 各 handler
  └─ /                  → 静态前端（SPA 回退到 index.html）
```

`proxy.Handle` 内部：

1. 解析 body 取 `model` / `stream`；
2. `router.Pick(model)` 得到有序候选；
3. 逐个候选 `attempt`：
   - 重写 body（`model` 改成上游模型名；流式注入 `stream_options.include_usage`）；
   - 换 `Authorization`，合并 provider 自定义 header；
   - 上游 5xx / 网络错误 / 流式首帧前断流 → 记 provider 失败、换下一个候选；
     **4xx 客户端错误不记失败**（避免坏请求把健康上游摘除）；
   - 非流式：整包读完再回传（便于解析 usage）；
   - 流式：逐帧转发，从含 `usage` 的最后一帧取 token 数；
   - 成功响应带 `X-LLM-Router-Provider: <provider_id>` 头（流式、非流式都有）；
4. `quota.Record` 记账（内存聚合 + JSONL 落盘；按「实际上游模型名 → 别名 → default」顺序取价）。

**重试边界**：只有"还没有向客户端写出任何字节"才换候选。流式一旦写出首帧就不再重试，否则会产生重复内容。

## 4. 路由策略

```
route.model 命中路由表？
  ├─ 是 → 用路由表 targets（strategy = failover | weighted）
  └─ 否 → 兜底：所有 enabled 且声明支持该模型的 provider，按 weight 分流
```

候选排序：

1. 过滤掉 `enabled=false` 与冷却中的 provider；
2. 若全部冷却中，放行一个"半开"候选做探测（避免整体不可用）；
3. `failover`：按 `priority` 升序分组，组内按权重随机；
4. `weighted`：所有候选同池，按 `weight × 延迟因子` 加权随机排序。

延迟因子：`w' = w × 1000 / (1000 + latency_ewma_ms)`，即延迟 1s 时权重减半，让快节点自然多拿流量。半开候选额外乘 0.1。

## 5. 健康状态机

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
- 只做被动探测（靠真实请求），不主动发心跳，避免产生额外费用。

## 6. 配额与记账

- **RPM**：内存滑动窗口（保留最近 1 分钟的时间戳），超限返回 429；
- **额度**：总额度（token / USD）与每日额度，超限返回 402；
- **记账**：每次请求写一条 JSONL 到 `data/usage/YYYY-MM-DD.jsonl`，
  进程启动时回放最近 90 天重建内存聚合（损坏行跳过，不阻塞启动）；
- **成本**：`prompt/1000 × input_per_1k + completion/1000 × output_per_1k`，
  价格表在 `settings.pricing`，按「实际上游模型名 → 请求别名 → default」顺序取价。
  这是估算，不是账单。

## 7. Key 安全

- 明文格式 `sk-tr-<48 hex>`，创建接口返回一次；
- 库中只存 `sha256(明文)`，用 `constant-time` 比较；
- 管理 API 返回的 provider `api_key` 一律脱敏（`abcd…wxyz`）；
  配置为 `env:VAR` 引用的回显引用本身（不含密钥），真实 Key 由环境变量注入；
- 编辑 provider 时若 api_key 字段带脱敏省略号，后端不会覆盖原值。
- **额度告警**：`limiter.CheckWarn` 在请求通过限流后检查用量是否 ≥ 任一上限的 80%，
  跨线只提示一次（回落线下后重置），写 warn 日志供成本监控。

## 8. 持久化格式

`data/config.json`（原子写：临时文件 → fsync → chmod 600 → rename）：

```jsonc
{
  "version": 1,
  "settings": { /* listen / admin_token / 阈值 / 价格表 */ },
  "providers": [ /* id, base_url, api_key, models, weight, priority ... */ ],
  "routes":    [ /* model, strategy, targets[] */ ],
  "keys":      [ /* id, prefix, hash, quota ... */ ]
}
```

`data/usage/2026-09-07.jsonl`（每行一条）：

```json
{"ts":"...","key_id":"...","model":"smart","provider_id":"backup",
 "upstream_model":"gpt-4o","prompt_tokens":9,"completion_tokens":5,
 "total_tokens":14,"cost_usd":0.0001,"latency_ms":312,"stream":false,"status":200}
```

## 9. 已知边界与后续

见 `TODO.md`。主要待办：多实例下的共享状态、按 Key 的成本告警、请求体审计采样、Prometheus 指标。
