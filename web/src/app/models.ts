/** 与 Go 端 internal/api 的 JSON 结构保持一致。 */

export interface Agg {
  requests: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cost_usd: number;
  errors: number;
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
  strategy: 'weighted' | 'failover';
  targets: RouteTarget[];
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
  /** 无公开余额接口的平台（SenseNova Token Plan / TokenRouter）：官方公开信息 */
  plan?: string;
  quota?: string;
  reset?: string;
  note?: string;
}

/** 额度页：单个 Provider 的余额查询结果（error: no_key | unavailable）。 */
export interface ProviderBalance {
  id: string;
  name: string;
  balance?: Balance;
  error?: string;
}
