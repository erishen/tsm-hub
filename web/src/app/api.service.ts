import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable, of, forkJoin, throwError } from 'rxjs';
import { catchError, map, mergeMap, tap } from 'rxjs/operators';
import {
  Agg, ApiKey, Balance, Overview, ProbeModel, Provider, ProviderBalance, ProviderHealth, Quota, Route, UsageResponse,
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

  /** 批量余额缓存：5 分钟内命中直接返回，避免每次进入额度页都查询上游。 */
  private balancesCache: { data: ProviderBalance[]; at: number } | null = null;
  private static readonly BALANCES_TTL = 5 * 60 * 1000;

  /** 批量查询所有 Provider 已存 Key 的账户余额/额度（供额度页使用，无 Key 项标记 no_key）。
   *  force=true 强制绕过缓存（刷新按钮）；返回 at=数据获取时间戳。 */
  providerBalances(force = false): Observable<{ balances: ProviderBalance[]; at: number }> {
    const c = this.balancesCache;
    if (!force && c && Date.now() - c.at < ApiService.BALANCES_TTL) {
      return of({ balances: c.data, at: c.at });
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

  listRoutes(): Observable<{ routes: Route[] }> {
    return this.http.get<{ routes: Route[] }>('/api/admin/routes', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  saveRoute(r: Route): Observable<unknown> {
    return this.http.post('/api/admin/routes', r, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  deleteRoute(model: string): Observable<unknown> {
    return this.http.delete(`/api/admin/routes/${encodeURIComponent(model)}`, { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  listKeys(): Observable<{ keys: ApiKey[] }> {
    return this.http.get<{ keys: ApiKey[] }>('/api/admin/keys', { headers: this.headers() })
      .pipe(catchError(this.handleError));
  }

  createKey(body: {
    name: string; models?: string[]; quota?: Partial<Quota>; expires_in_seconds?: number;
  }): Observable<{ id: string; key: string; prefix: string; warning: string }> {
    return this.http.post<{ id: string; key: string; prefix: string; warning: string }>(
      '/api/admin/keys', body, { headers: this.headers() },
    ).pipe(catchError(this.handleError));
  }

  toggleKey(id: string, enabled: boolean): Observable<unknown> {
    return this.http.post(`/api/admin/keys/${encodeURIComponent(id)}/toggle`, { enabled }, { headers: this.headers() })
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

  /**
   * 一键联调：确保存在指向本机 mockupstream（:8799）的 provider，并提供一个调试用 Key。
   * 幂等：已存在的 mock-debug Key 一律先删除再新建，保证同时只有一个，且每次都能拿到明文。
   * 每次调用都会探测本机 mock 的真实模型列表并写回 provider，保证测试页下拉能选到 mock 模型。
   * 调用方需先在本机启动 mockupstream（make mock && ./bin/mockupstream -addr :8799）。
   */
  ensureMockLocal(): Observable<string> {
    return this.listProviders().pipe(
      mergeMap((r): Observable<{ models: string[]; keys: ApiKey[] }> => {
        const existing = (r.providers || []).find((p) => p.id === 'mock-local');
        // 探测本机 mock 模型；mock 未启动时回退到已知模型名，避免联调不可用。
        return this.probeModels({ base_url: 'http://localhost:8799/v1' }).pipe(
          catchError(() => of({ models: [] })),
          mergeMap((pr): Observable<{ models: string[]; keys: ApiKey[] }> => {
            const models = pr.models && pr.models.length ? pr.models.map((m) => m.id) : ['mock-model', 'mock-extra'];
            const prov$: Observable<unknown> = existing
              ? this.saveProvider({ ...existing, models })
              : this.saveProvider({
                  id: 'mock-local',
                  name: 'Mock 本地联调',
                  base_url: 'http://localhost:8799/v1',
                  models,
                  weight: 1,
                  priority: 1,
                  timeout_ms: 10000,
                  enabled: true,
                });
            return prov$.pipe(mergeMap((): Observable<{ models: string[]; keys: ApiKey[] }> =>
              this.listKeys().pipe(map((kr) => ({ models, keys: kr.keys || [] })))));
          }),
        );
      }),
      mergeMap((kr): Observable<{ id: string; key: string; prefix: string; warning: string }> => {
        const olds = (kr.keys || []).filter((k) => k.name === 'mock-debug');
        const del$: Observable<unknown> = olds.length
          ? forkJoin(olds.map((k) => this.deleteKey(k.id)))
          : of(null);
        return del$.pipe(mergeMap(() => this.createKey({ name: 'mock-debug', models: ['*'] })));
      }),
      map((r) => r.key),
      catchError(this.handleError),
    );
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
  };
}
