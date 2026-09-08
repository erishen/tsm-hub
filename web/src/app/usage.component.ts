import { Component, OnInit, signal } from '@angular/core';
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
        <button (click)="load()">刷新</button>
      </div>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

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
      <table *ngIf="data() && data()!.days.length; else none">
        <thead>
          <tr>
            <th>日期</th><th class="num">请求</th><th class="num">Prompt</th>
            <th class="num">Completion</th><th class="num">Tokens</th>
            <th class="num">成本</th><th class="num">错误</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let d of data()!.days">
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
      <h2>最近请求</h2>
      <table *ngIf="data() && data()!.recent.length; else none4">
        <thead>
          <tr>
            <th>时间</th><th>Key</th><th>模型</th><th>Provider</th>
            <th class="num">Tokens</th><th class="num">延迟</th><th>流式</th><th>状态</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let r of data()!.recent">
            <td class="mono">{{ r.ts | date:'MM-dd HH:mm:ss' }}</td>
            <td class="mono">{{ r.key_id }}</td>
            <td class="mono">{{ r.model }}</td>
            <td class="mono muted">{{ r.provider_id }}<span *ngIf="r.upstream_model"> → {{ r.upstream_model }}</span></td>
            <td class="num">{{ r.total_tokens }}</td>
            <td class="num">{{ r.latency_ms }} ms</td>
            <td>{{ r.stream ? '是' : '否' }}</td>
            <td>
              <span class="badge" [class.bad]="r.status >= 400 || !!r.error" [class.ok]="r.status < 400 && !r.error">
                {{ r.status }}
              </span>
              <div class="muted" style="font-size:12px" *ngIf="r.error">{{ r.error }}</div>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none4><div class="empty">暂无流水</div></ng-template>
    </div>
  `,
})
export class UsageComponent implements OnInit {
  readonly data = signal<UsageResponse | null>(null);
  readonly error = signal('');
  days = 7;

  readonly compact = compact;
  readonly usd = usd;

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.api.usage(this.days, 100).subscribe({
      next: (u) => this.data.set(u),
      error: (e: Error) => this.error.set(e.message),
    });
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
      }),
      { requests: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0, cost_usd: 0, errors: 0 },
    );
  }

  trackByTs(_: number, r: UsageRecord): string {
    return r.ts;
  }
}
