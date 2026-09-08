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

/** 探测返回的模型元信息：上下文窗口（token 总量）与免费标记。 */
export interface ProbeModel {
  id: string;
  context_length?: number;
  free?: boolean;
}
