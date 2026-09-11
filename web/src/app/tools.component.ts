import { Component, OnInit, signal } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { ToolInfo } from './models';

@Component({
  selector: 'app-tools',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './tools.component.html',
  styleUrls: ['./tools.component.css'],
})
export class ToolsComponent implements OnInit, OnDestroy {
  tools = signal<ToolInfo[]>([]);
  error = signal('');
  saved = signal('');
  loadingTools = signal(true);
  testing = signal<ToolInfo | null>(null);
  detailTool = signal<ToolInfo | null>(null);
  testArgs: Record<string, string> = {};
  testResult = signal<string | null>(null);
  testError = signal('');
  testRunning = signal(false);

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.testing() || this.detailTool()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.loadTools();
  }

  // 保证 loading 骨架至少可见 350ms，避免接口太快导致闪烁不可见。
  private minShown(start: number, flag: { done: () => void }): void {
    const el = Date.now() - start;
    if (el >= 350) { flag.done(); return; }
    setTimeout(flag.done, 350 - el);
  }

  loadTools(): void {
    this.loadingTools.set(true);
    const t0 = Date.now();
    this.api.listTools().subscribe({
      next: (r) => {
        this.tools.set(r.tools || []);
        this.minShown(t0, { done: () => this.loadingTools.set(false) });
      },
      error: (e) => { this.error.set(e.error?.error?.message || '加载工具池失败'); this.minShown(t0, { done: () => this.loadingTools.set(false) }); },
    });
  }

  refreshTools(): void {
    this.error.set('');
    this.saved.set('');
    this.loadingTools.set(true);
    this.api.listTools().subscribe({
      next: (r) => {
        this.tools.set(r.tools || []);
        this.loadingTools.set(false);
        this.saved.set('工具池已刷新');
      },
      error: (e) => { this.loadingTools.set(false); this.error.set(e.error?.error?.message || '刷新失败'); },
    });
  }

  openTest(t: ToolInfo): void {
    this.testError.set('');
    this.testResult.set(null);
    this.testing.set(t);
    this.testArgs = {};
    const props = t.parameters?.properties || {};
    for (const k of Object.keys(props)) {
      this.testArgs[k] = '';
    }
  }

  closeTest(): void {
    this.testing.set(null);
  }

  /** 打开工具详情弹窗：展示完整描述 + 参数列表（只读）。 */
  openDetail(t: ToolInfo): void {
    this.detailTool.set(t);
  }

  closeDetail(): void {
    this.detailTool.set(null);
  }

  /** 从详情弹窗跳转到测试弹窗：关闭详情，打开测试。 */
  openTestFromDetail(): void {
    const t = this.detailTool();
    this.closeDetail();
    if (t) this.openTest(t);
  }

  /** 详情弹窗的参数列表（基于 detailTool 而非 testing）。 */
  detailParams(): { key: string; type: string; required: boolean; desc: string; enum?: string[] }[] {
    const t = this.detailTool();
    if (!t?.parameters?.properties) return [];
    const props = t.parameters.properties;
    const req = new Set(t.parameters.required || []);
    return Object.keys(props).map((k) => ({
      key: k,
      type: props[k].type || 'string',
      required: req.has(k),
      desc: props[k].description || '',
      enum: props[k].enum,
    }));
  }

  paramFields(): { key: string; type: string; required: boolean; desc: string; enum?: string[] }[] {
    const t = this.testing();
    if (!t?.parameters?.properties) return [];
    const props = t.parameters.properties;
    const req = new Set(t.parameters.required || []);
    return Object.keys(props).map((k) => ({
      key: k,
      type: props[k].type || 'string',
      required: req.has(k),
      desc: props[k].description || '',
      enum: props[k].enum,
    }));
  }

  runTest(): void {
    const t = this.testing();
    if (!t) return;
    const args: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(this.testArgs)) {
      if (v === '') continue;
      const f = this.paramFields().find((x) => x.key === k);
      args[k] = f?.type === 'number' ? Number(v) : f?.type === 'boolean' ? v === 'true' : v;
    }
    this.testError.set('');
    this.testResult.set(null);
    this.testRunning.set(true);
    this.api.invokeTool(t.name, args).subscribe({
      next: (r: unknown) => {
        this.testRunning.set(false);
        this.testResult.set((r as { result?: string })?.result ?? JSON.stringify(r));
      },
      error: (e: Error) => {
        this.testRunning.set(false);
        this.testError.set(e.message || '调用失败');
      },
    });
  }

  srcLabel(src: string): string {
    if (src === 'builtin') return '内置';
    if (src === 'builtin-conditional') return '条件';
    if (src.startsWith('mcp:')) return src.slice(4);
    return src;
  }
}
