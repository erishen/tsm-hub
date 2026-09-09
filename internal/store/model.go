// Package store 定义 llm-router 的持久化数据模型。
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
}

// SmartScoreCfg 是 smart 策略的可调参数。0 值表示用默认值。
type SmartScoreCfg struct {
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
	ID            string  `json:"id"`
	ContextLength int     `json:"context_length,omitempty"`
	Free          bool    `json:"free,omitempty"`
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
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	// ProbeAt 是最近一次探测成功的时间；ProbeModels 是那次探测的模型快照。
	ProbeAt     time.Time     `json:"probe_at,omitempty"`
	ProbeModels []ProbeModel  `json:"probe_models,omitempty"`
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
}

// Config 是 data/config.json 的整体结构。
type Config struct {
	Version   int        `json:"version"`
	Settings  Settings   `json:"settings"`
	Providers []Provider `json:"providers"`
	Routes    []Route    `json:"routes"`
	Keys      []APIKey   `json:"keys"`
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
}
