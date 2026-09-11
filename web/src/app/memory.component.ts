import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ApiService } from './api.service';

@Component({
  selector: 'app-memory',
  standalone: true,
  imports: [CommonModule],
  templateUrl: './memory.component.html',
  styleUrls: ['./memory.component.css'],
})
export class MemoryComponent implements OnInit {
  memory = signal<{ ns: string; value: string }[]>([]);
  error = signal('');
  readonly loading = signal(true);

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.loadMemory();
  }

  loadMemory(): void {
    this.loading.set(true);
    this.api.listMemory().subscribe({
      next: (r) => { this.memory.set(r.entries || []); this.loading.set(false); },
      error: (e: Error) => { this.error.set('加载失败：' + e.message); this.loading.set(false); },
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
