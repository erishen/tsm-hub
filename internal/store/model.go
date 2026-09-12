// Package store 定义 tsm-hub 的持久化数据模型。
//
// 所有配置落在单个 JSON 文件（data/config.json），启动时全量载入内存，
// 变更走「临时文件 + rename」原子落盘，避免写一半断电导致配置损坏。
package store

import (
	"os"
	"strings"
	"time"
)

// CurrentVersion 是配置文件的 schema 版本，用于未来升级迁移。
const CurrentVersion = 1

// DefaultAdminToken 是初始化配置时写入的占位管理口令，上线前必须改掉。
const DefaultAdminToken = "change-me-admin"

// NoToolsModels 是已知不支持 tool calling（function calling）的模型静态名单。
// 来源：实际测试验证 + 模型类型推断（嵌入/内容安全/音频生成等专用模型通常不支持 tools）。
// 路由选择时：请求带 tools 会自动过滤掉此名单中的模型，避免上游 400 后才 failover。
// 动态检测：带 tools 请求返回 400 时也会运行时标记为不可用（30 分钟冷却）。
var NoToolsModels = map[string]bool{
	// OpenRouter 免费模型：实际测试带 tools 返回 400（model does not support tool calling）
	"nvidia/nemotron-3-super-120b-a12b:free": true,
	// 嵌入模型：不支持 chat completions，自然不支持 tools
	"qwen3.7-text-embedding":       true,
	"qwen3.7-text-embedding-flash": true,
	// 内容安全模型：专用分类，不支持 tools
	"nvidia/nemotron-3.5-content-safety:free": true,
	// 音频生成模型：不支持 tools
	"google/lyria-3-clip-preview": true,
	"google/lyria-3-pro-preview":  true,
}

// EnvKeyPrefix 表示 api_key 引用环境变量，如 "env:OPENAI_API_KEY"。
// 真实密钥通过环境变量注入，不必写进 config.json；配置落盘与回显保留该引用本身。
const EnvKeyPrefix = "env:"

// Price 描述某模型的计费单价（美元 / 1K tokens）。
type Price struct {
	InputPer1K  float64 `json:"input_per_1k"`
	OutputPer1K float64 `json:"output_per_1k"`
}

// Settings 是全局运行设置。
type Settings struct {
	Listen           string           `json:"listen"`
	AdminToken       string           `json:"admin_token"`
	DefaultTimeoutMS int              `json:"default_timeout_ms"`
	MaxBodyBytes     int64            `json:"max_body_bytes"`
	Pricing          map[string]Price `json:"pricing"`
	// FailThreshold 连续失败多少次后把 provider 标记为不健康。
	FailThreshold int `json:"fail_threshold"`
	// CooldownSec 不健康 provider 的冷却期，冷却结束后进入半开探测。
	CooldownSec int `json:"cooldown_sec"`
	// Smart 是 smart 成本智能路由的评分参数；未配置时用代码默认值。
	Smart SmartScoreCfg `json:"smart,omitempty"`
	// SkillsDir 指向外部 Agent Skills 技能库目录（如 resolve-skills/skills）。
	// 空 = 不启用技能库浏览。
	SkillsDir string `json:"skills_dir,omitempty"`
	// Agent 是网关 agent 能力（内置通用工具 + 服务端执行循环）的配置。
	Agent AgentCfg `json:"agent,omitempty"`
	// Mcps 是外部 MCP server 配置：name → {command, args, env}（stdio 传输）或
	// {transport:"http", url}（Streamable HTTP 远程）。
	// 连接后其工具以 mcp_<server>_<tool> 注册进网关工具池，由网关执行。
	Mcps map[string]MCPServer `json:"mcps,omitempty"`
	// IgnoredMcpServers 是外部 MCP 候选忽略列表：被忽略的 server 不出现在
	// 外部 MCP 候选列表里（即使调用方声明过其工具）。管理员可手动恢复。
	IgnoredMcpServers []string `json:"ignored_mcp_servers,omitempty"`
	// AuditRetentionDays 是审计日志保留天数，默认 90 天；<=0 时用默认值。
	AuditRetentionDays int `json:"audit_retention_days,omitempty"`
	// UsageRetentionDays 是用量流水保留天数，默认 0（不自动清理）；>0 时启动时清理过期文件。
	UsageRetentionDays int `json:"usage_retention_days,omitempty"`
	// Sandbox 是 Docker 沙箱（execute_code 工具）配置。
	Sandbox SandboxCfg `json:"sandbox,omitempty"`
	// Fastpath 是确定性快路径 + codegen 配置。
	Fastpath FastpathCfg `json:"fastpath,omitempty"`
	// TLS 是 HTTPS/TLS 配置；启用后网关直接监听 HTTPS，无需反向代理。
	TLS TLSCfg `json:"tls,omitempty"`
	// CORS 是跨域资源共享配置；浏览器端直接调用网关时需要。
	CORS CORSCfg `json:"cors,omitempty"`
}

