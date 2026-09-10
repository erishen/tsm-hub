import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ApiService } from './api.service';

@Component({
  selector: 'app-memory',
  standalone: true,
  imports: [CommonModule],
  template: `
    <div class="page-head">
      <div>
        <h1>会话记忆</h1>
        <div class="sub">内置 remember/recall：按 Key ID 隔离命名空间，SQLite 持久化（data/memory.db），重启保留</div>
      </div>
      <button class="danger" (click)="clearMem()" [disabled]="!memory().length">清空全部记忆</button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="card">
      <h2>记忆条目（{{ memory().length }}）</h2>
      <table class="tbl" *ngIf="memory().length; else noneMem">
        <thead>
          <tr>
            <th>作用域（Key ID）</th><th>Key</th><th>内容</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let e of memory()">
            <td class="col-name"><span class="mono small">{{ memScope(e.ns) }}</span></td>
            <td class="col-name"><span class="mono small">{{ memKey(e.ns) }}</span></td>
            <td><span class="mono small ellipsis" [title]="e.value">{{ e.value }}</span></td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneMem><div class="empty">暂无会话记忆 —— 调用方可经 remember 写入、recall 取回</div></ng-template>
    </div>

    <div class="card">
      <h2>说明</h2>
      <ul class="muted small" style="line-height:1.8;padding-left:18px;margin:0">
        <li><span class="mono">remember</span>（key, value）：写入一条记忆，自动带上调用方 Key ID 命名空间</li>
        <li><span class="mono">recall</span>（key）：取回自己写入的记忆，不同 Key 之间互相隔离</li>
        <li>存储为 SQLite（<span class="mono">data/memory.db</span>），网关重启后记忆保留</li>
        <li>持久化长期记忆另有 MCP memory 图谱（MCP 页管理）与 serena 项目记忆</li>
      </ul>
    </div>
  `,
  styles: [`
    table.tbl { table-layout: fixed; }
    .ellipsis { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .col-name { width: 30%; }
  `],
})
export class MemoryComponent implements OnInit {
  memory = signal<{ ns: string; value: string }[]>([]);
  error = signal('');

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.loadMemory();
  }

  loadMemory(): void {
    this.api.listMemory().subscribe({
      next: (r) => this.memory.set(r.entries || []),
      error: (e: Error) => this.error.set('加载失败：' + e.message),
    });
  }

  clearMem(): void {
    if (!confirm('清空全部会话记忆（remember/recall 数据）？')) return;
    this.api.clearMemory().subscribe({
      next: () => this.loadMemory(),
      error: (e: Error) => this.error.set('清空失败：' + e.message),
    });
  }

  /** 从 ns "mem:<keyID>:<key>" 解析 keyID。 */
  memScope(ns: string): string {
    const p = ns.split(':');
    return p.length >= 3 ? p[1] : ns;
  }

  /** 从 ns 解析 key。 */
  memKey(ns: string): string {
    const p = ns.split(':');
    return p.length >= 3 ? p.slice(2).join(':') : ns;
  }
}
