import { Component, OnInit, signal } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService, compact, usd } from './api.service';
import { AggView, DailyPoint, ObservabilityResponse } from './models';

function pct(n: number): string {
  return (n * 100).toFixed(1) + '%';
}

function ms(n: number): string {
  if (n <= 0) return '-';
  return n < 1000 ? n + ' ms' : (n / 1000).toFixed(2) + ' s';
}

function rateClass(rate: number): string {
  if (rate >= 0.3) return 'badge err';
  if (rate >= 0.05) return 'badge warn';
  return 'badge ok';
}

@Component({
  selector: 'app-observability',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/observability.component.html',
})
export class ObservabilityComponent implements OnInit, OnDestroy {
  days = 14;
  data = signal<ObservabilityResponse | null>(null);
  error = signal('');
  readonly loading = signal(true);
  Math = Math;

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.adoptTarget()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.error.set('');
    this.loading.set(true);
    this.api.observability(this.days).subscribe({
      next: (d) => { this.data.set(d); this.loading.set(false); },
      error: (e) => { this.error.set(e?.message || '加载失败'); this.loading.set(false); },
    });
  }

  /** 按天趋势只展示有数据的日期，清空后不残留 0 行。 */
  visibleTrend(): DailyPoint[] {
    return (this.data()?.trend || []).filter(
      (d) => d.requests > 0 || d.total_tokens > 0 || d.cost_usd > 0 || d.errors > 0,
    );
  }

  /** 导出按天趋势为 CSV（日期/请求/错误/Prompt Tokens/Completion Tokens/成本/平均延迟）。 */
  exportCsv(): void {
    const rows = this.visibleTrend();
    if (!rows.length) return;
    const header = ['日期', '请求数', '错误数', 'Prompt Tokens', 'Completion Tokens', '总成本(USD)', '平均延迟(ms)'];
    const lines = [header.join(',')];
    for (const d of rows) {
      const avgLat = d.requests > 0 ? Math.round(d.latency_sum_ms / d.requests) : 0;
      lines.push([d.date, d.requests, d.errors, d.prompt_tokens, d.completion_tokens, d.cost_usd.toFixed(6), avgLat].join(','));
    }
    const blob = new Blob(['\uFEFF' + lines.join('\n')], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `observability_${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  }

  // ---- 外部工具/技能录用 ----
  adoptTarget = signal<{ name: string; calls?: number; key_count?: number; adopted: boolean; kind?: string } | null>(null);
  adoptForm = { description: '', kind: 'tool', impl_type: 'none', impl_source: '' };
  adopting = signal(false);

  openAdopt(t: { name: string; calls?: number; key_count?: number; adopted: boolean; kind?: string }): void {
    this.adoptTarget.set(t);
    this.adoptForm = { description: '', kind: t.kind === 'skill' ? 'skill' : 'tool', impl_type: 'none', impl_source: '' };
  }

  closeAdopt(): void {
    if (this.adopting()) return;
    this.adoptTarget.set(null);
  }

  adopt(): void {
    const t = this.adoptTarget();
    if (!t) return;
    this.adopting.set(true);
    this.api.adoptExternalTool(t.name, {
      description: this.adoptForm.description,
      kind: this.adoptForm.kind,
      impl_type: this.adoptForm.kind === 'skill' ? 'none' : this.adoptForm.impl_type,
      impl_source: this.adoptForm.impl_source || undefined,
    }).subscribe({
      next: () => { this.adopting.set(false); this.adoptTarget.set(null); this.load(); },
      error: (e: Error) => { this.adopting.set(false); this.error.set('录用失败：' + e.message); },
    });
  }

  unadopt(t: { name: string }): void {
    if (!confirm('取消录用 ' + t.name + '？')) return;
    this.api.deleteExternalTool(t.name).subscribe({
      next: () => this.load(),
      error: (e: Error) => this.error.set('取消录用失败：' + e.message),
    });
  }

  pct = pct;
  ms = ms;
  rateClass = rateClass;
  compact = compact;
  usd = usd;
}
