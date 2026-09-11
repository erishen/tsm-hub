# tsm-hub TODO

> 最后更新：2026-09-11
> 项目状态：v1.0 稳定版，核心功能全部完成

## v1.0 已完成 ✅

### 网关核心

- [x] 自制 Token Key（`sk-tr-`）签发 / 校验 / 启停 / 删除，只存 SHA-256 哈希
- [x] OpenAI 兼容代理（`/v1/chat/completions`、`/v1/models`、`/v1/*` 透传）
- [x] SSE 流式逐帧转发 + 自动注入 `stream_options.include_usage`
- [x] 智能路由：优先级 failover、权重分流、延迟 EWMA 加权
- [x] 健康检查：连续失败摘除、冷却、半开探测（healthy/half-open/degraded 三态）
- [x] 配额：RPM 滑动窗口、总 token / 总金额 / 每日额度
- [x] 用量统计：按天 / 模型 / Key 聚合，JSONL 流水 + 一键清空
- [x] 客户端发现接口：`/v1/models`、`/v1/tools`、`/v1/mcps`、`/v1/skills`（无需 admin）

### 协议适配层（25 个平台）

- [x] **13 种国外协议**：OpenAI、Anthropic Claude、Azure OpenAI、Google Gemini、AWS Bedrock、AWS SageMaker、Cohere、Mistral、HuggingFace、Replicate、Together AI、Fireworks AI、Groq
- [x] **12 个国内平台**（OpenAI 兼容）：智谱 AI、阿里通义千问、百度文心一言、腾讯混元、字节豆包、Kimi、零一万物、DeepSeek、商汤日日新、硅基流动、MiniMax、讯飞星火
- [x] 适配器架构：`ProtocolAdapter` 接口 + 注册表 + `RequestSigner` 可选接口（AWS SigV4）
- [x] 上游平台页面（`/platforms`）：展示 25 个平台详情 + 一键接入跳转

### 能力池（Tools + Skills + MCPs）

- [x] **Tools**：内置工具池（get_time/calc/read_file/fetch_url/remember/recall 等），可被 LLM 感知和调用
- [x] **Skills**：技能库（gitmodules 安装），默认注入技能清单，支持技能执行
- [x] **MCPs**：MCP Server 管理（stdio/SSE），预装常用 MCP（serena/fs/memory/think），工具池聚合
- [x] **记忆（Memory）**：SQLite 持久化存储，支持 remember/recall，重启不丢
- [x] **沙箱（Sandbox）**：Docker 沙箱执行环境，代码执行隔离

### 发现与晋升机制

- [x] **fastpath**：常用请求模式快速路径，codegen 自动生成优化代码
- [x] **外部发现**：自动记录调用方使用的自定义 tools/skills/mcps，统计使用情况
- [x] **择优晋升**：高频使用的外部能力可晋升为系统内置（fastpath 插件或 Tools），晋升时可选目标类型
- [x] **晋升管理台**：查看候选、使用统计、一键晋升

### 管理台（18 个页面）

- [x] 概览（Dashboard）：Providers 实时健康、请求统计、快捷入口
- [x] Providers：增删改查、协议选择、模型探测、健康状态显示
- [x] 上游平台：25 个平台展示 + 快速接入
- [x] 路由表：智能路由配置、弹窗编辑、免费模型优先
- [x] Token Keys：签发/复制/启停/删除、外部接入说明（OpenAI 兼容）
- [x] 模型目录：分类展示、评分、免费标记、按 provider 区分
- [x] 应用推荐：根据模型池推荐适用场景
- [x] 额度查询：多平台余额/额度、缓存、手动刷新、控制台链接
- [x] 技能库：技能列表、Markdown 预览、执行
- [x] MCP：Server 管理、工具池展示、外部候选、弹窗编辑
- [x] 工具：内置工具列表、详情弹窗、描述截断
- [x] 记忆：记忆管理、SQLite 持久化
- [x] 沙箱：Docker 沙箱管理
- [x] 快路径：fastpath 插件管理、晋升目标选择
- [x] 用量：按天趋势、模型/Key 归因、成本计算（免费模型不计费）、清空
- [x] 监控：Provider 归因、按天趋势、状态分布
- [x] 审计日志：操作审计 SQLite 存储、查询
- [x] 测试（Playground）：模型选择（免费优先）、Markdown 渲染、流式输出

