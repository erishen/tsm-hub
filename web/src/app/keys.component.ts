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
  template: `
    <div class="page-head">
      <div>
        <h1>Token Keys</h1>
        <div class="sub">对外签发 sk-tr- 开头的自制 Key，服务端只保存哈希</div>
      </div>
      <button class="primary" (click)="startNew()">+ 签发</button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="page-loading" *ngIf="loading()">加载中…</div>
    <!-- 外部接入说明 -->
    <div class="card">
      <h2>外部怎么连</h2>

      <div class="step">
        <div class="step-num">1</div>
        <div style="flex:1">
          <label>Base URL（OpenAI 兼容端点，网关实际地址）</label>
          <input [(ngModel)]="baseUrl" (blur)="saveBaseUrl()" placeholder="http://localhost:9070/v1" style="max-width:520px" />
          <div class="muted small" style="margin-top:4px">网关监听 <span class="mono">:9070</span>；本机联调默认已填好，部署到服务器时改成对外可达地址（如 <span class="mono">https://llm.example.com/v1</span>）。</div>
        </div>
      </div>

      <div class="step">
        <div class="step-num">2</div>
        <div style="flex:1">
          <label>请求头认证</label>
          <div class="code-block" style="max-width:520px">
            <button class="small" style="float:right" (click)="copy('Authorization: Bearer <你的Key>', 'auth')">{{ copied() === 'auth' ? '已复制 ✓' : '复制' }}</button>
            <pre>Authorization: Bearer &lt;你的Key&gt;</pre>
          </div>
          <div class="muted small" style="margin-top:4px">Key 以 <span class="mono">sk-tr-</span> 开头，本页签发；服务端只保存哈希。与 OpenAI 客户端完全兼容（SDK 里传 <span class="mono">api_key</span> 即可）。</div>
        </div>
      </div>

      <div class="step">
        <div class="step-num">3</div>
        <div style="flex:1">
          <label>直接复制示例跑通</label>
          <div style="display:flex;gap:12px;flex-wrap:wrap">
            <div style="flex:1;min-width:320px">
              <label class="small muted" style="margin-bottom:6px">curl</label>
              <div class="code-block">
                <button class="small" style="float:right" (click)="copy(curlExample, 'curl')">{{ copied() === 'curl' ? '已复制 ✓' : '复制' }}</button>
                <pre>{{ curlExample }}</pre>
              </div>
            </div>
            <div style="flex:1;min-width:320px">
              <label class="small muted" style="margin-bottom:6px">Python（OpenAI SDK）</label>
              <div class="code-block">
                <button class="small" style="float:right" (click)="copy(pyExample, 'py')">{{ copied() === 'py' ? '已复制 ✓' : '复制' }}</button>
                <pre>{{ pyExample }}</pre>
              </div>
            </div>
          </div>
          <div class="muted small" style="margin-top:8px">
            模型名填 <b>playground 候选</b>（路由别名或 Provider 模型 id）；网关自动智能分流到免费/低价家。
            配额、禁用、过期等都在本页控制。
          </div>
        </div>
      </div>

      <div class="step">
        <div class="step-num">4</div>
        <div style="flex:1">
          <label>可编程接口（用同一个 Key 查询网关能力）</label>
          <div class="muted small" style="margin-top:2px;margin-bottom:8px">
            带 <span class="mono">Authorization: Bearer &lt;你的Key&gt;</span> 即可读取；用于让调用方程序动态发现网关挂了什么。
          </div>
          <div class="api-grid">
            <div class="api-item" *ngFor="let a of apiEndpoints">
              <span class="mono method">{{ a.method }}</span>
              <span class="mono path">{{ a.path }}</span>
              <span class="muted small">{{ a.desc }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 一次性明文展示 -->
    <div class="card" *ngIf="justCreated">
      <h2>请立即保存这个 Key</h2>
      <div class="banner warn">它只会出现这一次。服务端只保存 sha256 哈希，关闭后无法找回。</div>
      <div class="secret">{{ justCreated.key }}</div>
      <div class="muted" style="margin-top:8px">
        ID: <span class="mono">{{ justCreated.id }}</span> ·
        Prefix: <span class="mono">{{ justCreated.prefix }}</span>
      </div>
      <div style="margin-top:12px">
        <label>立即用 curl 验证</label>
        <div class="code-block">
          <button class="small" style="float:right" (click)="copy(curlWithKey())">{{ copied() === 'curlKey' ? '已复制 ✓' : '复制' }}</button>
          <pre>{{ curlWithKey() }}</pre>
        </div>
      </div>
      <div style="margin-top:12px">
        <button class="primary" (click)="copy(justCreated.key, 'key')">{{ copied() === 'key' ? '已复制 ✓' : '复制 Key' }}</button>
        <button (click)="justCreated = null">我已保存</button>
      </div>
    </div>

    <!-- 签发弹窗 -->
    <!-- 补看明文弹窗（创建后 2 分钟窗口内可用） -->
    <div class="modal-backdrop" *ngIf="revealPlain()">
      <div class="modal card" style="max-width:560px">
        <div style="display:flex;align-items:center;gap:10px">
          <h2 style="margin:0">Key 明文</h2>
          <span class="muted" style="font-size:12px">{{ revealName() }} · 仅本次展示，关闭后无法再查看</span>
          <button class="small" style="margin-left:auto" (click)="closeReveal()">关闭</button>
        </div>
        <div class="mono" style="margin:14px 0;padding:12px;background:#f6f4ef;border-radius:8px;word-break:break-all;user-select:all">{{ revealPlain() }}</div>
        <div style="display:flex;gap:8px">
          <button class="primary" (click)="copy(revealPlain(), 'key')">复制 Key</button>
          <span class="muted" style="font-size:12px;align-self:center" *ngIf="copied() === 'key'">已复制 ✓</span>
        </div>
      </div>
    </div>

    <div class="modal-backdrop" *ngIf="creating() || editing()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">+</div>
          <div class="modal-titles">
            <h2>{{ editing() ? '编辑 Key' : '签发新 Key' }}</h2>
            <div class="sub">{{ editing() ? editing()!.prefix + ' · key 本身不变' : '生成 sk-tr- 开头的自制 Key，服务端只保存哈希' }}</div>
          </div>
          <button class="icon" (click)="cancel()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <h3>基本信息</h3>
            <div class="form-row">
              <div><label>名称</label><input [(ngModel)]="form.name" placeholder="生产环境 A" /></div>
              <div><label>允许模型（逗号分隔，留空=全部）</label><input [(ngModel)]="modelsText" placeholder="smart,fast" /></div>
            </div>
            <div class="form-row">
              <div>
                <label>技能注入（可选）</label>
                <select [(ngModel)]="injectSkills">
                  <option value="">不注入</option>
                  <option value="list">技能清单（轻量，用于"有啥技能"）</option>
                  <option value="all">全部技能全文</option>
                  <option value="__name__">指定技能</option>
                </select>
              </div>
              <div *ngIf="injectSkills === '__name__'">
                <label>选择技能</label>
                <select [(ngModel)]="injectSkillName">
                  <option *ngFor="let s of skillOptions()" [value]="s.name">{{ s.name }}</option>
                </select>
              </div>
            </div>
          </div>
          <div class="form-section">
            <h3>配额（0 = 不限）</h3>
            <div class="form-row">
              <div><label>总 Token 上限</label><input type="number" [(ngModel)]="form.quota.max_tokens" /></div>
              <div><label>总成本上限 USD</label><input type="number" [(ngModel)]="form.quota.max_cost_usd" /></div>
              <div><label>每日 Token 上限</label><input type="number" [(ngModel)]="form.quota.daily_tokens" /></div>
              <div><label>每分钟请求数</label><input type="number" [(ngModel)]="form.quota.rpm" /></div>
              <div><label>有效期（天，0=永久）</label><input type="number" [(ngModel)]="expireDays" /></div>
            </div>
          </div>
        </div>
        <div class="modal-foot">
          <button (click)="cancel()" [disabled]="saving()">取消</button>
          <button class="primary" (click)="saveOrCreate()" [disabled]="saving()">
            {{ saving() ? (editing() ? '保存中…' : '签发中…') : (editing() ? '保存' : '签发') }}
          </button>
        </div>
      </div>
    </div>

    <div class="card">
      <h2>已签发（{{ keys().length }}）</h2>
      <div class="batch-bar" *ngIf="selectedCount()">
        <span>已选 {{ selectedCount() }} 项</span>
        <button class="small" (click)="batchToggle(true)">批量启用</button>
        <button class="small" (click)="batchToggle(false)">批量停用</button>
        <button class="small danger" (click)="batchDelete()">批量删除</button>
        <button class="small" style="margin-left:auto" (click)="clearSelection()">取消选择</button>
      </div>
      <table *ngIf="keys().length; else none">
        <thead>
          <tr>
            <th style="width:32px"><input type="checkbox" [checked]="allSelected()" (change)="toggleAll($event)" /></th>
            <th>名称 / Prefix</th><th>状态</th><th>模型</th><th>技能注入</th>
            <th class="num">用量 Tokens</th><th class="num">成本</th>
            <th class="num">RPM</th><th>工具使用 <span class="muted" style="font-weight:400;font-size:11px">（执行 / 声明）</span></th><th>剩余 / 配额</th><th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let k of keys()" [class.selected]="isSelected(k.id)">
            <td><input type="checkbox" [checked]="isSelected(k.id)" (change)="toggleSelect(k.id, $event)" /></td>
            <td>
              <div>{{ k.name }}</div>
              <div class="mono muted">{{ k.prefix }}</div>
            </td>
            <td>
              <span class="badge" [class.ok]="k.enabled" [class.bad]="!k.enabled">
                {{ k.enabled ? 'enabled' : 'disabled' }}
              </span>
              <div class="muted" style="font-size:12px" *ngIf="k.expires_at">
                {{ k.expires_at | date:'yyyy-MM-dd' }} 过期
              </div>
            </td>
            <td>{{ (k.models && k.models.length) ? k.models.join(', ') : '全部' }}</td>
            <td>
              <span class="muted" style="font-size:12px" *ngIf="!k.inject_skills">—</span>
              <span *ngIf="k.inject_skills" title="技能注入：该 Key 的请求会自动携带技能上下文">{{ injectLabel(k.inject_skills) }}</span>
            </td>
            <td class="num">{{ compact(k.usage?.total_tokens ?? 0) }}</td>
            <td class="num">{{ usd(k.usage?.cost_usd ?? 0) }}</td>
            <td class="num">{{ k.rpm_current }}/{{ k.quota.rpm || '∞' }}</td>
            <td style="font-size:12px;max-width:220px">
              <ng-container *ngIf="k.tools_used && k.tools_used.length; else noTools">
                <span class="mono" style="margin-right:4px" *ngFor="let t of k.tools_used; let last = last">{{ t }}{{ last ? '' : ',' }}</span>
                <div class="muted" *ngIf="k.tools_declared && k.tools_declared.length" title="该 Key 请求中声明过的工具（含系统外）">
                  声明：{{ k.tools_declared.join(', ') }}
                </div>
              </ng-container>
              <ng-template #noTools><span class="muted">—</span></ng-template>
            </td>
            <td class="muted" style="font-size:12px">{{ quotaRemain(k) }}</td>
            <td style="white-space:nowrap">
              <button class="small" (click)="startEdit(k)">编辑</button>
              <button class="small" style="margin-left:6px" (click)="toggle(k)">{{ k.enabled ? '停用' : '启用' }}</button>
              <button class="small" style="margin-left:6px" *ngIf="k.revealable" (click)="reveal(k)">补看明文</button>
              <button class="small danger" style="margin-left:6px" (click)="remove(k)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none><div class="empty">还没有 Key，点击右上角「+ 签发」创建对外访问凭证（sk-tr- 开头）</div></ng-template>
    </div>
  `,
  styles: [`
    .api-grid { display:flex; flex-direction:column; gap:6px; max-width:720px; }
    .api-item { display:flex; align-items:baseline; gap:10px; padding:6px 10px; background:#fbfaf7;
                border:1px solid var(--border,#e4e3dd); border-radius:8px; }
    .api-item .method { font-weight:600; color:#8BC8EA; min-width:38px; font-size:12px; }
    .api-item .path { font-weight:600; min-width:110px; }
  `],
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
  form: { name: string; quota: Quota } = { name: '', quota: this.blankQuota() };
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
    this.form = { name: '', quota: this.blankQuota() };
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
    }).subscribe({
      next: (res) => {
        this.saving.set(false);
        this.justCreated = { id: res.id, key: res.key, prefix: res.prefix };
        this.creating.set(false);
        this.form = { name: '', quota: this.blankQuota() };
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
