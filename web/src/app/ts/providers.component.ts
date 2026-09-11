import { Component, OnInit, signal, computed, HostListener } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { Balance, Provider, ProbeModel } from './models';

@Component({
  selector: 'app-providers',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/providers.component.html',
})
export class ProvidersComponent implements OnInit, OnDestroy {
  readonly providers = signal<Provider[]>([]);
  readonly error = signal('');
  readonly loading = signal(true);
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

  // ---- 批量操作 ----
  private selected = new Set<string>();
  selectedCount(): number { return this.selected.size; }
  isSelected(id: string): boolean { return this.selected.has(id); }
  allSelected(): boolean { return this.providers().length > 0 && this.selected.size === this.providers().length; }
  toggleSelect(id: string, ev: Event): void {
    const checked = (ev.target as HTMLInputElement).checked;
    if (checked) this.selected.add(id); else this.selected.delete(id);
  }
  toggleAll(ev: Event): void {
    const checked = (ev.target as HTMLInputElement).checked;
    if (checked) this.providers().forEach((p) => this.selected.add(p.id));
    else this.selected.clear();
  }
  clearSelection(): void { this.selected.clear(); }

  /** 批量启用/禁用：逐个调用保存 API，完成后刷新列表。 */
  batchToggle(enabled: boolean): void {
    const ids = [...this.selected];
    if (!ids.length) return;
    if (!confirm(`确认${enabled ? '启用' : '禁用'}选中的 ${ids.length} 个 Provider？`)) return;
    let done = 0;
    ids.forEach((id) => {
      const p = this.providers().find((x) => x.id === id);
      if (!p) { done++; return; }
      const updated = { ...p, enabled };
      this.api.saveProvider(updated).subscribe({
        next: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
        error: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
      });
    });
  }

  /** 批量删除：逐个调用删除 API，完成后刷新列表。 */
  batchDelete(): void {
    const ids = [...this.selected];
    if (!ids.length) return;
    if (!confirm(`确认删除选中的 ${ids.length} 个 Provider？同时会清掉路由表里指向它们的候选。此操作不可恢复。`)) return;
    let done = 0;
    ids.forEach((id) => {
      this.api.deleteProvider(id).subscribe({
        next: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
        error: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
      });
    });
  }
  modelSet = new Set<string>();

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.editing()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

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
      weight: 100, priority: 10, timeout_ms: 120000, protocol: 'openai',
      healthy: true, latency_ms: 0,
    };
  }

  load(): void {
    this.loading.set(true);
    this.api.listProviders().subscribe({
      next: (r) => { this.providers.set(r.providers ?? []); this.loading.set(false); },
      error: (e: Error) => { this.error.set(e.message); this.loading.set(false); },
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
