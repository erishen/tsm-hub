import { Component, OnInit, signal, effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';

interface FastMatcher { name: string; trigger: string; desc: string; builtin: boolean }
interface FastPlugin { name: string; trigger: string; source: string; promoted: boolean; mode?: string; mtime: number; size: number }

@Component({
  selector: 'app-fastpath',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './fastpath.component.html',
  styleUrls: ['./fastpath.component.css']
})
export class FastpathComponent implements OnInit, OnDestroy {
  builtin: FastMatcher[] = [];
  plugins: FastPlugin[] = [];
  readonly loading = signal(true);
  testQuery = '';
  testResult: string | null = null;
  testMethod = '';
  chain: any[] | null = null;
  testing = false;
  genMsg = '';

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.promoteTarget()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.reload();
  }

  reload(): void {
    this.loading.set(true);
    this.api.getFastpath().subscribe({
      next: (d: any) => {
        this.builtin = d.builtin || [];
        this.plugins = d.plugins || [];
        this.loading.set(false);
      },
      error: (e: Error) => { console.error(e); this.loading.set(false); },
    });
  }

  test(): void {
    if (!this.testQuery) return;
    this.testing = true;
    this.testResult = null;
    this.testMethod = '';
    this.genMsg = '';
    this.api.fastpathGenerate(this.testQuery).subscribe({
      next: (d: any) => {
        this.testResult = d.answer;
        this.testMethod = d.method || '';
        this.chain = d.chain || null;
        this.testing = false;
        if (d.method && d.method !== 'codegen' && this.testQuery) {
          this.reload();
        }
      },
      error: (e: Error) => {
        this.testResult = '测试失败：' + e.message;
        this.testing = false;
      },
    });
  }

  generate(): void {
    if (!this.testQuery) return;
    this.testing = true;
    this.genMsg = '';
    this.api.fastpathGenerate(this.testQuery).subscribe({
      next: (d: any) => {
        this.testing = false;
        if (d.answer) {
          this.genMsg = '已生成并验证：' + d.answer;
        } else {
          const cg = (d.chain || []).find((x: any) => x.stage === 'codegen');
          this.genMsg = cg?.detail || '模型认为该问题无法用纯代码确定性解决。';
        }
        this.reload();
      },
      error: (e: Error) => {
        this.testing = false;
        this.genMsg = '生成失败：' + e.message;
      },
    });
  }

  promoteTarget = signal<FastPlugin | null>(null);
  promoteMode = 'fastpath';

  modeLabel(mode?: string): string {
    if (!mode || mode === 'fastpath') return '已晋升 · 拦截';
    if (mode === 'tool') return '已晋升 · 工具';
    return '已晋升 · 两者';
  }

  openPromote(p: FastPlugin): void {
    this.promoteTarget.set(p);
    this.promoteMode = 'fastpath';
  }

  closePromote(): void {
    this.promoteTarget.set(null);
  }

  doPromote(): void {
    const p = this.promoteTarget();
    if (!p) return;
    this.genMsg = '';
    this.api.promoteFastpath(p.name, this.promoteMode).subscribe({
      next: () => { this.promoteTarget.set(null); this.reload(); },
      error: (e: Error) => (this.genMsg = '晋升失败：' + e.message),
    });
  }

  remove(p: FastPlugin): void {
    if (!confirm('删除插件 ' + p.name + '？')) return;
    this.api.deleteFastpath(p.name).subscribe({
      next: () => this.reload(),
      error: (e: Error) => (this.genMsg = '删除失败：' + e.message),
    });
  }
}
