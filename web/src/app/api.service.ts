import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable, of, throwError } from 'rxjs';
import { catchError, map, tap } from 'rxjs/operators';
import {
  Agg, ApiKey, Balance, CatalogModel, McpServer, ObservabilityResponse, Overview, ProbeModel, Provider, ProviderBalance, ProviderHealth, Quota, RecommendationsResp, Route, SkillDetail, SkillSummary, ToolInfo, UsageResponse,
} from './models';

const SESSION_KEY = 'llm-router.session';

/** 统一封装管理 API：自动带上会话 token，401 时清空登录态。 */
@Injectable({ providedIn: 'root' })
export class ApiService {
  readonly session = signal<string | null>(localStorage.getItem(SESSION_KEY));
  readonly lastError = signal<string>('');

  constructor(private http: HttpClient) {}

  get loggedIn(): boolean {
    return !!this.session();
  }

  private headers(): Record<string, string> {
    const s = this.session();
    return s ? { 'X-Session-Token': s } : {};
  }

  private handleError = (err: any): Observable<never> => {
    const msg = err?.error?.error?.message || err?.message || '请求失败';
    if (err?.status === 401) {
      this.logout();
    }
    this.lastError.set(msg);
    return throwError(() => new Error(msg));
  };

  login(token: string): Observable<{ session_token: string }> {
    return this.http
      .post<{ session_token: string }>('/api/admin/login', { token })
      .pipe(catchError(this.handleError));
  }

  saveSession(token: string): void {
    localStorage.setItem(SESSION_KEY, token);
    this.session.set(token);
  }

  logout(): void {
    localStorage.removeItem(SESSION_KEY);
    this.session.set(null);
  }

