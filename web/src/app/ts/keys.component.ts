import { Component, OnInit, signal, HostListener } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService, compact, usd } from './api.service';
import { ApiKey, Quota, SkillSummary } from './models';

@Component({
  selector: 'app-keys',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/keys.component.html',
  styleUrls: ['../css/keys.component.css'],
})
export class KeysComponent implements OnInit, OnDestroy {
  readonly keys = signal<ApiKey[]>([]);
  readonly error = signal('');
  readonly loading = signal(true);
  readonly creating = signal(false);
  readonly editing = signal<ApiKey | null>(null);
  readonly revealing = signal('');
  readonly revealPlain = signal('');
  readonly revealName = signal('');
  readonly saving = signal(false);
  /** 复制的目标标记：'' = 无；'key' | 'curl' | 'py' | 'curlKey' */
  readonly copied = signal('');
  justCreated: { id: string; key: string; prefix: string } | null = null;

  // ---- 批量操作 ----
  private selected = new Set<string>();
  selectedCount(): number { return this.selected.size; }
  isSelected(id: string): boolean { return this.selected.has(id); }
  allSelected(): boolean { return this.keys().length > 0 && this.selected.size === this.keys().length; }
  toggleSelect(id: string, ev: Event): void {
    const checked = (ev.target as HTMLInputElement).checked;
    if (checked) this.selected.add(id); else this.selected.delete(id);
  }
  toggleAll(ev: Event): void {
    const checked = (ev.target as HTMLInputElement).checked;
    if (checked) this.keys().forEach((k) => this.selected.add(k.id));
    else this.selected.clear();
  }
  clearSelection(): void { this.selected.clear(); }

  /** 批量启用/停用：逐个调用 toggleKey API，完成后刷新列表。 */
  batchToggle(enabled: boolean): void {
    const ids = [...this.selected];
    if (!ids.length) return;
    if (!confirm(`确认${enabled ? '启用' : '停用'}选中的 ${ids.length} 个 Key？`)) return;
    let done = 0;
    ids.forEach((id) => {
      this.api.toggleKey(id, enabled).subscribe({
        next: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
        error: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
      });
    });
  }

  /** 批量删除：逐个调用 deleteKey API，完成后刷新列表。 */
  batchDelete(): void {
    const ids = [...this.selected];
    if (!ids.length) return;
    if (!confirm(`确认删除选中的 ${ids.length} 个 Key？使用这些 Key 的客户端会立即失效。此操作不可恢复。`)) return;
    let done = 0;
    ids.forEach((id) => {
      this.api.deleteKey(id).subscribe({
        next: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
        error: () => { if (++done === ids.length) { this.selected.clear(); this.load(); } },
      });
    });
  }

  /** 可编程接口：用同一个 Key 读取网关能力（发现 tools / mcps / skills / models）。 */
  readonly apiEndpoints = [
    { method: 'GET', path: '/v1/models', desc: '可用模型与路由别名' },
    { method: 'GET', path: '/v1/tools', desc: '网关工具池（内置 + MCP，OpenAI function schema）' },
    { method: 'GET', path: '/v1/mcps', desc: '挂载的 MCP server（名称 / 状态 / 工具）' },
    { method: 'GET', path: '/v1/skills', desc: '技能库清单；/v1/skills/&lt;name&gt; 取单个技能全文' },
  ];

  readonly compact = compact;
  readonly usd = usd;

  modelsText = '';
  expireDays = 0;
  form: { name: string; quota: Quota; agent_disabled: boolean } = { name: '', quota: this.blankQuota(), agent_disabled: false };
  /** 技能注入：''=不注入 | list | all | __name__(指定技能)，默认 list（技能清单）。 */
  injectSkills = 'list';
  injectSkillName = '';
  readonly skillOptions = signal<SkillSummary[]>([]);

  /** 对外 Base URL（OpenAI 兼容端点），默认按当前 host 推断 :9070，可改并记忆。 */
  baseUrl = '';

  private static readonly BASE_URL_KEY = 'tsm-hub.public-base-url';

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.creating() || this.editing()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

constructor(private api: ApiService) {}

  ngOnInit(): void {
    const saved = localStorage.getItem(KeysComponent.BASE_URL_KEY);
    this.baseUrl = saved || `http://${window.location.hostname}:9070/v1`;
    this.load();
    this.api.listSkills().subscribe({
      next: (r) => this.skillOptions.set(r.skills ?? []),
      error: () => this.skillOptions.set([]),
    });
  }

