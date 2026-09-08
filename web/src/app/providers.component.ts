import { Component, OnInit, signal, HostListener } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { Provider } from './models';

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
            <td class="mono">{{ p.base_url }}</td>
            <td>{{ (p.models || []).join(', ') }}</td>
            <td class="num">{{ p.weight }}</td>
            <td class="num">{{ p.priority }}</td>
            <td>
              <span class="badge" [class.ok]="p.enabled && p.healthy" [class.bad]="!p.healthy">
                {{ p.enabled ? (p.healthy ? 'healthy' : 'degraded') : 'disabled' }}
              </span>
            </td>
            <td class="num">{{ p.latency_ms }} ms</td>
            <td>
              <button class="small" (click)="edit(p)">编辑</button>
              <button class="small danger" (click)="remove(p)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none><div class="empty">还没有 Provider</div></ng-template>
    </div>

    <!-- 编辑弹窗 -->
    <div class="modal-backdrop" *ngIf="editing()" (click)="cancel()">
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
                <input [(ngModel)]="modelsText" placeholder="gpt-4o,gpt-4o-mini" />
              </div>
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

  form: Provider = this.blank();
  modelsText = '';

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

  startNew(): void {
    this.form = this.blank();
    this.modelsText = '';
    this.editing.set(true);
  }

  edit(p: Provider): void {
    this.form = { ...p };
    this.modelsText = (p.models || []).join(',');
    this.editing.set(true);
  }

  cancel(): void {
    this.editing.set(false);
  }

  save(): void {
    this.saving.set(true);
    const models = this.modelsText.split(',').map((s) => s.trim()).filter(Boolean);
    const payload = { ...this.form, models };
    // 编辑时若 Key 未改动（脱敏展示），不要覆盖服务端真实值。
    if (payload.api_key.includes('…')) {
      delete (payload as Partial<Provider>).api_key;
    }
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
