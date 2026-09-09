import { Component, OnInit, signal, HostListener } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { Provider, Route, RouteTarget } from './models';

@Component({
  selector: 'app-routes',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>路由表</h1>
        <div class="sub">把对外模型名映射到上游候选：failover 按优先级降级，weighted 按权重分流</div>
      </div>
      <button class="primary" (click)="startNew()">+ 新增</button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <!-- 编辑弹窗 -->
    <div class="modal-backdrop" *ngIf="editing()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">{{ editingModel ? '✎' : '+' }}</div>
          <div class="modal-titles">
            <h2>{{ editingModel ? '编辑 ' + editingModel : '新增路由' }}</h2>
            <div class="sub">{{ editingModel ? '调整映射策略与上游候选' : '把对外模型名映射到上游候选' }}</div>
          </div>
          <button class="icon" (click)="cancel()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <h3>映射</h3>
            <div class="form-row">
              <div><label>对外模型名</label><input [(ngModel)]="form.model" placeholder="smart" /></div>
              <div>
                <label>策略</label>
                <select [(ngModel)]="form.strategy">
                  <option value="failover">failover（优先降级）</option>
                  <option value="weighted">weighted（加权分流）</option>
                </select>
              </div>
            </div>
            <div>
              <label>备注（可选，如「百炼免费额度优先，用完可删」）</label>
              <input [(ngModel)]="form.remark" placeholder="临时策略说明…" />
            </div>
          </div>

          <div class="form-section">
            <h3>候选</h3>
            <div class="inline-form" *ngFor="let t of form.targets; let i = index">
              <div style="flex:2 1 200px">
                <label>Provider</label>
                <select [(ngModel)]="t.provider_id">
                  <option *ngFor="let p of providers()" [ngValue]="p.id">{{ p.id }} — {{ p.name }}</option>
                </select>
              </div>
              <div><label>上游模型（留空沿用）</label><input [(ngModel)]="t.model" placeholder="gpt-4o" /></div>
              <div style="flex:0 0 90px"><label>权重</label><input type="number" [(ngModel)]="t.weight" /></div>
              <div style="flex:0 0 90px"><label>优先级</label><input type="number" [(ngModel)]="t.priority" /></div>
              <button class="danger" (click)="removeTarget(i)">移除</button>
            </div>
            <button class="ghost" style="margin-top:8px" (click)="addTarget()">+ 候选</button>
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

    <div class="card">
      <h2>已配置（{{ routes().length }}）</h2>
      <table *ngIf="routes().length; else none">
        <thead>
          <tr><th>对外模型</th><th>策略</th><th>候选（按尝试顺序）</th><th>操作</th></tr>
        </thead>
        <tbody>
          <tr *ngFor="let r of routes()">
            <td class="mono">
              {{ r.model }}
              <div class="muted small" *ngIf="r.remark" style="color:#b45309;margin-top:2px" title="路由备注">{{ r.remark }}</div>
            </td>
            <td><span class="badge">{{ r.strategy }}</span></td>
            <td>
              <div *ngFor="let t of r.targets" class="mono">
                {{ t.provider_id }}
                <span class="muted">→ {{ t.model || '同请求模型' }}</span>
                <span class="badge">w{{ t.weight }}</span>
                <span class="badge">p{{ t.priority }}</span>
              </div>
            </td>
            <td style="white-space:nowrap">
              <button class="small" (click)="edit(r)">编辑</button>
              <button class="small danger" style="margin-left:6px" (click)="remove(r)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #none>
        <div class="empty">还没有路由规则。未命中路由表时，会自动回退到「声明支持该模型的 provider」。</div>
      </ng-template>
    </div>
  `,
})
export class RoutesComponent implements OnInit {
  readonly routes = signal<Route[]>([]);
  readonly providers = signal<Provider[]>([]);
  readonly error = signal('');
  readonly editing = signal(false);
  readonly saving = signal(false);
  editingModel = '';

  form: Route = { model: '', strategy: 'failover', targets: [], remark: '' };

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
    this.api.listProviders().subscribe({
      next: (r) => this.providers.set(r.providers ?? []),
      error: () => {},
    });
  }

  load(): void {
    this.api.listRoutes().subscribe({
      next: (r) => this.routes.set(r.routes ?? []),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  startNew(): void {
    this.form = { model: '', strategy: 'failover', targets: [this.blankTarget()], remark: '' };
    this.editingModel = '';
    this.editing.set(true);
  }

  blankTarget(): RouteTarget {
    return { provider_id: '', model: '', weight: 100, priority: 10 };
  }

  edit(r: Route): void {
    this.form = JSON.parse(JSON.stringify(r));
    this.editingModel = r.model;
    this.editing.set(true);
  }

  @HostListener('document:keydown.escape')
  onEsc(): void {
    if (this.editing() && !this.saving()) this.cancel();
  }

  cancel(): void {
    this.editing.set(false);
  }

  addTarget(): void {
    this.form.targets.push(this.blankTarget());
  }

  removeTarget(i: number): void {
    this.form.targets.splice(i, 1);
  }

  save(): void {
    this.saving.set(true);
    this.api.saveRoute(this.form).subscribe({
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

  remove(r: Route): void {
    if (!confirm(`删除路由 ${r.model}？`)) return;
    this.api.deleteRoute(r.model).subscribe({
      next: () => this.load(),
      error: (e: Error) => this.error.set(e.message),
    });
  }
}
