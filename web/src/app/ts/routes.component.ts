import { Component, OnInit, signal, HostListener } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { Provider, Route, RouteTarget } from './models';

@Component({
  selector: 'app-routes',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/routes.component.html',
})
export class RoutesComponent implements OnInit, OnDestroy {
  readonly routes = signal<Route[]>([]);
  readonly providers = signal<Provider[]>([]);
  readonly error = signal('');
  readonly loading = signal(true);
  readonly editing = signal(false);
  readonly saving = signal(false);
  editingModel = '';

  form: Route = { model: '', strategy: 'failover', targets: [], remark: '' };

  // ---- 批量操作 ----
  private selected = new Set<string>();
  selectedCount(): number { return this.selected.size; }
  isSelected(model: string): boolean { return this.selected.has(model); }
  allSelected(): boolean { return this.routes().length > 0 && this.selected.size === this.routes().length; }
  toggleSelect(model: string, ev: Event): void {
    const checked = (ev.target as HTMLInputElement).checked;
    if (checked) this.selected.add(model); else this.selected.delete(model);
  }
  toggleAll(ev: Event): void {
    const checked = (ev.target as HTMLInputElement).checked;
    if (checked) this.routes().forEach((r) => this.selected.add(r.model || '*'));
    else this.selected.clear();
  }
  clearSelection(): void { this.selected.clear(); }

  /** 批量删除：逐个调用 deleteRoute API，完成后刷新列表。 */
  batchDelete(): void {
    const models = [...this.selected];
    if (!models.length) return;
    if (!confirm(`确认删除选中的 ${models.length} 条路由规则？此操作不可恢复。`)) return;
    let done = 0;
    models.forEach((model) => {
      this.api.deleteRoute(model).subscribe({
        next: () => { if (++done === models.length) { this.selected.clear(); this.load(); } },
        error: () => { if (++done === models.length) { this.selected.clear(); this.load(); } },
      });
    });
  }

  
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
    this.api.listProviders().subscribe({
      next: (r) => this.providers.set(r.providers ?? []),
      error: () => {},
    });
  }

  load(): void {
    this.loading.set(true);
    this.api.listRoutes().subscribe({
      next: (r) => { this.routes.set(r.routes ?? []); this.loading.set(false); },
      error: (e: Error) => { this.error.set(e.message); this.loading.set(false); },
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
    const label = r.model || '*(通配)';
    if (!confirm(`删除路由 ${label}？`)) return;
    this.api.deleteRoute(r.model).subscribe({
      next: () => this.load(),
      error: (e: Error) => this.error.set(e.message),
    });
  }
}
