import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';

interface AuditLog {
  id: number;
  ts: string;
  action: string;
  object: string;
  object_id: string;
  detail: string;
  operator: string;
  client_ip: string;
  user_agent: string;
}

@Component({
  selector: 'app-audit-logs',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page">
      <div class="page-head">
        <h1>审计日志</h1>
        <p class="sub">记录所有管理操作（创建/修改/删除），用于合规追溯。默认保留 90 天。</p>
      </div>

      <div class="toolbar">
        <div class="filters">
          <select [(ngModel)]="filterObject" (change)="load()" class="select">
            <option value="">全部对象</option>
            <option value="provider">Provider</option>
            <option value="route">路由表</option>
            <option value="key">Token Key</option>
            <option value="mcp">MCP</option>
            <option value="settings">设置</option>
            <option value="usage">用量</option>
            <option value="memory">记忆</option>
            <option value="fastpath">快路径</option>
          </select>
          <select [(ngModel)]="filterAction" (change)="load()" class="select">
            <option value="">全部操作</option>
            <option value="create">创建</option>
            <option value="update">更新</option>
            <option value="upsert">新增/更新</option>
            <option value="delete">删除</option>
            <option value="toggle">启停</option>
            <option value="clear">清空</option>
          </select>
          <input [(ngModel)]="filterObjectID" (keyup.enter)="load()" placeholder="对象 ID 搜索" class="input" />
          <button (click)="load()" class="primary">查询</button>
        </div>
        <div class="meta">
          <span *ngIf="!loading()">共 {{ total() }} 条</span>
          <span *ngIf="loading()" class="muted">加载中…</span>
        </div>
      </div>

      <div class="table-wrap" *ngIf="!loading() && logs().length > 0">
        <table class="table">
          <thead>
            <tr>
              <th style="width:160px">时间</th>
              <th style="width:80px">操作</th>
              <th style="width:90px">对象</th>
              <th style="width:140px">对象 ID</th>
              <th>详情</th>
              <th style="width:100px">操作者</th>
              <th style="width:120px">IP</th>
            </tr>
          </thead>
          <tbody>
            <tr *ngFor="let l of logs()">
              <td class="mono small">{{ formatTime(l.ts) }}</td>
              <td><span class="badge" [ngClass]="actionBadge(l.action)">{{ l.action }}</span></td>
              <td>{{ l.object }}</td>
              <td class="mono small" [title]="l.object_id">{{ l.object_id || '-' }}</td>
              <td class="detail" [title]="l.detail">{{ l.detail || '-' }}</td>
              <td class="small muted">{{ l.operator }}</td>
              <td class="mono small">{{ l.client_ip }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="pagination" *ngIf="total() > limit">
        <button (click)="prevPage()" [disabled]="offset() === 0" class="small">上一页</button>
        <span class="muted">第 {{ offset() / limit + 1 }} / {{ Math.ceil(total() / limit) }} 页</span>
        <button (click)="nextPage()" [disabled]="offset() + limit >= total()" class="small">下一页</button>
      </div>

      <div class="empty" *ngIf="!loading() && logs().length === 0">
        <div class="empty-icon">📋</div>
        <div class="empty-title">暂无审计日志</div>
        <div class="empty-desc">执行管理操作（新增/修改/删除 Provider、路由、Key 等）后会自动记录</div>
      </div>
    </div>
  `,
  styles: [`
    .page { padding: 24px; max-width: 1400px; margin: 0 auto; }
    .page-head { margin-bottom: 20px; }
    .page-head h1 { margin: 0 0 4px; font-size: 22px; font-weight: 600; }
    .page-head .sub { margin: 0; color: var(--text-secondary, #6b7280); font-size: 13px; }
    .toolbar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; gap: 12px; flex-wrap: wrap; }
    .filters { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .select, .input { padding: 6px 10px; border: 1px solid var(--border-color, #e5e7eb); border-radius: 6px; font-size: 13px; background: var(--card-color, #fff); color: var(--text-color, #111); }
    .input { width: 160px; }
    .primary { padding: 6px 14px; background: var(--accent, #3b82f6); color: #fff; border: none; border-radius: 6px; cursor: pointer; font-size: 13px; }
    .primary:hover { opacity: 0.9; }
    .small { padding: 4px 10px; font-size: 12px; }
    .meta { font-size: 13px; color: var(--text-secondary, #6b7280); }
    .muted { color: var(--text-secondary, #6b7280); }
    .table-wrap { overflow-x: auto; border: 1px solid var(--border-color, #e5e7eb); border-radius: 8px; }
    .table { width: 100%; border-collapse: collapse; font-size: 13px; }
    .table th { text-align: left; padding: 10px 12px; background: var(--bg-secondary, #f9fafb); border-bottom: 1px solid var(--border-color, #e5e7eb); font-weight: 600; white-space: nowrap; }
    .table td { padding: 8px 12px; border-bottom: 1px solid var(--border-color, #f3f4f6); vertical-align: top; }
    .table tr:hover { background: var(--bg-hover, #f9fafb); }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
    .small { font-size: 12px; }
    .detail { max-width: 400px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--text-secondary, #6b7280); }
    .badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 500; }
    .badge-create { background: #dcfce7; color: #166534; }
    .badge-update, .badge-upsert { background: #dbeafe; color: #1e40af; }
    .badge-delete { background: #fee2e2; color: #991b1b; }
    .badge-toggle { background: #fef3c7; color: #92400e; }
    .badge-clear { background: #f3e8ff; color: #6b21a8; }
    .pagination { display: flex; justify-content: center; align-items: center; gap: 16px; margin-top: 16px; }
    .pagination button { padding: 4px 12px; border: 1px solid var(--border-color, #e5e7eb); border-radius: 6px; background: var(--card-color, #fff); cursor: pointer; font-size: 12px; }
    .pagination button:disabled { opacity: 0.5; cursor: not-allowed; }
    .empty { text-align: center; padding: 60px 20px; }
    .empty-icon { font-size: 40px; margin-bottom: 12px; }
    .empty-title { font-size: 16px; font-weight: 600; margin-bottom: 6px; }
    .empty-desc { font-size: 13px; color: var(--text-secondary, #6b7280); }
  `],
})
export class AuditLogsComponent implements OnInit {
  logs = signal<AuditLog[]>([]);
  total = signal(0);
  loading = signal(false);
  limit = 50;
  offset = signal(0);
  filterObject = '';
  filterAction = '';
  filterObjectID = '';

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.loading.set(true);
    this.offset.set(0);
    this.fetch();
  }

  fetch(): void {
    this.loading.set(true);
    this.api.getAuditLogs({
      object: this.filterObject || undefined,
      action: this.filterAction || undefined,
      object_id: this.filterObjectID || undefined,
      limit: this.limit,
      offset: this.offset(),
    }).subscribe({
      next: (r) => {
        this.logs.set(r.logs || []);
        this.total.set(r.total || 0);
        this.loading.set(false);
      },
      error: () => {
        this.logs.set([]);
        this.total.set(0);
        this.loading.set(false);
      },
    });
  }

  prevPage(): void {
    if (this.offset() >= this.limit) {
      this.offset.set(this.offset() - this.limit);
      this.fetch();
    }
  }

  nextPage(): void {
    if (this.offset() + this.limit < this.total()) {
      this.offset.set(this.offset() + this.limit);
      this.fetch();
    }
  }

  formatTime(ts: string): string {
    try {
      const d = new Date(ts);
      return d.toLocaleString('zh-CN', { hour12: false });
    } catch {
      return ts;
    }
  }

  actionBadge(action: string): string {
    switch (action) {
      case 'create': return 'badge-create';
      case 'update':
      case 'upsert': return 'badge-update';
      case 'delete': return 'badge-delete';
      case 'toggle': return 'badge-toggle';
      case 'clear': return 'badge-clear';
      default: return '';
    }
  }

  protected readonly Math = Math;
}
