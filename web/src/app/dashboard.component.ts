import { Component, OnInit, OnDestroy, computed, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { RouterLink } from '@angular/router';
import { ApiService, compact, emptyAgg, usd } from './api.service';
import { Agg, DailyPoint, Overview, ProviderHealth } from './models';

@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [CommonModule, FormsModule, RouterLink],
  template: `
    <div class="page-head">
      <div>
        <h1>概览</h1>
        <div class="sub">网关运行状态与今日用量</div>
      </div>
      <div class="inline-form">
        <div style="flex:0 0 110px">
          <label>自动刷新</label>
          <select [(ngModel)]="autoRefreshSec" (ngModelChange)="setupAutoRefresh()">
            <option [ngValue]="0">关闭</option>
            <option [ngValue]="5">5 秒</option>
            <option [ngValue]="10">10 秒</option>
            <option [ngValue]="30">30 秒</option>
          </select>
        </div>
        <button class="small" (click)="load()">刷新</button>
      </div>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>
    <div class="page-loading" *ngIf="loading()">加载中…</div>

    <div class="grid">
      <div class="stat">
        <div class="label">Providers</div>
        <div class="value">{{ overview()?.healthy ?? 0 }} / {{ overview()?.providers ?? 0 }}</div>
        <div class="hint">健康 / 总数</div>
      </div>
      <div class="stat">
        <div class="label">路由规则</div>
        <div class="value">{{ overview()?.routes ?? 0 }}</div>
        <div class="hint">对外模型别名</div>
      </div>
      <div class="stat">
        <div class="label">Token Keys</div>
        <div class="value">{{ overview()?.keys ?? 0 }}</div>
        <div class="hint">已签发</div>
      </div>
      <div class="stat">
        <div class="label">今日 Tokens</div>
        <div class="value">{{ compact(total().total_tokens) }}</div>
        <div class="hint">{{ total().requests }} 次请求</div>
      </div>
      <div class="stat">
        <div class="label">今日成本</div>
        <div class="value">{{ usd(total().cost_usd) }}</div>
        <div class="hint">累计 {{ usd(overview()?.total?.cost_usd ?? 0) }}</div>
      </div>
      <div class="stat">
        <div class="label">错误率</div>
        <div class="value">{{ errorRate() }}%</div>
        <div class="hint">{{ total().errors }} / {{ total().requests }}</div>
      </div>
    </div>

    <div class="card" style="margin-top:18px">
      <h2>近 7 日 Tokens</h2>
      <div class="bar-chart" *ngIf="days().length; else noData">
        <div class="bar-col" *ngFor="let d of days()">
          <div class="bar" [style.height.%]="barHeight(d)"></div>
          <div class="bar-label">{{ d.date.slice(5) }}</div>
        </div>
      </div>
      <ng-template #noData><div class="empty">暂无用量数据</div></ng-template>
    </div>

    <div class="card">
      <h2>Provider 实时健康</h2>
      <table *ngIf="health().length; else noProvider">
        <thead>
          <tr>
            <th>Provider</th><th>状态</th><th class="num">延迟</th>
            <th class="num">请求</th><th class="num">失败</th><th>最近错误</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let h of health()">
            <td class="mono">{{ h.provider_id }}</td>
            <td>
              <span class="badge" [class.ok]="h.healthy" [class.bad]="!h.healthy">
                {{ h.healthy ? 'healthy' : 'degraded' }}
              </span>
            </td>
            <td class="num">{{ h.latency_ms }} ms</td>
            <td class="num">{{ h.requests }}</td>
            <td class="num">{{ h.errors }}</td>
            <td class="muted">{{ h.last_error || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #noProvider><div class="empty">还没有 Provider，先去 <a routerLink="/providers">添加</a></div></ng-template>
    </div>
  `,
})
export class DashboardComponent implements OnInit, OnDestroy {
  readonly overview = signal<Overview | null>(null);
  readonly days = signal<DailyPoint[]>([]);
  readonly health = signal<ProviderHealth[]>([]);
  readonly error = signal('');
  readonly loading = signal(true);
  autoRefreshSec = 0;
  private autoTimer: ReturnType<typeof setInterval> | null = null;

  readonly compact = compact;
  readonly usd = usd;

  readonly total = computed<Agg>(() => this.overview()?.today ?? emptyAgg());

  readonly errorRate = computed(() => {
    const t = this.total();
    if (!t.requests) return '0';
    return ((t.errors / t.requests) * 100).toFixed(1);
  });

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  // 多请求计数：全部完成后才收起 loading。
  private pending = 0;
  private begin(): void { this.pending++; this.loading.set(true); }
  private end(): void { if (--this.pending <= 0) { this.pending = 0; this.loading.set(false); } }

  load(): void {
    this.error.set('');
    this.begin();
    this.api.overview().subscribe({
      next: (o) => { this.overview.set(o); this.end(); },
      error: (e: Error) => { this.error.set(e.message); this.end(); },
    });
    this.begin();
    this.api.usage(7, 10).subscribe({
      next: (u) => { this.days.set(u.days); this.end(); },
      error: () => this.end(),
    });
    this.begin();
    this.api.health().subscribe({
      next: (h) => { this.health.set(h.providers ?? []); this.end(); },
      error: () => this.end(),
    });
  }

  barHeight(d: DailyPoint): number {
    const max = Math.max(...this.days().map((x) => x.total_tokens), 1);
    return Math.max((d.total_tokens / max) * 100, 2);
  }

  /** 自动刷新：设置定时器定期调用 load()，关闭时清理。 */
  setupAutoRefresh(): void {
    if (this.autoTimer) { clearInterval(this.autoTimer); this.autoTimer = null; }
    if (this.autoRefreshSec > 0) {
      this.autoTimer = setInterval(() => this.load(), this.autoRefreshSec * 1000);
    }
  }

  ngOnDestroy(): void {
    if (this.autoTimer) clearInterval(this.autoTimer);
  }
}
