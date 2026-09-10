import { Component, OnInit, signal } from '@angular/core';
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
  template: `
    <div class="page-head">
      <div>
        <h1>监控</h1>
        <div class="sub">请求归因 · 失败率 · 延迟 · 成本 · failover（数据来自用量聚合，不触发上游查询）</div>
      </div>
      <div class="inline-form">
        <div style="flex:0 0 110px">
          <label>区间</label>
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
        <div class="label">今日请求</div>
        <div class="value">{{ data()!.today.requests }}</div>
        <div class="hint">错误率 {{ pct(data()!.today.error_rate) }} · 平均 {{ ms(data()!.today.avg_latency_ms) }}</div>
      </div>
      <div class="stat">
        <div class="label">今日成本</div>
        <div class="value">{{ usd(data()!.today.cost_usd) }}</div>
        <div class="hint">Tokens {{ compact(data()!.today.total_tokens) }}</div>
      </div>
      <div class="stat">
        <div class="label">近 7 天请求</div>
        <div class="value">{{ data()!.week.requests }}</div>
        <div class="hint">failover {{ data()!.week.failovers }} 次</div>
      </div>
      <div class="stat">
        <div class="label">近 7 天错误率</div>
        <div class="value">{{ pct(data()!.week.error_rate) }}</div>
        <div class="hint">错误 {{ data()!.week.errors }} / {{ data()!.week.requests }}</div>
      </div>
      <div class="stat">
        <div class="label">近 {{ days }} 天请求</div>
        <div class="value">{{ data()!.month.requests }}</div>
        <div class="hint">成本 {{ usd(data()!.month.cost_usd) }}</div>
      </div>
      <div class="stat">
        <div class="label">近 {{ days }} 天平均延迟</div>
        <div class="value">{{ ms(data()!.month.avg_latency_ms) }}</div>
        <div class="hint">failover {{ data()!.month.failovers }} 次</div>
      </div>
    </div>

    <div class="card">
      <h2>Provider 归因 <span class="muted" style="font-weight:400;font-size:12px">（实际请求走了谁 / 错误率 / 延迟）</span></h2>
      <table *ngIf="data() && data()!.providers.length; else none">
        <thead>
          <tr>
            <th>Provider</th><th class="num">请求</th><th class="num">错误率</th>
            <th class="num">平均延迟</th><th class="num">Tokens</th>
            <th class="num">成本</th><th class="num">failover</th><th class="num">被跳过</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let p of data()!.providers">
            <td>{{ p.name }} <span class="muted mono" style="font-size:11px">{{ p.id }}</span></td>
            <td class="num">{{ p.usage.requests }}</td>
            <td class="num"><span [class]="rateClass(p.usage.error_rate)">{{ pct(p.usage.error_rate) }}</span></td>
            <td class="num">{{ ms(p.usage.avg_latency_ms) }}</td>
            <td class="num">{{ compact(p.usage.total_tokens) }}</td>
            <td class="num">{{ usd(p.usage.cost_usd) }}</td>
            <td class="num">{{ p.usage.failovers }}</td>
            <td class="num">
              <span [class.bad]="p.skipped > 0" *ngIf="p.skipped" class="badge" title="作为 failover 失败候选被跳过的次数（稳定性反向指标）">{{ p.skipped }}</span>
              <span *ngIf="!p.skipped" class="muted">0</span>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none><div class="empty">暂无数据</div></ng-template>
    </div>

    <div class="card">
      <h2>场景分布 <span class="muted" style="font-weight:400;font-size:12px">（auto 智能分流命中统计）</span></h2>
      <table *ngIf="data() && data()!.scenes.length; else none2">
        <thead>
          <tr>
            <th>场景</th><th class="num">请求</th><th class="num">错误率</th>
            <th class="num">平均延迟</th><th class="num">Tokens</th><th class="num">成本</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let s of data()!.scenes">
            <td><span class="badge ok">{{ s.scene }}</span></td>
            <td class="num">{{ s.usage.requests }}</td>
            <td class="num"><span [class]="rateClass(s.usage.error_rate)">{{ pct(s.usage.error_rate) }}</span></td>
            <td class="num">{{ ms(s.usage.avg_latency_ms) }}</td>
            <td class="num">{{ compact(s.usage.total_tokens) }}</td>
            <td class="num">{{ usd(s.usage.cost_usd) }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #none2><div class="empty">暂无数据</div></ng-template>
    </div>

    <div class="card">
      <h2>工具使用 <span class="muted" style="font-weight:400;font-size:12px">（网关 agent 实际执行过的工具 TOP，含 mcp_* 与 skill 名）</span></h2>
      <table *ngIf="data() && data()!.tools.length; else noneTools">
        <thead>
          <tr>
            <th>工具</th><th class="num">调用次数</th><th class="num">使用方（key 数）</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let t of data()!.tools">
            <td><span class="mono">{{ t.name }}</span></td>
            <td class="num">{{ t.calls }}</td>
            <td class="num">{{ t.key_count }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneTools><div class="empty">暂无工具调用</div></ng-template>
    </div>

    <div class="card">
      <h2>技能调用 <span class="muted" style="font-weight:400;font-size:12px">（网关执行过的技能，按 skill: 前缀聚合）</span></h2>
      <table *ngIf="data() && data()!.skills.length; else noneSkills">
        <thead>
          <tr>
            <th>技能</th><th class="num">调用次数</th><th class="num">使用方（key 数）</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let t of data()!.skills">
            <td><span class="mono">{{ t.skill }}</span></td>
            <td class="num">{{ t.calls }}</td>
            <td class="num">{{ t.key_count }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneSkills><div class="empty">暂无技能调用</div></ng-template>
    </div>

    <div class="card">
      <h2>MCP 使用 <span class="muted" style="font-weight:400;font-size:12px">（mcp_&lt;server&gt;_&lt;tool&gt; 按 server 聚合）</span></h2>
      <table *ngIf="data() && data()!.mcps.length; else noneMcps">
        <thead>
          <tr>
            <th>MCP Server</th><th class="num">调用次数</th><th class="num">使用方（key 数）</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let t of data()!.mcps">
            <td><span class="mono">{{ t.server }}</span></td>
            <td class="num">{{ t.calls }}</td>
            <td class="num">{{ t.key_count }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneMcps><div class="empty">暂无 MCP 调用</div></ng-template>
    </div>

    <div class="card">
      <h2>外部自创工具 <span class="muted" style="font-weight:400;font-size:12px">（调用方声明、不在网关目录里的工具/技能；可择优录用进网关）</span></h2>
      <table *ngIf="data() && data()!.external_tools.length; else noneExt">
        <thead>
          <tr>
            <th>名称</th><th class="num">调用次数</th><th class="num">使用方（key 数）</th><th>状态</th><th class="actions">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let t of data()!.external_tools">
            <td><span class="mono">{{ t.name }}</span></td>
            <td class="num">{{ t.calls }}</td>
            <td class="num">{{ t.key_count }}</td>
            <td>
              <span *ngIf="t.adopted" class="badge ok">{{ t.kind === 'skill' ? '已录用·技能' : '已录用·工具' }}</span>
              <span *ngIf="!t.adopted" class="badge">未录用</span>
            </td>
            <td class="actions">
              <button *ngIf="!t.adopted" class="small primary" (click)="openAdopt(t)">录用</button>
              <button *ngIf="t.adopted" class="small" (click)="unadopt(t)">取消</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneExt><div class="empty">暂无外部自创工具</div></ng-template>
    </div>

    <div class="card">
      <h2>按天趋势</h2>
      <table *ngIf="visibleTrend().length; else none3">
        <thead>
          <tr>
            <th>日期</th><th class="num">请求</th><th class="num">错误率</th>
            <th class="num">平均延迟</th><th class="num">Tokens</th><th class="num">成本</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let d of visibleTrend()">
            <td class="mono">{{ d.date }}</td>
            <td class="num">{{ d.requests }}</td>
            <td class="num">{{ pct(d.requests ? d.errors / d.requests : 0) }}</td>
            <td class="num">{{ ms(d.requests ? Math.floor(d.latency_sum_ms / d.requests) : 0) }}</td>
            <td class="num">{{ compact(d.total_tokens) }}</td>
            <td class="num">{{ usd(d.cost_usd) }}</td>
          </tr>
        </tbody>
      </table>
      <ng-template #none3><div class="empty">暂无数据</div></ng-template>
    </div>

    <!-- 录用外部工具弹窗 -->
    <div class="modal-backdrop" *ngIf="adoptTarget()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <span class="modal-icon">＋</span>
          <div class="modal-titles">
            <h2>录用外部工具</h2>
            <div class="sub">{{ adoptTarget()?.name }} · 来自外部调用方声明</div>
          </div>
          <button class="icon" (click)="closeAdopt()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <h3>说明</h3>
            <div class="form-row">
              <div><label>描述（模型可见）</label><input [(ngModel)]="adoptForm.description" placeholder="这个能力做什么用" /></div>
            </div>
          </div>
          <div class="form-section">
            <h3>类型</h3>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="adoptForm.kind" value="tool" />
              <span><b>工具</b><span class="muted" style="display:block;font-size:12px">可执行能力（JS 检测器 / 转发 / 仅登记）</span></span>
            </label>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="adoptForm.kind" value="skill" />
              <span><b>技能</b><span class="muted" style="display:block;font-size:12px">技能说明，skill-run 可注入（适用于外部调用方自创的技能）</span></span>
            </label>
          </div>
          <div *ngIf="adoptForm.kind === 'skill'" class="form-section">
            <h3>技能说明（skill-run 注入内容）</h3>
            <div class="form-row">
              <div>
                <label>指令说明（模型可见，可含执行步骤）</label>
                <textarea [(ngModel)]="adoptForm.impl_source" rows="5" class="mono code-input"
                          placeholder="技能说明：检查持仓与行情，输出周报（概览/收益/风险/建议）。"></textarea>
              </div>
            </div>
          </div>
          <div *ngIf="adoptForm.kind !== 'skill'" class="form-section">
            <h3>网关侧实现方式</h3>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="adoptForm.impl_type" value="none" />
              <span><b>仅登记</b><span class="muted" style="display:block;font-size:12px">进工具池供模型感知；执行由调用方侧完成</span></span>
            </label>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="adoptForm.impl_type" value="js" />
              <span><b>JS 检测器</b><span class="muted" style="display:block;font-size:12px">粘贴 detect(text) 实现，网关可直接执行（类似 fastpath 插件）</span></span>
            </label>
            <div *ngIf="adoptForm.impl_type === 'js'" style="margin-top:8px">
              <label>JS 源码（函数 detect(text) 返回命中文本或 null）</label>
              <textarea [(ngModel)]="adoptForm.impl_source" rows="6" class="mono code-input"
                        placeholder="function detect(text) { 返回命中文本或 null }"></textarea>
            </div>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="adoptForm.impl_type" value="alias" />
              <span><b>转发到现有工具</b><span class="muted" style="display:block;font-size:12px">映射到系统已有工具（如 calc / fetch_url / mcp_*）</span></span>
            </label>
            <div *ngIf="adoptForm.impl_type === 'alias'" style="margin-top:8px">
              <label>目标工具名</label>
              <input [(ngModel)]="adoptForm.impl_source" placeholder="如 calc / get_time / fetch_url" />
            </div>
          </div>
        </div>
        <div class="modal-foot">
          <button class="small" (click)="closeAdopt()">取消</button>
          <button class="small primary" (click)="adopt()" [disabled]="adopting()">录用</button>
        </div>
      </div>
    </div>
  `,
})
export class ObservabilityComponent implements OnInit {
  days = 14;
  data = signal<ObservabilityResponse | null>(null);
  error = signal('');
  Math = Math;

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.error.set('');
    this.api.observability(this.days).subscribe({
      next: (d) => this.data.set(d),
      error: (e) => this.error.set(e?.message || '加载失败'),
    });
  }

  /** 按天趋势只展示有数据的日期，清空后不残留 0 行。 */
  visibleTrend(): DailyPoint[] {
    return (this.data()?.trend || []).filter(
      (d) => d.requests > 0 || d.total_tokens > 0 || d.cost_usd > 0 || d.errors > 0,
    );
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
