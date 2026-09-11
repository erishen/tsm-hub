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
  templateUrl: '../html/dashboard.component.html',
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
