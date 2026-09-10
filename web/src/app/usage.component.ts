import { Component, OnInit, OnDestroy, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService, compact, usd } from './api.service';
import { Agg, DailyPoint, UsageRecord, UsageResponse } from './models';

@Component({
  selector: 'app-usage',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>用量</h1>
        <div class="sub">流水落盘在 data/usage/YYYY-MM-DD.jsonl</div>
      </div>
      <div class="inline-form">
        <div style="flex:0 0 110px">
          <label>天数</label>
          <select [(ngModel)]="days" (ngModelChange)="load()">
            <option [ngValue]="7">7 天</option>
            <option [ngValue]="14">14 天</option>
            <option [ngValue]="30">30 天</option>
          </select>
        </div>
        <div style="flex:0 0 110px">
          <label>自动刷新</label>
          <select [(ngModel)]="autoRefreshSec" (ngModelChange)="setupAutoRefresh()">
            <option [ngValue]="0">关闭</option>
            <option [ngValue]="5">5 秒</option>
            <option [ngValue]="10">10 秒</option>
            <option [ngValue]="30">30 秒</option>
          </select>
        </div>
        <button (click)="load()">刷新</button>
        <button (click)="exportCsv()" [disabled]="!data()?.recent?.length">导出 CSV</button>
        <button class="danger" (click)="clear()" [disabled]="clearing()">{{ clearing() ? '清空中…' : '清空' }}</button>
      </div>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="page-loading" *ngIf="loading()">加载中…</div>
    <div class="grid" *ngIf="data()">
      <div class="stat">
        <div class="label">区间 Tokens</div>
        <div class="value">{{ compact(sum(data()!.days).total_tokens) }}</div>
      </div>
      <div class="stat">
        <div class="label">区间请求</div>
        <div class="value">{{ sum(data()!.days).requests }}</div>
      </div>
      <div class="stat">
        <div class="label">区间成本</div>
        <div class="value">{{ usd(sum(data()!.days).cost_usd) }}</div>
      </div>
      <div class="stat">
        <div class="label">错误数</div>
        <div class="value">{{ sum(data()!.days).errors }}</div>
      </div>
    </div>

    <div class="card">
      <h2>按天</h2>
      <table *ngIf="visibleDays().length; else none">
        <thead>
          <tr>
            <th>日期</th><th class="num">请求</th><th class="num">Prompt</th>
            <th class="num">Completion</th><th class="num">Tokens</th>
            <th class="num">成本</th><th class="num">错误</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let d of visibleDays()">
            <td class="mono">{{ d.date }}</td>
            <td class="num">{{ d.requests }}</td>
            <td class="num">{{ d.prompt_tokens }}</td>
            <td class="num">{{ d.completion_tokens }}</td>
            <td class="num">{{ d.total_tokens }}</td>
            <td class="num">{{ usd(d.cost_usd) }}</td>
            <td class="num">{{ d.errors }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #none><div class="empty">暂无数据</div></ng-template>
    </div>

    <div class="card">
      <h2>按模型</h2>
      <table *ngIf="data() && data()!.models.length; else none2">
        <thead>
          <tr><th>模型</th><th class="num">请求</th><th class="num">Tokens</th><th class="num">成本</th></tr>
        </thead>
        <tbody>
          <tr *ngFor="let m of data()!.models">
            <td class="mono">{{ m.model }}</td>
            <td class="num">{{ m.usage.requests }}</td>
            <td class="num">{{ m.usage.total_tokens }}</td>
            <td class="num">{{ usd(m.usage.cost_usd) }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #none2><div class="empty">暂无数据</div></ng-template>
    </div>

    <div class="card">
      <h2>按 Key</h2>
      <table *ngIf="data() && data()!.keys.length; else none3">
        <thead>
          <tr><th>Key ID</th><th class="num">请求</th><th class="num">Tokens</th><th class="num">成本</th></tr>
        </thead>
        <tbody>
          <tr *ngFor="let k of data()!.keys">
            <td class="mono">{{ k.key_id }}</td>
            <td class="num">{{ k.usage.requests }}</td>
            <td class="num">{{ k.usage.total_tokens }}</td>
            <td class="num">{{ usd(k.usage.cost_usd) }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #none3><div class="empty">暂无数据</div></ng-template>
    </div>

    <div class="card">
      <h2>最近请求 <span class="muted" style="font-weight:400;font-size:12px">（{{ data()?.recent?.length ?? 0 }} 条，倒序）</span></h2>
      <table *ngIf="data() && data()!.recent.length; else none4">
        <thead>
          <tr>
            <th>时间</th><th>Key</th><th>模型</th><th>Provider</th>
            <th class="num">Tokens</th><th class="num">延迟</th><th>流式</th><th>状态</th><th>归因</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let r of data()!.recent"
              [class.err-row]="r.status >= 400 || !!r.error">
            <td class="mono nowrap">{{ r.ts | date:'MM-dd HH:mm:ss' }}</td>
            <td class="mono nowrap">{{ r.key_id }}</td>
            <td class="mono nowrap" style="max-width:200px" [title]="r.model">
              <ng-container *ngIf="r.model === '*'">* <span class="muted" style="font-size:12px">通配</span></ng-container>
              <ng-container *ngIf="r.model !== '*'">{{ r.model }}</ng-container>
            </td>
            <td class="mono muted nowrap" style="max-width:250px"
                [title]="r.provider_id + (r.upstream_model ? ' → ' + r.upstream_model : '')">
              {{ providerCell(r) }}
            </td>
            <td class="num nowrap">{{ r.total_tokens }}</td>
            <td class="num nowrap" [class.slow]="r.latency_ms >= 3000">{{ r.latency_ms }} ms</td>
            <td class="nowrap">{{ r.stream ? '是' : '否' }}</td>
            <td class="nowrap">
              <span class="badge" [class.bad]="r.status >= 400 || !!r.error" [class.ok]="r.status < 400 && !r.error">
                {{ r.status }}
              </span>
              <span class="muted" style="font-size:12px;margin-left:8px;vertical-align:middle"
                    [title]="r.error" *ngIf="r.error">{{ shortErr(r.error) }}</span>
            </td>
            <td class="nowrap" style="max-width:260px">
              <span class="badge ok" *ngIf="r.fastpath" title="确定性快路径">⚡ {{ r.fastpath }}</span>
              <span class="badge" *ngIf="r.scene" [title]="'场景: ' + r.scene">{{ r.scene }}</span>
              <span *ngIf="r.attempt && r.attempt > 1">
                <span class="badge warn" [title]="failTitle(r)" style="cursor:help">↻{{ r.attempt }}</span>
                <span class="muted" style="font-size:12px" *ngIf="r.failover?.length"
                      [title]="failTitle(r)">{{ failChain(r) }}</span>
              </span>
              <span class="badge" *ngIf="r.exec_tools?.length" style="margin-left:2px"
                    [title]="'网关执行的工具: ' + r.exec_tools!.join(', ')">⚙ {{ execSummary(r) }}</span>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none4><div class="empty">暂无流水</div></ng-template>
    </div>
  `,
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