  injectLabel(v: string): string {
    if (v === 'list') return '技能清单';
    if (v === 'all') return '全部技能';
    return v;
  }

  saveBaseUrl(): void {
    try { localStorage.setItem(KeysComponent.BASE_URL_KEY, this.baseUrl.trim()); } catch {}
  }

  get curlExample(): string {
    return [
      `curl ${this.baseUrl.trim()}/chat/completions \\`,
      `  -H "Authorization: Bearer <你的Key>" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '{"messages":[{"role":"user","content":"你好"}]}'`,
      ``,
      `# 无脑调用：不传 model（或传 "auto"），网关按内容自动分流`,
      `# chat / fast / reason / code 等场景由 tsm-hub 内部决策，外部无需关心`,
    ].join('\n');
  }

  get pyExample(): string {
    return [
      `from openai import OpenAI`,
      ``,
      `client = OpenAI(`,
      `    base_url="${this.baseUrl.trim()}",  # 网关地址`,
      `    api_key="<你的Key>",                # sk-tr- 开头`,
      `)`,
      ``,
      `# 无脑调用：model 可省略或传 "auto"，网关按内容自动分流`,
      `resp = client.chat.completions.create(`,
      `    model="auto",`,
      `    messages=[{"role": "user", "content": "你好"}],`,
      `)`,
      `print(resp.choices[0].message.content)`,
    ].join('\n');
  }

  /** 刚签发时的带真实 Key 的 curl（仅此一次展示）。 */
  curlWithKey(): string {
    if (!this.justCreated) return '';
    return [
      `curl ${this.baseUrl.trim()}/chat/completions \\`,
      `  -H "Authorization: Bearer ${this.justCreated.key}" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}]}'`,
    ].join('\n');
  }

  blankQuota(): Quota {
    return { max_tokens: 0, max_cost_usd: 0, daily_tokens: 0, rpm: 60 };
  }

  load(): void {
    this.loading.set(true);
    this.api.listKeys().subscribe({
      next: (r) => { this.keys.set(r.keys ?? []); this.loading.set(false); },
      error: (e: Error) => { this.error.set(e.message); this.loading.set(false); },
    });
  }

  @HostListener('document:keydown.escape')
  onEsc(): void {
    if ((this.creating() || this.editing()) && !this.saving()) this.cancel();
  }

  startNew(): void {
    this.form = { name: '', quota: this.blankQuota(), agent_disabled: false };
    this.modelsText = '';
    this.expireDays = 0;
    this.injectSkills = 'list';
    this.injectSkillName = '';
    this.creating.set(true);
  }

  startEdit(k: ApiKey): void {
    this.form = {
      name: k.name,
      quota: {
        max_tokens: k.quota.max_tokens ?? 0,
        max_cost_usd: k.quota.max_cost_usd ?? 0,
        daily_tokens: k.quota.daily_tokens ?? 0,
        rpm: k.quota.rpm ?? 0,
      },
      agent_disabled: k.agent_disabled ?? false,
    };
    this.modelsText = (k.models && k.models.length) ? k.models.join(',') : '';
    this.expireDays = 0; // 编辑不改有效期
    const inj = k.inject_skills || '';
    if (inj === 'list' || inj === 'all' || inj === '') {
      this.injectSkills = inj;
      this.injectSkillName = '';
    } else {
      this.injectSkills = '__name__';
      this.injectSkillName = inj;
    }
    this.editing.set(k);
  }

  cancel(): void {
    this.creating.set(false);
    this.editing.set(null);
  }

  saveOrCreate(): void {
    if (this.editing()) { this.save(); return; }
    this.create();
  }

  save(): void {
    const k = this.editing();
    if (!k) return;
    this.saving.set(true);
    const models = this.modelsText.split(',').map((s) => s.trim()).filter(Boolean);
    const inject = this.injectSkills === '__name__' ? this.injectSkillName : this.injectSkills;
    this.api.updateKey(k.id, {
      name: this.form.name,
      models: models.length ? models : [],
      quota: this.form.quota,
      inject_skills: inject,
      agent_disabled: this.form.agent_disabled,
    }).subscribe({
      next: () => {
        this.saving.set(false);
        this.editing.set(null);
        this.load();
      },
      error: (e: Error) => {
        this.saving.set(false);
        this.error.set(e.message);
      },
    });
  }