// TLSCfg 配置网关的 HTTPS/TLS 终端。
type TLSCfg struct {
	// Enabled=true 时使用 ListenAndServeTLS 启动 HTTPS 服务。
	Enabled bool `json:"enabled,omitempty"`
	// CertFile 是 TLS 证书文件路径（PEM 格式）。
	CertFile string `json:"cert_file,omitempty"`
	// KeyFile 是 TLS 私钥文件路径（PEM 格式）。
	KeyFile string `json:"key_file,omitempty"`
}

// CORSCfg 配置跨域资源共享（CORS）策略。
type CORSCfg struct {
	// Enabled=true 时发送 CORS 响应头并处理预检请求。
	Enabled bool `json:"enabled,omitempty"`
	// AllowedOrigins 是允许的源列表；支持 "*" 表示允许所有源（不推荐生产环境）。
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	// AllowedMethods 是允许的 HTTP 方法列表，默认 GET,POST,PUT,DELETE,OPTIONS。
	AllowedMethods []string `json:"allowed_methods,omitempty"`
	// AllowedHeaders 是允许的请求头列表，默认 Content-Type,Authorization,X-Admin-Token,X-Session-Token。
	AllowedHeaders []string `json:"allowed_headers,omitempty"`
	// AllowCredentials 表示是否允许携带凭证（Cookie、Authorization 头）。
	AllowCredentials bool `json:"allow_credentials,omitempty"`
	// MaxAge 是预检请求缓存时间（秒），默认 86400（24小时）。
	MaxAge int `json:"max_age,omitempty"`
}

// FastpathCfg 配置确定性快路径（零模型回答）与 codegen（LLM 生成检测器）。
type FastpathCfg struct {
	// Enabled=true 时启用快路径：内置匹配器（算术/时间/日期/换算/统计/进制/字数）
	// 直接纯代码回答；false 时全部走 agent/模型。
	Enabled bool `json:"enabled,omitempty"`
	// Codegen=true 时内置匹配器未命中会请 LLM 生成 JS 检测器并持久化复用。
	Codegen bool `json:"codegen,omitempty"`
	// PluginsDir 检测器插件目录（默认 <data>/fastpath_plugins；晋升目录为
	// <data>/fastpath_promoted，与插件一起按 mtime 热重载）。
	PluginsDir string `json:"plugins_dir,omitempty"`
}

// SandboxCfg 配置 execute_code 的 Docker 沙箱执行环境。
type SandboxCfg struct {
	// Enabled=true 时注册 execute_code 工具（需本机 docker 可用）。
	Enabled bool `json:"enabled,omitempty"`
	// TimeoutSec 单次执行超时（默认 30s）。
	TimeoutSec int `json:"timeout_seconds,omitempty"`
	// MemoryMB 容器内存上限（默认 512）。
	MemoryMB int `json:"memory_mb,omitempty"`
	// CPUs 容器 CPU 上限（默认 1）。
	CPUs float64 `json:"cpus,omitempty"`
	// MaxOutputKB stdout/stderr 输出上限（默认 100KB）。
	MaxOutputKB int `json:"max_output_kb,omitempty"`
}

