import { Component, OnInit, signal, HostListener } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService, compact, usd } from './api.service';
import { ApiKey, Quota } from './models';

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

    <!-- 外部接入说明 -->
    <div class="card">
      <h2>外部怎么连</h2>
      <div class="form-row">
        <div style="flex:0 0 420px">
          <label>Base URL（OpenAI 兼容端点，网关实际地址）</label>
          <input [(ngModel)]="baseUrl" (blur)="saveBaseUrl()" placeholder="http://localhost:9070/v1" />
        </div>
        <div style="flex:1">
          <label>认证方式</label>
          <div class="muted" style="padding:8px 0">请求头加 <code>Authorization: Bearer &lt;你的Key&gt;</code>（<code>sk-tr-</code> 开头），与 OpenAI 完全兼容。</div>
        </div>
      </div>
      <div style="margin-top:10px">
        <label>curl 示例</label>
        <div class="code-block">
          <button class="small" style="float:right" (click)="copy(curlExample)">{{ copied() === 'curl' ? '已复制 ✓' : '复制' }}</button>
          <pre>{{ curlExample }}</pre>
        </div>
      </div>
      <div style="margin-top:10px">
        <label>OpenAI SDK 接入</label>
        <div class="code-block">
          <button class="small" style="float:right" (click)="copy(pyExample)">{{ copied() === 'py' ? '已复制 ✓' : '复制' }}</button>
          <pre>{{ pyExample }}</pre>
        </div>
      </div>
      <div class="muted small" style="margin-top:8px">
        模型名填 <b>playground 候选</b>（路由别名或 Provider 模型 id）；网关会自动智能分流到免费/低价家。
        配额、禁用、过期等都在本页控制。
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
    <div class="modal-backdrop" *ngIf="creating()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">+</div>
          <div class="modal-titles">
            <h2>签发新 Key</h2>
            <div class="sub">生成 sk-tr- 开头的自制 Key，服务端只保存哈希</div>
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
          <button class="primary" (click)="create()" [disabled]="saving()">
            {{ saving() ? '签发中…' : '签发' }}
          </button>
        </div>
      </div>
    </div>

    <div class="card">
      <h2>已签发（{{ keys().length }}）</h2>
      <table *ngIf="keys().length; else none">
        <thead>
          <tr>
            <th>名称 / Prefix</th><th>状态</th><th>模型</th>
            <th class="num">用量 Tokens</th><th class="num">成本</th>
            <th class="num">RPM</th><th>剩余 / 配额</th><th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let k of keys()">
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
            <td class="num">{{ compact(k.usage?.total_tokens ?? 0) }}</td>
            <td class="num">{{ usd(k.usage?.cost_usd ?? 0) }}</td>
            <td class="num">{{ k.rpm_current }}/{{ k.quota.rpm || '∞' }}</td>
            <td class="muted" style="font-size:12px">{{ quotaRemain(k) }}</td>
            <td style="white-space:nowrap">
              <button class="small" (click)="toggle(k)">{{ k.enabled ? '停用' : '启用' }}</button>
              <button class="small danger" style="margin-left:6px" (click)="remove(k)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none><div class="empty">还没有 Key</div></ng-template>
    </div>
  `,
})
export class KeysComponent implements OnInit {
  readonly keys = signal<ApiKey[]>([]);
  readonly error = signal('');
  readonly creating = signal(false);
  readonly saving = signal(false);
  /** 复制的目标标记：'' = 无；'key' | 'curl' | 'py' | 'curlKey' */
  readonly copied = signal('');
  justCreated: { id: string; key: string; prefix: string } | null = null;

  readonly compact = compact;
  readonly usd = usd;

  modelsText = '';
  expireDays = 0;
  form: { name: string; quota: Quota } = { name: '', quota: this.blankQuota() };

  /** 对外 Base URL（OpenAI 兼容端点），默认按当前 host 推断 :9070，可改并记忆。 */
  baseUrl = '';

  private static readonly BASE_URL_KEY = 'llm-router.public-base-url';

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    const saved = localStorage.getItem(KeysComponent.BASE_URL_KEY);
    this.baseUrl = saved || `http://${window.location.hostname}:9070/v1`;
    this.load();
  }

  saveBaseUrl(): void {
    try { localStorage.setItem(KeysComponent.BASE_URL_KEY, this.baseUrl.trim()); } catch {}
  }

  get curlExample(): string {
    return [
      `curl ${this.baseUrl.trim()}/chat/completions \\`,
      `  -H "Authorization: Bearer <你的Key>" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}]}'`,
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
      `resp = client.chat.completions.create(`,
      `    model="deepseek-v4-flash",`,
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
    this.api.listKeys().subscribe({
      next: (r) => this.keys.set(r.keys ?? []),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  @HostListener('document:keydown.escape')
  onEsc(): void {
    if (this.creating() && !this.saving()) this.cancel();
  }

  startNew(): void {
    this.form = { name: '', quota: this.blankQuota() };
    this.modelsText = '';
    this.expireDays = 0;
    this.creating.set(true);
  }

  cancel(): void {
    this.creating.set(false);
  }

  create(): void {
    this.saving.set(true);
    const models = this.modelsText.split(',').map((s) => s.trim()).filter(Boolean);
    this.api.createKey({
      name: this.form.name,
      models: models.length ? models : undefined,
      quota: this.form.quota,
      expires_in_seconds: this.expireDays > 0 ? this.expireDays * 86400 : undefined,
    }).subscribe({
      next: (res) => {
        this.saving.set(false);
        this.justCreated = { id: res.id, key: res.key, prefix: res.prefix };
        this.creating.set(false);
        this.form = { name: '', quota: this.blankQuota() };
        this.modelsText = '';
        this.load();
      },
      error: (e: Error) => {
        this.saving.set(false);
        this.error.set(e.message);
      },
    });
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
