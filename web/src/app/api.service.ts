import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable, of, throwError } from 'rxjs';
import { catchError, map, mergeMap } from 'rxjs/operators';
import {
  Agg, ApiKey, Overview, Provider, ProviderHealth, Quota, Route, UsageResponse,
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
   * 一键联调：确保存在指向本机 mockupstream（:8799）的 provider，并创建一个调试用 Key。
   * 返回明文 Key（仅此一次）。调用方需先在本机启动 mockupstream（make mock && ./bin/mockupstream -addr :8799）。
   */
  ensureMockLocal(): Observable<string> {
    return this.listProviders().pipe(
      mergeMap((r) => {
        const exists = (r.providers || []).some((p) => p.id === 'mock-local');
        const prov$ = exists
          ? of(null)
          : this.saveProvider({
              id: 'mock-local',
              name: 'Mock 本地联调',
              base_url: 'http://localhost:8799/v1',
              models: ['*'],
              weight: 1,
              priority: 1,
              timeout_ms: 10000,
              enabled: true,
            });
        return prov$.pipe(mergeMap(() => this.createKey({ name: 'mock-debug', models: ['*'] })));
      }),
      map((r: { key: string }) => r.key),
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
