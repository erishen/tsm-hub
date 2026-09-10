/** 与 Go 端 internal/api 的 JSON 结构保持一致。 */

export interface Agg {
  requests: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cost_usd: number;
  errors: number;
  failovers: number;
  latency_sum_ms: number;
}

/** Agg + 派生指标（后端 toAggView 输出）。 */
export interface AggView extends Agg {
  avg_latency_ms: number;
  error_rate: number;
}

export interface Quota {
  max_tokens: number;
  max_cost_usd: number;
  daily_tokens: number;
  rpm: number;
}

export interface Provider {
  id: string;
  name: string;
  base_url: string;
  api_key: string; // 返回时已脱敏
  models: string[];
  headers?: Record<string, string>;
  enabled: boolean;
  weight: number;
  priority: number;
  timeout_ms: number;
  healthy: boolean;
  latency_ms: number;
}

export interface RouteTarget {
  provider_id: string;
  model?: string;
  weight: number;
  priority: number;
}

export interface Route {
  model: string;
  strategy: 'weighted' | 'failover' | 'smart';
  targets: RouteTarget[];
  /** 备注（如「百炼免费额度优先，用完可删」等临时策略说明） */
  remark?: string;
}

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  enabled: boolean;
  models?: string[];
  quota: Quota;
  created_at: string;
  expires_at?: string;
  usage?: Agg;
  rpm_current: number;
  tools_used?: string[];
  tools_declared?: string[];
  /** 技能注入：""=不注入；list=技能清单；all=全部技能；其他=单个技能名。 */
  inject_skills?: string;
}

export interface Overview {
  providers: number;
  healthy: number;
  routes: number;
  keys: number;
  requests_total: number;
  today: Agg;
  total: Agg;
}

export interface DailyPoint extends Agg {
  date: string;
}

export interface UsageResponse {
  days: DailyPoint[];
  models: { model: string; usage: Agg }[];
  keys: { key_id: string; usage: Agg }[];
  recent: UsageRecord[];
}

export interface UsageRecord {
  ts: string;
  key_id: string;
  model: string;
  provider_id: string;
  upstream_model: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cost_usd: number;
  latency_ms: number;
  stream: boolean;
  status: number;
  error?: string;
  /** 智能分流命中的场景（chat/reason/code/fast；非 auto 为空）。 */
  scene?: string;
  /** 确定性快路径命中标记（方法名/plugin:xx/codegen）。 */
  fastpath?: string;
  /** 实际尝试的第几个候选（>1 表示发生过 failover）。 */
  attempt?: number;
  /** failover 链：按顺序记录每个失败候选（不含最终命中的那个）。 */
  failover?: FailoverStep[];
  /** 请求里客户端声明的工具名。 */
  client_tools?: string[];
  /** 网关 agent 实际执行过的工具名。 */
  exec_tools?: string[];
}

export interface FailoverStep {
  provider_id: string;
  model?: string;
  error?: string;
}

/** 快路径尝试链中的一步（管理台测试/生成用）。 */
export interface FastPathProbe {
  stage: string;
  hit: boolean;
  detail?: string;
}

export interface ObservabilityResponse {
  today: AggView;
  week: AggView;
  month: AggView;
  providers: { id: string; name: string; usage: AggView; skipped: number }[];
  scenes: { scene: string; usage: AggView }[];
  trend: DailyPoint[];
  tools: { name: string; calls: number; key_count: number }[];
  /** skill:<name> 前缀聚合的技能执行统计。 */
  skills: { skill: string; calls: number; key_count: number }[];
  /** mcp_<server>_<tool> 按 server 聚合的 MCP 工具统计。 */
  mcps: { server: string; calls: number; key_count: number }[];
  /** 调用方声明但不在网关目录里的外部自创工具（adopted=已被录用进网关）。 */
  external_tools: { name: string; calls: number; key_count: number; adopted: boolean; kind?: string; impl_type?: string; description?: string }[];
}