### 安全与合规

- [x] Admin Token 鉴权（`X-Admin-Token`），登录暴力尝试防护（5 次锁 10 分钟）
- [x] Key SHA-256 哈希存储，新建只展示一次明文，2 分钟重看窗口
- [x] config.json 权限 600，上游 Key 支持 `env:VAR` 引用（不写进配置文件）
- [x] 审计日志 SQLite（操作记录）
- [x] 请求体超限保护（413）、panic 兜底（500 + Request ID）
- [x] 4xx 不误熔断、上游错误响应 gzip 解压、JSON 错误格式化
- [x] Prometheus `/metrics`：HTTP 状态、配额拒绝、上游健康/请求/错误/延迟 EWMA

### 工程质量

- [x] Go 编译零警告、`go vet` 通过
- [x] 前端编译零警告（Angular）
- [x] 单元测试全部通过（8 个包，平均覆盖率 ~60%）
- [x] 端到端冒烟脚本（`make smoke`）
- [x] Dockerfile + docker-compose（多阶段构建，镜像 ~20MB）
- [x] 代码重构：admin.go 拆分（2656行→6文件）、前端模板抽取（内联→外部HTML/CSS）、app 目录按类型分子目录、适配器子包
- [x] 文档：README（中英）、ARCHITECTURE、SECURITY、TODO

### 近期修复

- [x] **stream_options 丢失**：OpenAI 适配器重新序列化请求体时丢掉了 rewriteBody 注入的 `stream_options.include_usage`，改为纯透传
- [x] **DeepSeek 余额字符串解析**：`flexFloat` 类型未实现 `UnmarshalJSON`，无法解析 `"51.75"` 字符串格式，已补全
- [x] **Provider 健康状态全 degraded**：Healthy 计算逻辑从 `不在冷却 && failures<failMax` 改为 `不在冷却`
- [x] **前端 NG8107 警告**：批量修复可选链操作符冗余

---

## v1.1 待办（可选增强）

### P1 — 推荐做

- [ ] **多实例一致性**：当前配置与限流都在单进程内存里；多副本需要 SQLite/Postgres
      或引入文件锁 + reload 机制（`store` 已有原子写，可加 fsnotify 热加载）
- [ ] **流式 token 估算兜底**：上游不返回 usage 时按字符数粗估，标记 `estimated=true`
- [ ] **成本告警管理台红点**：后端 warn 日志已上线，管理台红点提示待前端接入
- [ ] **Provider API Key AES 加密存储**：当前默认明文存 config.json（权限 600 + env: 引用可规避），
      可加 master key 配置，存储时 AES 加密/读取时解密

### P2 — 有空再做

- [ ] Key 分组（team / project）与归属标签
- [ ] 按 Key 设置允许的时间窗（如仅工作日 9:00–18:00）
- [ ] 缓存相同 prompt 的响应（按 Key 可选开启，注意隐私）
- [ ] 管理台支持多语言（当前中文）
- [ ] `/metrics` 补充延迟分位直方图（当前 EWMA gauge）
- [ ] Docker 镜像换 `scratch` + 静态链接（需验证 musl 下 SSE 与 DNS）
- [ ] Key 维度模型白名单校验错误信息更具体

---

## 技术债（当前量级可接受，QPS 上千时优化）

- `Router.Pick` 每次都 `ListRoutes()` / `ListProviders()`（会拷贝切片），高频场景应加读缓存
- `quota.Recorder.Recent()` 从当天往前扫文件，流水量大时应改为倒序索引文件
- `api.Settings()` 每次请求读配置要加 RLock，`quota.Limiter` 的 hits 无清理