  create(): void {
    this.saving.set(true);
    const models = this.modelsText.split(',').map((s) => s.trim()).filter(Boolean);
    const inject = this.injectSkills === '__name__' ? this.injectSkillName : this.injectSkills;
    this.api.createKey({
      name: this.form.name,
      models: models.length ? models : undefined,
      quota: this.form.quota,
      expires_in_seconds: this.expireDays > 0 ? this.expireDays * 86400 : undefined,
      inject_skills: inject || undefined,
      agent_disabled: this.form.agent_disabled,
    }).subscribe({
      next: (res) => {
        this.saving.set(false);
        this.justCreated = { id: res.id, key: res.key, prefix: res.prefix };
        this.creating.set(false);
        this.form = { name: '', quota: this.blankQuota(), agent_disabled: false };
        this.modelsText = '';
        this.injectSkills = '';
        this.injectSkillName = '';
        this.load();
      },
      error: (e: Error) => {
        this.saving.set(false);
        this.error.set(e.message);
      },
    });
  }

  /** 补看新建 key 的明文（仅创建后 2 分钟窗口内有效）。 */
  reveal(k: ApiKey): void {
    this.revealing.set(k.id);
    this.api.revealKey(k.id).subscribe({
      next: (r) => {
        this.revealPlain.set(r.key);
        this.revealName.set(`${k.name}（${k.prefix}）`);
        this.revealing.set('');
      },
      error: (e: Error) => { this.error.set(e.message || '补看失败（可能已超过 2 分钟窗口）'); this.revealing.set(''); },
    });
  }

  closeReveal(): void {
    this.revealPlain.set('');
  }

  toggle(k: ApiKey): void {
    this.api.toggleKey(k.id, !k.enabled).subscribe({
      next: () => this.load(),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  remove(k: ApiKey): void {
    if (!confirm(`删除 Key ${k.name}（${k.prefix}）？使用该 Key 的客户端会立即失效。`)) return;
    this.api.deleteKey(k.id).subscribe({
      next: () => this.load(),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  copy(text: string, label = ''): void {
    const done = (ok: boolean) => {
      this.copied.set(ok ? label : '');
      setTimeout(() => this.copied.set(''), 2000);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(() => done(true), () => this.fallbackCopy(text, done));
    } else {
      this.fallbackCopy(text, done);
    }
  }

  private fallbackCopy(text: string, done: (ok: boolean) => void): void {
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
      done(true);
    } catch {
      done(false);
    }
  }

  quotaText(q?: Quota): string {
    if (!q) return '不限';
    const parts: string[] = [];
    if (q.max_tokens) parts.push(`${compact(q.max_tokens)} tokens`);
    if (q.max_cost_usd) parts.push(usd(q.max_cost_usd));
    if (q.daily_tokens) parts.push(`${compact(q.daily_tokens)}/天`);
    if (q.rpm) parts.push(`${q.rpm}/min`);
    return parts.length ? parts.join(' · ') : '不限';
  }

  /** 剩余 / 配额：以 usage 聚合减去配额上限展示，未设配额显示不限。 */
  quotaRemain(k: ApiKey): string {
    const q = k.quota;
    const u = k.usage;
    if (!q || (!q.max_tokens && !q.max_cost_usd && !q.daily_tokens && !q.rpm)) return '不限';
    const parts: string[] = [];
    if (q.max_tokens) {
      const used = u?.total_tokens ?? 0;
      const left = Math.max(0, q.max_tokens - used);
      parts.push(`tokens ${compact(left)}/${compact(q.max_tokens)}`);
    }
    if (q.max_cost_usd) {
      const used = u?.cost_usd ?? 0;
      const left = Math.max(0, q.max_cost_usd - used);
      parts.push(`$ ${left.toFixed(2)}/${q.max_cost_usd.toFixed(2)}`);
    }
    if (q.daily_tokens) {
      const used = u?.total_tokens ?? 0;
      const left = Math.max(0, q.daily_tokens - used);
      parts.push(`/天 ${compact(left)}/${compact(q.daily_tokens)}`);
    }
    if (q.rpm) {
      const left = Math.max(0, q.rpm - (k.rpm_current ?? 0));
      parts.push(`rpm ${left}/${q.rpm}`);
    }
    return parts.join(' · ');
  }
}
