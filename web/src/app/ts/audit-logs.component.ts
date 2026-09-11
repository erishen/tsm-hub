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
  templateUrl: '../html/audit-logs.component.html',
  styleUrls: ['../css/audit-logs.component.css'],
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
