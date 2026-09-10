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
  template: `
    <div class="page-head">
      <div>
        <h1>工具池</h1>
        <div class="sub">网关 agent 可执行的全部工具（内置 + 条件 + MCP + 晋升工具），可一键测试</div>
      </div>
      <div style="display:flex;gap:8px">
        <button class="small" (click)="refreshTools()">刷新</button>
        <button class="small" (click)="loadTools()">加载</button>
      </div>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>
    <div class="banner ok" *ngIf="saved()">{{ saved() }}</div>

    <div class="card">
      <h2>网关工具池（{{ tools().length }}）</h2>
      <div class="muted small" style="margin-bottom:10px">
        客户端不传 tools 时，网关自动附加以下工具并在服务端执行；MCP 工具由常驻连接提供（管理见 MCP 页），外部自创工具录用见「监控」页。
      </div>
      <div class="tool-grid" *ngIf="loadingTools()">
        <div class="tool-card" style="grid-column:auto"><div class="skel-bar" style="width:55%"></div><div class="skel-bar" style="width:90%;margin-top:8px"></div></div>
        <div class="tool-card"><div class="skel-bar" style="width:45%"></div><div class="skel-bar" style="width:80%;margin-top:8px"></div></div>
        <div class="tool-card"><div class="skel-bar" style="width:60%"></div><div class="skel-bar" style="width:85%;margin-top:8px"></div></div>
        <div class="muted small" style="grid-column:1/-1">加载工具池中（MCP 进程可能较慢）…</div>
      </div>
      <div class="tool-grid" *ngIf="!loadingTools() && tools().length; else noTools">
        <div class="tool-card" *ngFor="let t of tools()">
          <div class="tool-name">
            <span class="mono tname" [title]="t.name">{{ t.name }}</span>
            <span class="badge" [class.ok]="t.source.startsWith('mcp:')">{{ srcLabel(t.source) }}</span>
            <button class="small" style="margin-left:auto" (click)="openTest(t)" *ngIf="!t.source.startsWith('mcp:') || t.parameters">测试</button>
          </div>
          <div class="muted tool-desc">{{ t.description || '（无描述）' }}</div>
        </div>
      </div>
      <ng-template #noTools>
        <div class="empty" *ngIf="error() && !tools().length">
          <span class="err-text">{{ error() }}</span>
          <button class="small" style="margin-left:10px" (click)="loadTools()">重试</button>
        </div>
        <div class="empty" *ngIf="!error()">工具池为空</div>
      </ng-template>
    </div>

    <!-- 测试工具弹窗 -->
    <div class="modal-backdrop" *ngIf="testing()" (click)="closeTest()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">▶</div>
          <div class="modal-titles">
            <h2>测试工具 <span class="mono">{{ testing()!.name }}</span></h2>
            <div class="sub">{{ testing()!.description }}</div>
          </div>
          <button class="icon" (click)="closeTest()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div *ngIf="!paramFields().length" class="muted" style="margin-top:8px">该工具无需参数。</div>
          <ng-container *ngFor="let f of paramFields()">
            <label>
              {{ f.key }}<span class="req" *ngIf="f.required"> *</span>
              <span class="muted small" *ngIf="f.desc"> — {{ f.desc }}</span>
            </label>
            <select *ngIf="f.enum && f.enum.length" [(ngModel)]="testArgs[f.key]">
              <option *ngFor="let e of f.enum" [value]="e">{{ e }}</option>
            </select>
            <input *ngIf="!f.enum || !f.enum.length" [(ngModel)]="testArgs[f.key]"
                   [type]="f.type === 'number' ? 'number' : 'text'"
                   [placeholder]="f.type === 'boolean' ? 'true / false' : ''" />
          </ng-container>

          <div class="test-result" *ngIf="testResult() !== null">
            <div class="muted small" style="margin-bottom:4px">返回结果：</div>
            <pre>{{ testResult() }}</pre>
          </div>
          <div class="banner error" *ngIf="testError()">{{ testError() }}</div>
        </div>
        <div class="modal-foot">
          <button (click)="closeTest()">关闭</button>
          <button class="primary" (click)="runTest()" [disabled]="testRunning()">{{ testRunning() ? '调用中…' : '运行' }}</button>
        </div>
      </div>
    </div>
  `,
  styles: [`
    .tool-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(260px,1fr)); gap:10px; }
    .tool-card { border:1px solid var(--border,#e4e3dd); border-radius:10px; padding:10px 12px; }
    .tool-name { display:flex; align-items:center; gap:8px; font-weight:600; }
    .tool-desc { margin-top:4px; font-size:12px; line-height:1.45; }
    .tool-grid { grid-template-columns: repeat(auto-fill,minmax(240px,1fr)); }
    .tool-card { min-width: 0; overflow: hidden; }
    .tool-name .tname { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .tool-name .badge, .tool-name button { flex: 0 0 auto; white-space: nowrap; }
    .tool-desc { word-break: break-word; }
    .test-result { margin-top:12px; }
    .test-result pre { background:#f6f5f1; border:1px solid var(--border,#e4e3dd); border-radius:8px; padding:10px; font-size:12px; white-space:pre-wrap; word-break:break-all; max-height:220px; overflow:auto; margin:0; }
    .req { color:#d33; }
    .skel-bar { height:12px; border-radius:6px;
      background:linear-gradient(90deg,#efece4 25%,#f8f6f1 37%,#efece4 63%);
      background-size:400% 100%; animation:skel 1.2s ease infinite; }
    @keyframes skel { 0% {background-position:100% 0;} 100% {background-position:0 0;} }
    .err-text { color:#d33; font-size:13px; }
  `],
})
export class ToolsComponent implements OnInit, OnDestroy {
  tools = signal<ToolInfo[]>([]);
  error = signal('');
  saved = signal('');
  loadingTools = signal(true);
  testing = signal<ToolInfo | null>(null);
  testArgs: Record<string, string> = {};
  testResult = signal<string | null>(null);
  testError = signal('');
  testRunning = signal(false);

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.testing()));
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