  overview(): Observable<Overview> {
    return this.http.get<Overview>('/api/admin/overview', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listProviders(): Observable<{ providers: Provider[] }> {
    return this.http.get<{ providers: Provider[] }>('/api/admin/providers', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  saveProvider(p: Partial<Provider>): Observable<unknown> {
    return this.http.post('/api/admin/providers', p, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteProvider(id: string): Observable<unknown> {
    return this.http.delete(`/api/admin/providers/${encodeURIComponent(id)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  /** 批量余额缓存（localStorage 持久化，刷新页面后仍生效）：进入页面默认只读缓存，
   *  不发起查询；点「刷新」才强制查询并更新缓存。 */
  private static readonly BALANCES_KEY = 'llm-router.balances.cache.v1';
  private get balancesCache(): { data: ProviderBalance[]; at: number } | null {
    try {
      const raw = localStorage.getItem(ApiService.BALANCES_KEY);
      return raw ? JSON.parse(raw) : null;
    } catch {
      return null;
    }
  }
  private set balancesCache(v: { data: ProviderBalance[]; at: number } | null) {
    try {
      if (v) localStorage.setItem(ApiService.BALANCES_KEY, JSON.stringify(v));
      else localStorage.removeItem(ApiService.BALANCES_KEY);
    } catch {
      /* localStorage 不可用时忽略，仅影响缓存 */
    }
  }

  /** 批量查询所有 Provider 已存 Key 的账户余额/额度（供额度页使用，无 Key 项标记 no_key）。
   *  force=false 只读缓存：有缓存返回缓存、无缓存返回空（at=0，不请求上游）；
   *  force=true 强制查询并更新缓存（刷新按钮）。返回 at=数据获取时间戳。 */
  providerBalances(force = false): Observable<{ balances: ProviderBalance[]; at: number }> {
    const c = this.balancesCache;
    if (!force) {
      if (c) return of({ balances: c.data, at: c.at });
      return of({ balances: [], at: 0 });
    }
    return this.http.get<{ balances: ProviderBalance[] }>('/api/admin/providers/balances', { headers: this.headers() })
      .pipe(
        catchError(this.handleError),
        tap((r) => {
          this.balancesCache = { data: r.balances ?? [], at: Date.now() };
        }),
        map((r) => ({ balances: r.balances ?? [], at: Date.now() })),
      );
  }

  /** 用 base_url + API Key 探测上游 /v1/models，返回模型列表（含上下文窗口、免费标记）与账户余额。 */
  probeModels(body: { base_url: string; api_key?: string }): Observable<{ models: ProbeModel[]; balance?: Balance }> {
    return this.http.post<{ models: ProbeModel[]; balance?: Balance }>('/api/admin/providers/probe', body, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  /** 模型目录：所有 Provider 已配置模型的归类/用途/免费/定价聚合；probe_at=最近探测时间。 */
  modelsCatalog(): Observable<{ models: CatalogModel[]; probe_at?: string }> {
    return this.http.get<{ models: CatalogModel[]; probe_at?: string }>('/api/admin/models/catalog', { headers: this.headers() });
  }

  /** 应用开发场景推荐（根据当前模型池能力） */
  getModelRecommendations(): Observable<RecommendationsResp> {
    return this.http.get<RecommendationsResp>('/api/admin/models/recommendations', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  /** 重新探测所有已配置 Key 的 Provider，刷新免费/价格快照后返回最新目录。 */
  refreshModels(): Observable<{ models: CatalogModel[]; probe_at?: string; providers?: Record<string, string> }> {
    return this.http.post<{ models: CatalogModel[]; probe_at?: string; providers?: Record<string, string> }>(
      '/api/admin/models/refresh', {}, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listRoutes(): Observable<{ routes: Route[] }> {
    return this.http.get<{ routes: Route[] }>('/api/admin/routes', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  saveRoute(r: Route): Observable<unknown> {
    return this.http.post('/api/admin/routes', r, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteRoute(model: string): Observable<unknown> {
    const m = model || '_default'; // 通配兜底路由（空 model）用 _default 哨兵
    return this.http.delete(`/api/admin/routes/${encodeURIComponent(m)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listSkills(): Observable<{ dir: string; skills: SkillSummary[] }> {
    return this.http.get<{ dir: string; skills: SkillSummary[] }>('/api/admin/skills', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  getSkill(name: string): Observable<SkillDetail> {
    return this.http.get<SkillDetail>(`/api/admin/skills/${encodeURIComponent(name)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listMcps(): Observable<{ mcps: McpServer[] }> {
    return this.http.get<{ mcps: McpServer[] }>('/api/admin/mcps', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  saveMcp(name: string, body: { command: string; args?: string[]; env?: Record<string, string>; transport?: string; url?: string }): Observable<unknown> {
    return this.http.post(`/api/admin/mcps/${encodeURIComponent(name)}`, body, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteMcp(name: string): Observable<unknown> {
    return this.http.delete(`/api/admin/mcps/${encodeURIComponent(name)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listTools(): Observable<{ tools: ToolInfo[] }> {
    return this.http.get<{ tools: ToolInfo[] }>('/api/admin/tools', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  getFastpath(): Observable<{ builtin: any[]; plugins: any[] }> {
    return this.http.get<{ builtin: any[]; plugins: any[] }>('/api/admin/fastpath', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  promoteFastpath(name: string, mode: string = 'fastpath'): Observable<unknown> {
    return this.http.post(`/api/admin/fastpath/${encodeURIComponent(name)}/promote`, { mode }, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  adoptExternalTool(name: string, body: { description?: string; kind?: string; impl_type: string; impl_source?: string }): Observable<unknown> {
    return this.http.post(`/api/admin/external-tools/${encodeURIComponent(name)}/adopt`, body, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  externalSkillCandidates(): Observable<{ candidates: { name: string; calls: number; key_count: number; adopted: boolean; description?: string; adopted_at?: string; kind?: string }[] }> {
    return this.http.get<{ candidates: { name: string; calls: number; key_count: number; adopted: boolean; description?: string; adopted_at?: string; kind?: string }[] }>('/api/admin/external-skills/candidates', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteExternalTool(name: string): Observable<unknown> {
    return this.http.delete(`/api/admin/external-tools/${encodeURIComponent(name)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  externalMcpCandidates(): Observable<{ candidates: { server: string; calls: number; key_count: number; tools: { name: string; calls: number }[] }[] }> {
    return this.http.get<{ candidates: { server: string; calls: number; key_count: number; tools: { name: string; calls: number }[] }[] }>('/api/admin/external-mcps/candidates', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  adoptExternalMcp(server: string, body: { transport: string; url?: string; command?: string; args?: string[]; env?: Record<string, string> }): Observable<unknown> {
    return this.http.post(`/api/admin/external-mcps/${encodeURIComponent(server)}/adopt`, body, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  suggestExternalMcp(server: string, tools: { name: string; calls: number }[]): Observable<{ suggestion: { transport: string; command: string; args: string[]; url: string; env_hint: string; notes: string } }> {
    return this.http.post<{ suggestion: { transport: string; command: string; args: string[]; url: string; env_hint: string; notes: string } }>('/api/admin/external-mcps/suggest', { server, tools }, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  sandboxStatus(): Observable<{ enabled: boolean; docker_ok: boolean; timeout_sec: number; memory_mb: number; cpus: number; max_output_kb: number; languages: string[] }> {
    return this.http.get<{ enabled: boolean; docker_ok: boolean; timeout_sec: number; memory_mb: number; cpus: number; max_output_kb: number; languages: string[] }>('/api/admin/sandbox/status', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listMemory(): Observable<{ entries: { ns: string; value: string }[] }> {
    return this.http.get<{ entries: { ns: string; value: string }[] }>('/api/admin/memory', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  clearMemory(): Observable<unknown> {
    return this.http.delete('/api/admin/memory', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteFastpath(name: string): Observable<unknown> {
    return this.http.delete(`/api/admin/fastpath/${encodeURIComponent(name)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  fastpathGenerate(query: string): Observable<{ answer: string; method: string }> {
    return this.http.post<{ answer: string; method: string }>('/api/admin/fastpath/generate', { query }, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  invokeTool(name: string, args: Record<string, unknown>): Observable<{ result: string }> {
    return this.http.post<{ result: string }>('/api/admin/tools/invoke', { name, args }, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listKeys(): Observable<{ keys: ApiKey[] }> {
    return this.http.get<{ keys: ApiKey[] }>('/api/admin/keys', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  createKey(body: {
    name: string; models?: string[]; quota?: Partial<Quota>; expires_in_seconds?: number;
    inject_skills?: string;
  }): Observable<{ id: string; key: string; prefix: string; warning: string }> {
    return this.http.post<{ id: string; key: string; prefix: string; warning: string }>(
      '/api/admin/keys', body, { headers: this.headers() },
    ).pipe(catchError(this.handleError));
  }

  /** 短窗口内补看新建 key 的明文（创建后 2 分钟）。 */
  revealKey(id: string): Observable<{ key: string }> {
    return this.http.get<{ key: string }>(`/api/admin/keys/${encodeURIComponent(id)}/plaintext`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  toggleKey(id: string, enabled: boolean): Observable<unknown> {
    return this.http.post(`/api/admin/keys/${encodeURIComponent(id)}/toggle`, { enabled }, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  updateKey(id: string, body: {
    name?: string; models?: string[]; quota?: Partial<Quota>; inject_skills?: string;
  }): Observable<unknown> {
    return this.http.patch(`/api/admin/keys/${encodeURIComponent(id)}`, body, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteKey(id: string): Observable<unknown> {
    return this.http.delete(`/api/admin/keys/${encodeURIComponent(id)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  usage(days = 7, limit = 50): Observable<UsageResponse> {
    return this.http.get<UsageResponse>(`/api/admin/usage?days=${days}&limit=${limit}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  clearUsage(): Observable<{ ok: boolean; cleared: boolean }> {
    return this.http.post<{ ok: boolean; cleared: boolean }>('/api/admin/usage/clear', {}, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  observability(days = 14): Observable<ObservabilityResponse> {
    return this.http.get<ObservabilityResponse>(`/api/admin/observability/overview?days=${days}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  health(): Observable<{ providers: ProviderHealth[] }> {
    return this.http.get<{ providers: ProviderHealth[] }>('/api/admin/health', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }
}

/** 数字格式化：1_234_567 → 1.23M */
export function compact(n: number): string {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return String(n);
}

export function usd(n: number): string {
  if (!n) return '$0';
  if (n < 0.01) return '$' + n.toFixed(4);
  return '$' + n.toFixed(2);
}

export function emptyAgg(): Agg {
  return {
    requests: 0, prompt_tokens: 0, completion_tokens: 0,
    total_tokens: 0, cost_usd: 0, errors: 0,
    failovers: 0, latency_sum_ms: 0,
  };
}