// MCPServer 描述一个 MCP server 连接。Transport 为空或 "stdio" 时是本地进程
// （Command/Args/Env）；"http" 时是远程 Streamable HTTP 端点（URL）。
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Transport 传输方式："" / "stdio" = 本地子进程；"http" = Streamable HTTP 远程。
	Transport string `json:"transport,omitempty"`
	// URL 是 http 传输的 MCP endpoint（如 http://127.0.0.1:8787/mcp）。
	URL string `json:"url,omitempty"`
	// TimeoutSec 是工具调用的超时秒数；0 时用默认 30s。
	// 长任务类 MCP（如数据管线、耗时 review）需要调大。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// AgentCfg 配置网关 agent：客户端不传 tools 时，网关自动附加内置工具池
// （get_time/calc/fetch_url/echo/skill-run/remember/recall/read_file）并在
// 服务端执行 tool_calls 循环，最终把答案（含工具结果）返回给客户端。
type AgentCfg struct {
	// Disabled=true 时关闭网关工具循环（默认启用）。
	Disabled bool `json:"disabled,omitempty"`
	// MaxRounds 工具循环最大轮数（默认 4）。
	MaxRounds int `json:"max_rounds,omitempty"`
	// AllowPrivateURL 放行 fetch_url 访问内网/环回地址（默认拒绝，防 SSRF）。
	AllowPrivateURL bool `json:"allow_private_url,omitempty"`
	// ReadRoot 为 read_file 的允许根目录；空 = 不提供 read_file 工具。
	ReadRoot string `json:"read_root,omitempty"`
	// MemoryFile 为 remember 的持久化文件；空 = 仅进程内存（重启丢失）。
	MemoryFile string `json:"memory_file,omitempty"`
}

// SmartScoreCfg 是 smart 策略的可调参数。0 值表示用默认值。
type SmartScoreCfg struct {
	// StrategyMode 策略模式：cost_first / stability_first / task_aware / balanced
	// - cost_first: 成本优先（默认，保持向后兼容）
	// - stability_first: 稳定性优先，成功率高的 Provider 优先
	// - task_aware: 任务感知，根据任务类型选择合适的模型
	// - balanced: 平衡模式，综合考虑所有维度
	StrategyMode string `json:"strategy_mode,omitempty"`
	// 各维度权重（0表示用默认值，范围0-200）
	CostWeight       int `json:"cost_weight,omitempty"`       // 成本权重
	StabilityWeight  int `json:"stability_weight,omitempty"`  // 稳定性权重（成功率）
	LatencyWeight    int `json:"latency_weight,omitempty"`    // 延迟权重
	CapabilityWeight int `json:"capability_weight,omitempty"` // 能力匹配权重
	// FreeBonus 免费模型的基础加分（默认 100）。
	FreeBonus int `json:"free_bonus"`
	// HalfOpenPenalty 半开（刚过冷却探测期）候选的扣分（默认 30）。
	HalfOpenPenalty int `json:"half_open_penalty"`
	// ThrottleSec 上游 429 后的冷却秒数，期间 smart/健康过滤不选该 provider（默认 60）。
	ThrottleSec int `json:"throttle_sec"`
	// UnavailableSec 上游 404 模型不存在后的标记时长，期间目录/路由过滤该模型（默认 1800）。
	UnavailableSec int `json:"unavailable_sec"`
	// PriceTiers 单价分档加分：prompt 单价 <= PromptMax 的档得分 Score（从高到低匹配，
	// 未配置时默认 [{0,60},{0.5,40},{2,20},{10,5}]）。
	PriceTiers []PriceTier `json:"price_tiers,omitempty"`
}

// PriceTier 是一个单价档位。
type PriceTier struct {
	PromptMax float64 `json:"prompt_max"`
	Score     int     `json:"score"`
}

// UnavailableModel 记录某个 provider 上某个模型被上游判为不可用（如 404 model not found）。
type UnavailableModel struct {
	// Reason 上游返回的原因摘要。
	Reason string `json:"reason"`
	// Until 冷却到期时间，之后自动恢复（免费/模型可能恢复上线）。
	Until time.Time `json:"until"`
}

