# tsm-hub TODO

> 最后更新：2026-09-10

## 2026-09-08 修复记录 ✅

- [x] **target priority 失效**：`Pick()` 候选构造时未带 `targets[].priority`，实际按 provider 优先级排序；已修复并补回归用例，删除死代码 `routeTargetsFor`
- [x] **4xx 误熔断**：上游 4xx 不再计入 provider 失败（只有网络错误 / 5xx / 流式首帧前断流才摘除）
- [x] **`X-LLM-Router-Provider` 响应头恒空**：流式、非流式都正确设置
- [x] **别名路由计价失效**：按「实际上游模型名 → 别名 → default」取价
- [x] **脱敏 api_key 回写覆盖**：后端 `UpsertProvider` 检测省略号保留原 Key（前端后端双重保护）
- [x] **无效 JSON 无响应**：`rewriteBody` 失败回 400，不再隐式 200
- [x] **admin 登录暴力尝试**：同 IP 失败 5 次锁 10 分钟（429）
- [x] **请求体超限**：超过 `max_body_bytes` 回 413，不截断后报误导性错误
- [x] **panic 兜底 + 请求 ID**：所有响应带 `X-Request-ID`，panic 回 500 并带 ID；SSE 流中途 panic 只记日志
- [x] **healthz 反映上游健康**：新增 `degraded` + `providers[]` 字段
- [x] **smoke 端口预检**：脚本启动前检查端口占用，避免残留进程造成假失败
- [x] 删除无调用方的 `store.Store.Snapshot()`，README/ARCHITECTURE 与实现同步
- [x] **上游 Key 支持 `env:VAR` 引用**：真实 Key 走环境变量注入，不必写进 config.json；
      管理台回显保留引用本身，不脱敏不泄露
- [x] **成本告警**：Key 用量达到任一额度上限 80% 打 warn 日志（跨线只提示一次）
- [x] **Prometheus /metrics**：HTTP 状态计数、配额拒绝计数、上游健康/请求/错误/延迟 EWMA
- [x] **git init**：项目建立版本历史（首次提交）

## P0 — 首版已完成 ✅

- [x] 自制 Token Key（`sk-tr-`）签发 / 校验 / 启停 / 删除，只存哈希
- [x] OpenAI 兼容代理（`/v1/chat/completions`、`/v1/models`、`/v1/*` 透传）
- [x] SSE 流式逐帧转发 + 自动注入 `stream_options.include_usage`
- [x] 智能路由：优先级 failover、权重分流、延迟 EWMA 加权
- [x] 健康检查：连续失败摘除、冷却、半开探测
- [x] 配额：RPM 滑动窗口、总 token / 总金额 / 每日额度
- [x] 用量统计：按天 / 模型 / Key 聚合，JSONL 流水
- [x] Angular 管理台：概览 / Providers / 路由表 / Keys / 用量
- [x] 端到端冒烟脚本（`make smoke`）+ 单测（api/proxy/router/store/quota/auth 全覆盖）
- [x] Dockerfile + docker-compose（镜像内不装 Node，前端产物由宿主机预构建）

## P1 — 接下来值得做

- [ ] **多实例一致性**：当前配置与限流都在单进程内存里；多副本需要 SQLite/Postgres
      或引入文件锁 + reload 机制（`store` 已有原子写，可加 fsnotify 热加载）
- [ ] **Key 维度的模型白名单校验前置到 `/v1/models`**（当前已过滤，但错误信息可更具体）
- [ ] **请求审计采样**：按比例记录 prompt 摘要（注意脱敏，默认关闭）
- [ ] **流式 token 估算兜底**：上游不返回 usage 时按字符数粗估，标记 `estimated=true`
- [ ] **Docker 镜像体积**：当前基于 golang:1.22-alpine 多阶段，产物约 20MB；可再换 `scratch` + 静态链接（需验证 musl 下 SSE 与 DNS 解析）
- [ ] **成本告警管理台提示**：后端 warn 日志已上线，管理台红点提示待前端接入
- [ ] **/metrics 补充延迟分位**：当前暴露 EWMA gauge，QPS 上来后可加直方图分位

## P2 — 可选增强

- [ ] 支持 Anthropic / Gemini 协议转换（目前只做 OpenAI 兼容层）
- [ ] Key 分组（team / project）与归属标签
- [ ] 按 Key 设置允许的时间窗（如仅工作日 9:00–18:00）
- [ ] 缓存相同 prompt 的响应（按 Key 可选开启，注意隐私）
- [ ] 管理台支持多语言（当前中文）
- [ ] 配置变更历史（audit log）与回滚
- [ ] **【隐私合规】客户端 Key 速率限制确认/实现**：现有 key 配置有 `rpm` 字段，
      需确认是否真正生效；如未生效则实现基于 Key ID 的令牌桶/滑动窗口限流
- [ ] **【隐私合规】Provider API Key 加密存储**：当前默认明文存 config.json（虽权限 600），
      可加 master key 配置，存储时 AES 加密/读取时解密；或至少文档中强制要求 `env:` 引用

## 技术债

- `Router.Pick` 每次都 `ListRoutes()` / `ListProviders()`（会拷贝切片），
  高频场景下应改为读快照或加读缓存 —— 当前量级可接受，QPS 上千时优化。
- `quota.Recorder.Recent()` 从当天往前扫文件，流水量大时应改为倒序索引文件。
- `api.Settings()` 每次请求读配置要加 RLock，`quota.Limiter` 的 hits 无清理；
  当前 key 数量与请求量级下不痛，QPS 上千时再优化。
