import { Component, OnInit, signal, computed, HostListener } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { Balance, Provider, ProbeModel } from './models';

@Component({
  selector: 'app-providers',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>Providers</h1>
        <div class="sub">上游 LLM 服务：填 Base URL、Key 与它支持的模型</div>
      </div>
      <button class="primary" (click)="startNew()">+ 新增</button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="card">
      <h2>已配置（{{ providers().length }}）</h2>
      <table *ngIf="providers().length; else none">
        <thead>
          <tr>
            <th>ID / 名称</th><th>Base URL</th><th>模型</th>
            <th class="num">权重</th><th class="num">优先级</th>
            <th>状态</th><th class="num">延迟</th><th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let p of providers()">
            <td>
              <div class="mono">{{ p.id }}</div>
              <div class="muted">{{ p.name }}</div>
            </td>
            <td class="mono nowrap" style="max-width:300px" [title]="p.base_url">{{ shortUrl(p.base_url) }}</td>
            <td style="max-width:240px">
              <div class="model-cell" [title]="(p.models || []).join(', ')">{{ modelsPreview(p.models) }}</div>
            </td>
            <td class="num">{{ p.weight }}</td>
            <td class="num">{{ p.priority }}</td>
            <td>
              <span class="badge" [class.ok]="p.enabled && p.healthy" [class.bad]="!p.healthy">
                {{ p.enabled ? (p.healthy ? 'healthy' : 'degraded') : 'disabled' }}
              </span>
            </td>
            <td class="num">{{ p.latency_ms }} ms</td>
            <td style="white-space:nowrap">
              <button class="small" (click)="edit(p)">编辑</button>
              <button class="small danger" style="margin-left:6px" (click)="remove(p)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none><div class="empty">还没有 Provider</div></ng-template>
    </div>

    <!-- 编辑弹窗 -->
    <div class="modal-backdrop" *ngIf="editing()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">{{ form.id ? '✎' : '+' }}</div>
          <div class="modal-titles">
            <h2>{{ form.id ? '编辑 ' + form.id : '新增 Provider' }}</h2>
            <div class="sub">{{ form.id ? '修改上游服务的连接与路由参数' : '填写上游 LLM 服务的连接信息' }}</div>
          </div>
          <button class="icon" (click)="cancel()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <h3>基础信息</h3>
            <div class="form-row">
              <div><label>ID（唯一）</label><input [(ngModel)]="form.id" placeholder="openai-main" /></div>
              <div><label>名称</label><input [(ngModel)]="form.name" placeholder="OpenAI 主账号" /></div>
            </div>
            <div class="form-row">
              <div><label>Base URL</label><input [(ngModel)]="form.base_url" placeholder="https://api.openai.com/v1" /></div>
              <div><label>API Key</label><input [(ngModel)]="form.api_key" placeholder="sk-..." /></div>
            </div>
          </div>

          <div class="form-section">
            <h3>模型与路由</h3>
            <div class="form-row">
              <div>
                <label>模型（逗号分隔，* 表示全部）</label>
                <div style="display:flex;gap:8px">
                  <input [(ngModel)]="modelsText" placeholder="gpt-4o,gpt-4o-mini" style="flex:1" />
                  <button type="button" (click)="probe()" [disabled]="probing() || !form.base_url">
                    {{ probing() ? '查询中…' : '按 Key 查询' }}
                  </button>
                </div>
                <div class="muted small" *ngIf="form.api_key.includes('…')" style="margin-top:4px">
                  当前 Key 为脱敏回显值，探测前需重新输入完整 Key（或填 env: 引用）
                </div>
              </div>
            </div>
            <div class="banner warn" *ngIf="probeError()" style="margin-top:8px">{{ probeError() }}</div>
            <div class="banner" *ngIf="balance()" style="margin-top:8px;display:flex;align-items:center;gap:8px;flex-wrap:wrap">
              <span>额度：<strong>{{ balanceText(balance()!) }}</strong></span>
              <span class="badge free" *ngIf="balance()!.kind === 'moonshot' && balance()!.voucher && !balance()!.cash">全赠送额度</span>
              <span class="badge ok" *ngIf="balance()!.kind === 'deepseek' && balance()!.granted">含赠送</span>
              <span class="badge free" *ngIf="balance()!.kind === 'openrouter' && balance()!.is_free_tier">免费层</span>
            </div>
            <div *ngIf="probeModels().length" style="margin-top:10px">
              <div class="muted small" style="margin-bottom:6px">上游实际提供的模型（多选，勾选自动写入上方输入框）</div>
              <input type="search" placeholder="搜索模型（按 id 过滤）…" [(ngModel)]="probeQuery"
                     style="width:100%;padding:5px 10px;margin-bottom:8px;box-sizing:border-box" />
              <div style="display:flex;flex-wrap:wrap;gap:6px;max-height:150px;overflow-y:auto">
                <label *ngFor="let m of probeFiltered()"
                       style="display:inline-flex;align-items:center;gap:4px;flex-shrink:0;white-space:nowrap;
                              padding:3px 10px;border:1px solid var(--border-color);
                              border-radius:999px;background:rgba(0,0,0,0.025);font-size:12px;cursor:pointer">
                  <input type="checkbox" [checked]="modelSet.has(m.id)" (change)="toggleModel(m.id, $event)" />
                  <span class="mono">{{ m.id }}</span>
                  <span class="muted small" *ngIf="m.context_length">{{ fmtCtx(m.context_length) }}</span>
                  <span class="muted small" *ngIf="m.pricing" title="输入 / 输出（$/1M tokens）">$ {{ m.pricing.prompt }}/{{ m.pricing.completion }}M</span>
                  <span class="badge free" *ngIf="m.free">FREE</span>
                </label>
              </div>
              <div class="muted small" *ngIf="probeQuery().trim()" style="margin-top:4px">匹配 {{ probeFiltered().length }}/{{ probeModels().length }}</div>
            </div>
            <div class="form-row">
              <div><label>权重</label><input type="number" [(ngModel)]="form.weight" /></div>
              <div><label>优先级（小的优先）</label><input type="number" [(ngModel)]="form.priority" /></div>
              <div>
                <label>启用</label>
                <select [(ngModel)]="form.enabled">
                  <option [ngValue]="true">启用</option>
                  <option [ngValue]="false">停用</option>
                </select>
              </div>
            </div>
          </div>

          <div class="form-section">
            <h3>高级</h3>
            <div class="form-row">
              <div><label>超时 ms</label><input type="number" [(ngModel)]="form.timeout_ms" /></div>
            </div>
          </div>

          <div class="banner warn" *ngIf="!form.id && form.name">
            留空 ID 时，会根据名称自动生成。
          </div>
        </div>
        <div class="modal-foot">
          <button (click)="cancel()" [disabled]="saving()">取消</button>
          <button class="primary" (click)="save()" [disabled]="saving()">
            {{ saving() ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>
  `,
})
export class ProvidersComponent implements OnInit {
  readonly providers = signal<Provider[]>([]);
  readonly error = signal('');
  readonly editing = signal(false);
  readonly saving = signal(false);
  readonly probing = signal(false);
  readonly probeModels = signal<ProbeModel[]>([]);
  readonly probeError = signal('');
  readonly balance = signal<Balance | null>(null);
  readonly probeQuery = signal('');
  /** 模型多选列表按 id 过滤（忽略大小写）。 */
  readonly probeFiltered = computed(() => {
    const q = this.probeQuery().trim().toLowerCase();
    if (!q) return this.probeModels();
    return this.probeModels().filter((m) => m.id.toLowerCase().includes(q));
  });

  form: Provider = this.blank();
  modelsText = '';
  modelSet = new Set<string>();

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  @HostListener('document:keydown.escape')
  onEsc(): void {
    if (this.editing() && !this.saving()) this.cancel();
  }

  blank(): Provider {
    return {
      id: '', name: '', base_url: '', api_key: '', models: [], enabled: true,
      weight: 100, priority: 10, timeout_ms: 120000, healthy: true, latency_ms: 0,
    };
  }

  load(): void {
    this.api.listProviders().subscribe({
      next: (r) => this.providers.set(r.providers ?? []),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  /** Base URL 模板层截断：前 44 字符 + …，hover title 看全。 */
  shortUrl(u: string): string {
    return u.length > 44 ? u.slice(0, 44) + '…' : u;
  }

  /** 模型列表模板层截断：单个模型名 >26 字符截断 + 显示前 3 个 + “+N”，hover title 看全。 */
  modelsPreview(models: string[] | undefined): string {
    const ms = models || [];
    if (!ms.length) return '—';
    const shown = ms.length > 3 ? ms.slice(0, 3) : ms;
    const parts = shown.map((m) => (m.length > 26 ? m.slice(0, 26) + '…' : m));
    return parts.join(', ') + (ms.length > 3 ? ` +${ms.length - 3}` : '');
  }

  startNew(): void {
    this.form = this.blank();
    this.modelsText = '';
    this.modelSet = new Set();
    this.probeModels.set([]);
    this.probeError.set('');
    this.balance.set(null);
    this.editing.set(true);
  }

  edit(p: Provider): void {
    this.form = { ...p };
    this.modelsText = (p.models || []).join(',');
    this.modelSet = new Set(p.models || []);
    this.probeModels.set([]);
    this.probeError.set('');
    this.balance.set(null);
    this.editing.set(true);
  }

  /** 按 Base URL + API Key 探测上游模型，并同步勾选状态。 */
  probe(): void {
    this.probeError.set('');
    // 编辑场景下 Key 是脱敏回显值（含省略号），无法用于探测；留空则不带鉴权。
    const key = this.form.api_key.includes('…') ? '' : this.form.api_key;
    this.probing.set(true);
    this.api.probeModels({ base_url: this.form.base_url, api_key: key || undefined }).subscribe({
      next: (r) => {
        this.probing.set(false);
        this.probeModels.set(r.models ?? []);
        this.balance.set(r.balance ?? null);
        this.syncModelSet();
      },
      error: (e: Error) => {
        this.probing.set(false);
        this.probeError.set(e.message);
      },
    });
  }

  private syncModelSet(): void {
    this.modelSet = new Set(this.modelsText.split(',').map((s) => s.trim()).filter(Boolean));
  }

  toggleModel(id: string, ev: Event): void {
    this.syncModelSet();
    const cb = ev.target as HTMLInputElement;
    if (cb.checked) this.modelSet.add(id); else this.modelSet.delete(id);
    this.modelsText = [...this.modelSet].join(',');
  }

  /** 上下文窗口格式化：262144 → 256K，1048576 → 1M。 */
  fmtCtx(n: number): string {
    if (!n) return '';
    if (n >= 1048576) return (n / 1048576).toFixed(n % 1048576 ? 1 : 0) + 'M';
    if (n >= 1024) return (n / 1024).toFixed(n % 1024 ? 0 : 0) + 'K';
    return String(n);
  }

  /** 账户额度（token 可使用总量）文案，按上游格式渲染。 */
  balanceText(b: Balance): string {
    switch (b.kind) {
      case 'moonshot':
        return `可用 ¥${b.available?.toFixed(2)} · 券 ¥${b.voucher?.toFixed(2)} · 现金 ¥${b.cash?.toFixed(2)}`;
      case 'deepseek': {
        const cur = b.currency || '';
        return `总余额 ${b.total?.toFixed(2)}${cur} · 赠送 ${b.granted?.toFixed(2)}${cur} · 充值 ${b.topped_up?.toFixed(2)}${cur}`;
      }
      case 'openai':
        return b.hard_limit_usd
          ? `订阅上限 $${b.hard_limit_usd}`
          : `已用 $${b.total_usage_usd?.toFixed(2)}`;
      case 'openrouter': {
        const used = `已用 $${b.usage?.toFixed(2) ?? '0.00'}`;
        const limit = b.limit != null
          ? ` / 上限 $${b.limit}`
          : '（无额度上限）';
        const exp = b.expires_at ? ` · 有效期至 ${b.expires_at.slice(0, 10)}` : '';
        return used + limit + exp;
      }
      default:
        return '';
    }
  }

  cancel(): void {
    this.editing.set(false);
  }

  save(): void {
    this.saving.set(true);
    const models = this.modelsText.split(',').map((s) => s.trim()).filter(Boolean);
    const payload = { ...this.form, models };
    // 编辑时若 Key 未改动（脱敏展示），原样提交，由后端识别省略号保留真实值；
    // 不能 delete 该字段——后端按字段缺失解码为空串，会把真实 Key 覆盖掉。
    this.api.saveProvider(payload).subscribe({
      next: () => {
        this.saving.set(false);
        this.editing.set(false);
        this.load();
      },
      error: (e: Error) => {
        this.saving.set(false);
        this.error.set(e.message);
      },
    });
  }

  remove(p: Provider): void {
    if (!confirm(`删除 provider ${p.id}？同时会清掉路由表里指向它的候选。`)) return;
    this.api.deleteProvider(p.id).subscribe({
      next: () => this.load(),
      error: (e: Error) => this.error.set(e.message),
    });
  }
}
