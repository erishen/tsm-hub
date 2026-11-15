# tsm-hub TODO

> 最后更新：2026-11-15
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
- [x] 上游 Key 可选 AES-256-GCM 落盘加密（`TSM_HUB_MASTER_KEY` → `enc:v1:`，fail-closed），
      配置写文件锁（flock）+ SIGHUP 兜底热加载
- [x] 审计日志 SQLite（操作记录）
- [x] 请求体超限保护（413）、panic 兜底（500 + Request ID）
- [x] 4xx 不误熔断、上游错误响应 gzip 解压、JSON 错误格式化
- [x] Prometheus `/metrics`：HTTP 状态、配额拒绝、上游健康/请求/错误/延迟 EWMA、
      按固定枚举分类的上游失败、HTTP 总耗时（按 flow 拆分）与流式首字节直方图

### 工程质量

- [x] Go 编译零警告、`go vet` 通过
- [x] 前端编译零警告（Angular）
- [x] 单元测试全部通过（`go test ./...` 覆盖 14 个包；`make test` 前置 `fmt-check` 门禁，
      `make check` 聚合格式/单测/vet/隐私四道纯本地门禁，`make test-race` 竞态回归亦全绿）。
      `make cover` 已修好并自动回退到完整 Go 1.25 SDK；补齐适配器子包契约测试后
      该包覆盖率 8.9% → 86.6%，全仓总覆盖率 51.4% → **58.8%**
      （2026-11-15 再补 `internal/config` 86.0% / `internal/audit` 89.0% 单测，此前两包 0.0% 且
      无间接覆盖，是安全敏感路径盲区；顺带揪出并修复 `audit.maskSensitive` 死循环，见近期修复）
- [x] 端到端冒烟脚本（`make smoke`）
- [x] Dockerfile + docker-compose（多阶段构建，tsm-hub 镜像 37.8MB；监控栈已加内存/CPU 上限）
- [x] 代码重构：admin.go 拆分（2656行→6文件）、前端模板抽取（内联→外部HTML/CSS）、app 目录按类型分子目录、适配器子包
- [x] 文档：README（中英）、ARCHITECTURE、SECURITY、TODO

### 近期修复

- [x] **stream_options 丢失**：OpenAI 适配器重新序列化请求体时丢掉了 rewriteBody 注入的 `stream_options.include_usage`，改为纯透传
- [x] **DeepSeek 余额字符串解析**：`flexFloat` 类型未实现 `UnmarshalJSON`，无法解析 `"51.75"` 字符串格式，已补全
- [x] **Provider 健康状态全 degraded**：Healthy 计算逻辑从 `不在冷却 && failures<failMax` 改为 `不在冷却`
- [x] **前端 NG8107 警告**：批量修复可选链操作符冗余
- [x] **监控栈瘦身与资源上限**（2026-11-15）：钉钉桥 `python:3.11-slim` → `3.11-alpine`
      （150MB → 58.9MB，仅用标准库 + alpine CA 证书）；Prometheus / Grafana / Alertmanager /
      blackbox / 桥按实测常驻内存的 4~8 倍加 `mem_limit` 与 `cpus`；Prometheus 加
      `--storage.tsdb.retention.size=2GB` 双上限。Grafana 11.4.0 官方无 alpine tag
      （`-ubuntu` 反而 539MB），镜像维持 479MB 不动
- [x] **客户端能力探测 405 风暴**（2026-10-18）：GET `/v1/chat/completions/tools|mcps|skills` 归一到正式能力端点直接返回目录；非 POST 打纯 chat 路径本地 405（详见 error-rate-investigation-2026-10-18.md §7）
- [x] **config 热加载原子替换失效**（2026-10-18）：watchConfig 改盯目录 + 文件名过滤，saveLocked/编辑器原子保存后仍生效（§8）
- [x] **数据竞争修复**（2026-10-18）：`go test -race` 全绿；客户端断开（499）不计 provider 失败、不计错误率
- [x] **错误率 41.7% → 33.9%**（2026-10-18）：放宽 isModelNotFound、清理路由死模型、gemini 移出默认路由、alibailian 禁用
- [x] **`audit.maskSensitive` 死循环修复**（2026-11-15）：替换敏感值为 `"***"` 后同一 `"api_key":"` 前缀仍匹配，
      内层 `for` 在同一 idx 反复重置换成 `***` 永不终止 → 任何 detail 含 `api_key`/`token`/`password`/`secret`
      的审计记录会让请求 goroutine 永久阻塞（正是隐私门禁路径，创建 provider 带 api_key 即触发）。
      修法：维护 `from` 游标跳过已处理片段。新增 `internal/audit` 单测（含 `TestRecordSensitiveMasked` /
      `TestMaskSensitive`）锁死回归；`internal/config` 同步补 13 例单测。总覆盖率 56.9% → **58.8%**
- [x] **出站 HTTP client 超时加固**（2026-11-15）：`http.DefaultClient`（Timeout=0 永不超时）
      → 共享带超时 client（管理台/模型探测 20s、工具调用 15s，另有 `toolHTTPClient`）。
      属防御性加固：逐处核实调用方本已带 `context.WithTimeout`（3~12s），无活跃 bug
- [x] **`TSM_HUB_MASTER_KEY` 配错启动 fail fast**（2026-11-15）：磁盘为明文配置时，
      格式错的主密钥原本要到首次管理台保存才报 `seal config secrets`（错误暴露过晚）；
      启动路径前置 `store.CheckMasterKey()` 校验，配错直接拒绝启动。
      `TestCheckMasterKey` 6 例锁死（未设/合法 hex/base64/63 位 hex/短 base64）

---

## v1.1 待办（可选增强）

### P1 — 推荐做