export interface ProviderHealth {
  provider_id: string;
  healthy: boolean;
  failures: number;
  last_error?: string;
  last_ok_at?: string;
  last_fail_at?: string;
  down_until?: string;
  latency_ms: number;
  requests: number;
  errors: number;
}

/** 模型目录条目：Provider×模型 一行（同一模型在不同 Provider 的免费/价格可能不同，不合并）。 */
export interface CatalogModel {
  id: string;
  provider: string;
  category: string;
  purpose: string;
  context?: string;
  /** 最近一次探测的上游实时上下文窗口（token 数），优先于静态 context。 */
  context_length?: number;
  free: boolean;
  pricing?: { prompt: string; completion: string };
  /** 非空表示该 Provider×模型曾被上游 404（model not found），冷却期内路由会跳过。 */
  unavailable?: string;
}

/** 探测返回的模型元信息：上下文窗口（token 总量）与免费标记。 */
export interface ProbeModel {
  id: string;
  context_length?: number;
  free?: boolean;
  /** 单价（$/1M tokens），OpenRouter 类平台在 models 响应提供。 */
  pricing?: { prompt: string; completion: string };
}

/** 探测返回的账户余额/额度（token 可使用总量），格式因上游而异。 */
export interface Balance {
  kind: 'moonshot' | 'deepseek' | 'openai' | 'openrouter' | 'platform_note';
  available?: number;
  voucher?: number;
  cash?: number;
  currency?: string;
  total?: number;
  granted?: number;
  topped_up?: number;
  hard_limit_usd?: number;
  total_usage_usd?: number;
  /** OpenRouter：已用 / 上限（null=无上限）/ 免费层 / key 有效期；/credits 接口的总额度/总已用 */
  usage?: number;
  limit?: number;
  limit_remaining?: number;
  is_free_tier?: boolean;
  expires_at?: string;
  total_credits?: number;
  total_usage?: number;
  /** 无公开余额接口的平台（SenseNova Token Plan / TokenRouter / 阿里云百炼）：官方公开信息 */
  plan?: string;
  quota?: string;
  reset?: string;
  note?: string;
  /** 平台控制台直达链接（查看剩余免费额度等） */
  url?: string;
}

/** 额度页：单个 Provider 的余额查询结果（error: no_key | unavailable）。 */
export interface ProviderBalance {
  id: string;
  name: string;
  /** 最近一次模型探测时间（缓存数据的新旧参考）。 */
  probe_at?: string;
  balance?: Balance;
  error?: string;
}

/** 技能库：单个技能的摘要（Agent Skills SKILL.md frontmatter）。 */
export interface SkillSummary {
  name: string;
  description: string;
  has_scripts: boolean;
  scripts?: string[];
  has_refs: boolean;
  has_assets: boolean;
}

/** 技能库：单个技能详情（SKILL.md 全文）。 */
export interface SkillDetail extends SkillSummary {
  body: string;
  raw: string;
  size: number;
  updated: string;
}

/** MCP：单个 server 的配置 + 连接状态（管理台 /mcps）。 */
export interface McpServer {
  name: string;
  command: string;
  args?: string[];
  env?: Record<string, string>;
  /** 传输方式：""/"stdio"= 本地进程；"http"= Streamable HTTP 远程 */
  transport?: string;
  /** http 传输的 MCP endpoint URL */
  url?: string;
  connected: boolean;
  tools: string[];
  /** 每个 MCP 工具的能力定义（描述 + 参数 schema） */
  tool_details?: { name: string; description: string; input_schema: Record<string, unknown> }[];
}

/** 工具池目录：内置 / 条件 / MCP 工具。 */
export interface ToolInfo {
  name: string;
  description: string;
  source: string; // builtin | builtin-conditional | mcp:<server>
  parameters?: { type?: string; properties?: Record<string, ToolParam>; required?: string[] };
}

export interface ToolParam {
  type?: string;
  description?: string;
  enum?: string[];
}