// UnavailableModelView 是管理台用的不可用模型视图。
type UnavailableModelView struct {
	ProviderID string    `json:"provider_id"`
	Model      string    `json:"model"`
	Reason     string    `json:"reason"`
	Until      time.Time `json:"until"`
}

// Provider 是一个上游 LLM 服务（OpenAI / DeepSeek / 通义 / 本地 Ollama ...）。
// ProbeModel 是一次按 Key 探测 /v1/models 时上游返回的模型元信息快照。
// 免费/价格/上下文会随上游调整（OpenRouter :free 列表、agnes 促销免费等），
// 目录页优先用最近一次探测的快照，静态知识表兜底。
type ProbeModel struct {
	ID            string   `json:"id"`
	ContextLength int      `json:"context_length,omitempty"`
	Free          bool     `json:"free,omitempty"`
	Pricing       *Pricing `json:"pricing,omitempty"`
}

// Pricing 是模型单价（$/1M tokens，输入/输出）。
type Pricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// Provider 是上游服务商配置。
type Provider struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	APIKey    string            `json:"api_key"`
	Models    []string          `json:"models"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	Weight    int               `json:"weight"`
	Priority  int               `json:"priority"`
	TimeoutMS int               `json:"timeout_ms"`
	// Protocol 指定上游 Provider 使用的 API 协议。
	// 支持："openai"（默认，OpenAI 兼容）、"anthropic"（Anthropic Claude）。
	// 网关对外只暴露 OpenAI 兼容 API，适配器负责请求/响应格式转换。
	Protocol  string    `json:"protocol,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ProbeAt 是最近一次探测成功的时间；ProbeModels 是那次探测的模型快照。
	ProbeAt     time.Time    `json:"probe_at,omitempty"`
	ProbeModels []ProbeModel `json:"probe_models,omitempty"`
}

// ResolvedAPIKey 返回实际用于上游鉴权的 Key：
// 配置为 env:NAME 时读取环境变量；变量缺失/为空时返回原串（由上游 401 暴露配置问题），
// 非 env 引用则原样返回。该方法不修改落盘配置，env 引用始终保留。
func (p Provider) ResolvedAPIKey() string {
	if name, ok := strings.CutPrefix(p.APIKey, EnvKeyPrefix); ok {
		if v, ok := os.LookupEnv(strings.TrimSpace(name)); ok && v != "" {
			return v
		}
	}
	return p.APIKey
}

// RouteTarget 是路由表里的一个候选上游。
type RouteTarget struct {
	ProviderID string `json:"provider_id"`
	// Model 是转发给上游时使用的模型名，留空表示沿用请求里的模型名。
	Model    string `json:"model,omitempty"`
	Weight   int    `json:"weight"`
	Priority int    `json:"priority"`
}

// Route 把一个「对外模型名 / 别名」映射到若干上游候选。
type Route struct {
	Model string `json:"model"`
	// Strategy 取值：weighted（同优先级内加权随机）、failover（按优先级顺序降级）。
	Strategy string        `json:"strategy"`
	Targets  []RouteTarget `json:"targets"`
	// Remark 备注（如「百炼免费额度优先，用完可删」等临时策略说明），仅展示用。
	Remark string `json:"remark,omitempty"`
}

// Quota 是绑定在自制 Key 上的配额。0 表示不限制。
type Quota struct {
	MaxTokens   int64   `json:"max_tokens"`
	MaxCostUSD  float64 `json:"max_cost_usd"`
	DailyTokens int64   `json:"daily_tokens"`
	RPM         int     `json:"rpm"`
}

