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
        <button (click)="copy(justCreated.key)">复制</button>
        <button (click)="justCreated = null">我已保存</button>
      </div>
    </div>

    <!-- 签发弹窗 -->
    <div class="modal-backdrop" *ngIf="creating()" (click)="cancel()">
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
            <th class="num">RPM</th><th>配额</th><th>操作</th>
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
            <td class="muted" style="font-size:12px">{{ quotaText(k.quota) }}</td>
            <td>
              <button class="small" (click)="toggle(k)">{{ k.enabled ? '停用' : '启用' }}</button>
              <button class="small danger" (click)="remove(k)">删除</button>
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
  justCreated: { id: string; key: string; prefix: string } | null = null;

  readonly compact = compact;
  readonly usd = usd;

  modelsText = '';
  expireDays = 0;
  form: { name: string; quota: Quota } = { name: '', quota: this.blankQuota() };

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
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

  copy(text: string): void {
    navigator.clipboard?.writeText(text);
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
}
