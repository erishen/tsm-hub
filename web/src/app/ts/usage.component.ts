import { Component, OnInit, OnDestroy, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService, compact, usd } from './api.service';
import { Agg, DailyPoint, UsageRecord, UsageResponse } from './models';

@Component({
  selector: 'app-usage',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/usage.component.html',
})
export class UsageComponent implements OnInit, OnDestroy {
  readonly data = signal<UsageResponse | null>(null);
  readonly error = signal('');
  readonly loading = signal(true);
  readonly clearing = signal(false);
  days = 7;
  autoRefreshSec = 0;
  private autoTimer: ReturnType<typeof setInterval> | null = null;

  readonly compact = compact;
  readonly usd = usd;

  failTitle(r: UsageRecord): string {
    if (!r.failover?.length) return '发生过 failover，实际第 ' + r.attempt + ' 个候选命中';
    return r.failover.map((f, i) =>
      (i + 1) + '. ' + f.provider_id + (f.model ? ' (' + f.model + ')' : '') + (f.error ? ' — ' + f.error : '')).join('\n');
  }

  failChain(r: UsageRecord): string {
    if (!r.failover?.length) return '';
    return r.failover.map(f => f.provider_id).join(' → ');
  }

  execSummary(r: UsageRecord): string {
    if (!r.exec_tools?.length) return '';
    return r.exec_tools.length === 1 ? r.exec_tools[0] : r.exec_tools.length + ' 个工具';
  }

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.loading.set(true);
    this.api.usage(this.days, 100).subscribe({
      next: (u) => { this.data.set(u); this.loading.set(false); },
      error: (e: Error) => { this.error.set(e.message); this.loading.set(false); },
    });
  }

  /** 清空全部用量流水（含 JSONL 文件与内存聚合），确认后执行并刷新。 */
  clear(): void {
    if (!confirm('确认清空全部用量记录？此操作不可恢复（data/usage/*.jsonl 会被删除）。')) return;
    this.clearing.set(true);
    this.api.clearUsage().subscribe({
      next: () => {
        this.clearing.set(false);
        this.error.set('');
        this.data.set(null);
        this.load();
      },
      error: (e: Error) => {
        this.clearing.set(false);
        this.error.set('清空失败: ' + e.message);
      },
    });
  }

  /** 自动刷新：设置定时器定期调用 load()，关闭时清理。 */
  setupAutoRefresh(): void {
    if (this.autoTimer) { clearInterval(this.autoTimer); this.autoTimer = null; }
    if (this.autoRefreshSec > 0) {
      this.autoTimer = setInterval(() => this.load(), this.autoRefreshSec * 1000);
    }
  }

  /** 导出最近请求为 CSV 文件（时间/模型/状态/Provider/延迟/错误）。 */
  exportCsv(): void {
    const rows = this.data()?.recent || [];
    if (!rows.length) return;
    const header = ['时间', '模型', '状态', 'Provider', '上游模型', 'Prompt Tokens', 'Completion Tokens', '延迟(ms)', '流式', '错误'];
    const lines = [header.join(',')];
    for (const r of rows) {
      const esc = (v: unknown) => '"' + String(v ?? '').replace(/"/g, '""') + '"';
      lines.push([
        esc(r.ts), esc(r.model), esc(r.status), esc(r.provider_id),
        esc(r.upstream_model || ''), esc(r.prompt_tokens || 0),
        esc(r.completion_tokens || 0), esc(r.latency_ms || 0),
        esc(r.stream ? '是' : '否'), esc(r.error || ''),
      ].join(','));
    }
    const blob = new Blob(['\uFEFF' + lines.join('\n')], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `usage_${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  }

  ngOnDestroy(): void {
    if (this.autoTimer) clearInterval(this.autoTimer);
  }

  /** 按天表格只展示有请求/用量/成本的日期，清空后不残留 0 行。 */
  visibleDays(): DailyPoint[] {
    return (this.data()?.days || []).filter(
      (d) => d.requests > 0 || d.total_tokens > 0 || d.cost_usd > 0 || d.errors > 0,
    );
  }

  sum(days: DailyPoint[]): Agg {
    return days.reduce(
      (acc, d) => ({
        requests: acc.requests + d.requests,
        prompt_tokens: acc.prompt_tokens + d.prompt_tokens,
        completion_tokens: acc.completion_tokens + d.completion_tokens,
        total_tokens: acc.total_tokens + d.total_tokens,
        cost_usd: acc.cost_usd + d.cost_usd,
        errors: acc.errors + d.errors,
        failovers: acc.failovers + d.failovers,
        latency_sum_ms: acc.latency_sum_ms + d.latency_sum_ms,
      }),
      { requests: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0, cost_usd: 0, errors: 0, failovers: 0, latency_sum_ms: 0 },
    );
  }

  /** 错误信息模板层截断：DOM 文本本来就短，绝不撑宽；hover title 看全文。 */
  shortErr(e: string): string {
    return e.length > 32 ? e.slice(0, 32) + '…' : e;
  }

  /** Provider → 上游模型 模板层截断（26 字符 + …），title 看全。 */
  providerCell(r: { provider_id: string; upstream_model?: string }): string {
    const s = r.upstream_model ? `${r.provider_id} → ${r.upstream_model}` : r.provider_id;
    return s.length > 26 ? s.slice(0, 26) + '…' : s;
  }

  trackByTs(_: number, r: UsageRecord): string {
    return r.ts;
  }
}