// APIKey 是对外签发的自制 Token Key。明文只在创建时返回一次，库里只存哈希。
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Hash      string    `json:"hash"`
	Enabled   bool      `json:"enabled"`
	Models    []string  `json:"models,omitempty"`
	Quota     Quota     `json:"quota"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// InjectSkills 技能注入模式：""=不注入；"list"=技能清单；
	// "all"=全部技能全文；其他值=单个技能名。转发 chat 请求时注入 system prompt。
	InjectSkills string `json:"inject_skills,omitempty"`
	// AgentDisabled 为 true 时，该 key 的请求不经过网关 agent（即使客户端未传 tools），
	// 直接透传到上游模型。适用于客户端自己有完整 Agent 流水线的场景（如 crewai-pse）。
	AgentDisabled bool `json:"agent_disabled,omitempty"`
	// ToolsAllow 工具白名单（精确工具名，如 calc / mcp_fs_read_file）：
	// 非空时网关 agent 只把名单内的工具 schema 发给上游，其余全部裁掉。
	// 空 = 不限制（全量注入）。用于按客户端裁剪 token 开销。
	ToolsAllow []string `json:"tools_allow,omitempty"`
	// McpsAllow MCP server 白名单：非空时只对名单内的 server 建连并暴露其工具，
	// 其余 server 既不建连也不出现在 tool schemas（省 token + 省握手）。
	// 空 = 不限制。与 ToolsAllow 叠加生效（两者都非空时取交集语义：MCP 工具
	// 需同时满足 server 在 McpsAllow 内且完整工具名在 ToolsAllow 内）。
	McpsAllow []string `json:"mcps_allow,omitempty"`
}

// Config 是 data/config.json 的整体结构。
// ExternalTool 是从外部调用方"择优录用"的能力：调用方在请求里声明了系统外的自创能力，
// 高频使用后由管理员录用进系统（模型可感知；可选绑定网关侧实现）。
type ExternalTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Kind: tool（默认，可执行工具）/ skill（技能说明，skill-run 可注入）。
	Kind string `json:"kind,omitempty"`
	// ImplType: none（仅登记，执行在调用方侧）/ js（JS detect(query) 检测器）/ alias（转发到现有工具）。
	ImplType   string `json:"impl_type"`
	ImplSource string `json:"impl_source,omitempty"`
	AdoptedAt  string `json:"adopted_at"`
}

type Config struct {
	Version       int            `json:"version"`
	Settings      Settings       `json:"settings"`
	Providers     []Provider     `json:"providers"`
	Routes        []Route        `json:"routes"`
	Keys          []APIKey       `json:"keys"`
	ExternalTools []ExternalTool `json:"external_tools,omitempty"`
}

// UsageRecord 是一条请求用量流水，按天追加写入 JSONL。
type UsageRecord struct {
	TS              time.Time `json:"ts"`
	KeyID           string    `json:"key_id"`
	Model           string    `json:"model"`
	ProviderID      string    `json:"provider_id"`
	UpstreamModel   string    `json:"upstream_model"`
	PromptTokens    int       `json:"prompt_tokens"`
	CompletionToken int       `json:"completion_tokens"`
	TotalTokens     int       `json:"total_tokens"`
	CostUSD         float64   `json:"cost_usd"`
	LatencyMS       int64     `json:"latency_ms"`
	Stream          bool      `json:"stream"`
	Status          int       `json:"status"`
	Error           string    `json:"error,omitempty"`
	// Scene 是智能分流命中的场景（chat/reason/code/fast；非 auto 请求为空）。
	Scene string `json:"scene,omitempty"`
	// FastPath 是确定性快路径命中标记（内置方法名 / plugin:<name> / codegen；未命中为空）。
	FastPath string `json:"fastpath,omitempty"`
	// Attempt 是本请求实际尝试的第几个候选（1=首次命中；>1 表示发生过 failover）。
	Attempt int `json:"attempt,omitempty"`
	// Failover 是 failover 链：按顺序记录每个失败候选（不含最终命中的那个）。
	// 旧流水无此字段，兼容。
	Failover []FailoverStep `json:"failover,omitempty"`
	// ClientTools 是请求里客户端声明的工具名（去重，最多 20 个）。
	ClientTools []string `json:"client_tools,omitempty"`
	// ExecTools 是网关 agent 实际执行过的工具名（含 skill-run 的 skill、mcp_* 工具，按执行序）。
	ExecTools []string `json:"exec_tools,omitempty"`
}

// FailoverStep 是 failover 链中的一个失败候选。
type FailoverStep struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model,omitempty"`
	Error      string `json:"error,omitempty"`
}