- [x] **多实例一致性**（2026-11-15 完成）：`saveLocked` 写入前对 `config.json.lock`
      取 `syscall.Flock(LOCK_EX)` 跨进程串行化（写侧本身已是 tmp+fsync+rename 原子替换）；
      新增 SIGHUP 兜底热加载（fsnotify 盯目录为主，Reload 幂等、失败保留旧配置）。
      多副本仍共享单机内存限流状态，跨实例配额同步仍需 SQLite/Postgres（见 P2 后续）
- [ ] **流式 token 估算兜底**：上游不返回 usage 时按字符数粗估，标记 `estimated=true`
      （2026-10-18 核查：当前路由内 provider 全部正常返回 usage，暂不实施）
- [x] **成本告警管理台红点**（2026-11-15 完成）：`quota.Limiter.SnapshotWarnings`
      实时快照使用率 ≥80% 的 key（不改限流状态）；`GET /api/admin/cost-warnings`
      按使用率降序返回；管理台首页由 Go 侧 serveIndex 运行时注入原生 JS 红点
      （幂等、不碰前端源码、`make web-build` 后仍生效），点击跳转 `/keys`
- [x] **Provider API Key AES 加密存储**（2026-11-15 完成）：设 `TSM_HUB_MASTER_KEY`
      （64 位 hex / 44 位 base64 = 32 字节）后，config.json 落盘的 `providers[].api_key`
      变为 `enc:v1:<nonce||ct>`（AES-256-GCM，随机 nonce）；内存态仍明文供鉴权；
      未设密钥 = 明文兼容（env: 引用不加密），有密文无密钥 fail-closed；
      密钥格式启动时校验、配错拒绝启动（`store.CheckMasterKey` fail fast，
      避免拖到首次管理台保存才报错）；privacy-check 门禁同步放行 `enc:v1:` 密文
- [x] **admin token 保持现状，不再提醒**（2026-11-14 用户明确指令）：启动时长度 <16 只告警
      不拒绝；当前部署口令偏短，但服务只绑 `127.0.0.1`，**刻意不调整**。另注意
      `docker-compose.yml` 里 `TSM_HUB_ADMIN_TOKEN` 是注释掉的、compose 只注入显式列出的
      变量，因此改 `.env` 的同名键不会生效；生效值始终是 `data/config.json` 的
      `settings.admin_token`

### P2 — 有空再做

- [ ] Key 分组（team / project）与归属标签
- [ ] 按 Key 设置允许的时间窗（如仅工作日 9:00–18:00）
- [ ] 缓存相同 prompt 的响应（按 Key 可选开启，注意隐私）
- [ ] 管理台支持多语言（当前中文）
- [x] `/metrics` 补充延迟分位直方图（2026-11-14 完成）：HTTP 总耗时直方图带
      `flow=stream|buffered`（SSE 长连接不再污染普通请求 p95），另有
      `http_first_byte_seconds` 首字节（TTFB）直方图；上游失败也补了
      `upstream_error_classes_total{provider,class}` 固定枚举分类
- [ ] Docker 镜像换 `scratch` + 静态链接（需验证 musl 下 SSE 与 DNS）
- [ ] Key 维度模型白名单校验错误信息更具体

---

## 技术债（当前量级可接受，QPS 上千时优化）

- `Router.Pick` 每次都 `ListRoutes()` / `ListProviders()`（会拷贝切片），高频场景应加读缓存
- `quota.Recorder.Recent()` 从当天往前扫文件，流水量大时应改为倒序索引文件
- ~~`api.Settings()` 需加 RLock / `quota.Limiter` hits 无清理~~ **（2026-11-15 已核/已修）**：
  `internal/store/store.go` 的 `Settings()` 早已有 `s.mu.RLock()`，前半条技术债不成立、
  已删除以免误导；`Limiter` 新增按分钟节流的惰性 sweep，删除窗口内已无请求的 key 的
  `hits`/`warn` 条目并压缩时间戳切片，`Snapshot()` 只报活跃 key（管理台按 `rpm[k.ID]`
  取值，缺失即 0，对外等价）
- ~~无 gofmt/lint 门禁~~ **（2026-11-14 已修）**：新增 `make fmt-check`（`gofmt -l internal cmd`
  非空即失败）并挂到 `make test` 前置；本次又清掉 `internal/a2a/` 4 个文件的漂移
  （Go 1.25 把注释列表 `* ` 规范成 `- `、struct tag 对齐变化）。仓库仍无 CI，门禁落在 Makefile
- ~~`make cover` 依赖完整 Go 1.25 工具链~~ **（2026-11-15 已修，非仓库缺陷）**：本机原只装
  Go 1.22.2，`go.mod` / `go.work` 要求 1.25 ⇒ Go 自动下载的 `golang.org/toolchain` 缓存被裁剪，
  `go test -coverprofile` 报 `go: no such tool "covdata"`；`GOTOOLCHAIN=local` 会被版本要求挡回。
  现已装好官方 SDK `~/sdk/go1.25.0`（tarball SHA256 与官方一致），`make cover` 会按 GOROOT
  识别残缺缓存并自动回退到该 SDK，也可 `make cover GO=/path/to/go` 显式指定。
  实测 14 包全绿、总覆盖率 **56.9%**（11-15 补完适配器测试后），`make test` / `go vet` /
  镜像构建都不受影响。
  ⚠️ 11-14 版按「`pkg/tool/.../covdata` 文件是否存在」自检是错的：官方完整 SDK 同样没有
  独立 `covdata` 二进制（Go 1.25 由 go 命令内置），会误报；11-15 改为按 GOROOT 路径识别
  残缺的自动下载 toolchain + 前置 SDK 的 bin 到 PATH，并把失败明确区分为工具链问题/测试问题
