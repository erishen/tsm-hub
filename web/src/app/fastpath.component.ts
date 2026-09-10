import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';

interface FastMatcher { name: string; trigger: string; desc: string; builtin: boolean }
interface FastPlugin { name: string; trigger: string; source: string; promoted: boolean; mode?: string; mtime: number; size: number }

@Component({
  selector: 'app-fastpath',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <h2>快路径 · Fast Path</h2>
    <p class="muted">确定性快路径：能用纯代码回答的问题（算术 / 时间 / 日期 / 单位换算 / 统计 / 进制 / 字数）直接返回，
      零模型调用、零上游消耗。内置匹配器未命中时，可让模型生成 JS 检测器（codegen）并持久化为插件复用；
      插件可一键「晋升」为正式检测器。</p>

    <div class="card" style="margin-bottom:16px">
      <h3>试一下</h3>
      <div class="row">
        <input style="flex:1" [(ngModel)]="testQuery" placeholder="输入问题，如：23*47+5 等于多少 / 把字符串 abcdef 反转输出"
               (keyup.enter)="test()" />
        <button class="primary" (click)="test()" [disabled]="testing || !testQuery">测试</button>
      </div>
      <div class="banner success" *ngIf="testResult !== null" style="margin-top:10px">
        <b>{{ testMethod || '未命中' }}</b>：{{ testResult || '无法用快路径回答（可点上方按钮生成检测器）' }}
      </div>
      <div *ngIf="chain && chain.length" style="margin-top:10px;font-size:12px">
        <div class="muted" style="margin-bottom:4px">尝试链：</div>
        <div *ngFor="let c of chain" style="display:flex;gap:8px;align-items:baseline;margin:2px 0">
          <span class="tag" [class.green]="c.hit">{{ c.stage }} · {{ c.hit ? '命中' : '未命中' }}</span>
          <span class="muted" [style.word-break]="'break-all'">{{ c.detail || '—' }}</span>
        </div>
      </div>
    </div>

    <div class="card" style="margin-bottom:16px">
      <h3>内置匹配器（{{ builtin.length }}）</h3>
      <table>
        <thead><tr><th>匹配器</th><th>触发示例</th><th>说明</th></tr></thead>
        <tbody>
          <tr *ngFor="let m of builtin">
            <td class="mono">{{ m.name }}</td>
            <td>{{ m.trigger }}</td>
            <td class="muted">{{ m.desc }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <div class="row" style="justify-content:space-between;align-items:center">
        <h3 style="margin:0">插件（{{ plugins.length }}）</h3>
        <button class="small" (click)="generate()" [disabled]="testing || !testQuery">对上方问题生成检测器</button>
      </div>
      <div class="banner error" *ngIf="genMsg">{{ genMsg }}</div>
      <table>
        <thead><tr><th>名称</th><th>触发（trigger）</th><th>状态</th><th style="width:220px">操作</th></tr></thead>
        <tbody>
          <tr *ngFor="let p of plugins">
            <td class="mono">{{ p.name }}</td>
            <td>{{ p.trigger || '—' }}</td>
            <td>
              <span class="tag" [class.green]="p.promoted">{{ p.promoted ? modeLabel(p.mode) : '运行时' }}</span>
            </td>
            <td>
              <button class="small" *ngIf="!p.promoted" (click)="openPromote(p)">晋升</button>
              <button class="small danger" (click)="remove(p)">删除</button>
            </td>
          </tr>
          <tr *ngIf="!plugins.length">
            <td colspan="4" class="muted">暂无插件。输入一个内置匹配器覆盖不到的问题，点「生成检测器」试试。</td>
          </tr>
        </tbody>
      </table>
      <details *ngIf="plugins.length" style="margin-top:10px">
        <summary class="muted" style="cursor:pointer">查看插件源码</summary>
        <pre class="code" *ngFor="let p of plugins">{{ p.source }}</pre>
      </details>

      <!-- 晋升模式选择弹窗 -->
      <div class="modal-backdrop" *ngIf="promoteTarget">
        <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <span class="modal-icon">⚡</span>
          <div class="modal-titles">
            <h2>晋升插件</h2>
            <div class="sub">{{ promoteTarget.name }} · {{ promoteTarget.trigger || '无触发词' }}</div>
          </div>
          <button class="icon" (click)="closePromote()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <div class="muted" style="font-size:12px;margin-bottom:12px">
              选择晋升方式：
            </div>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="promoteMode" value="fastpath" />
              <span><b>纯 fastpath（推荐）</b><span class="muted" style="display:block;font-size:12px">请求前精确匹配直接返回，零模型调用、零成本</span></span>
            </label>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="promoteMode" value="tool" />
              <span><b>注册为工具</b><span class="muted" style="display:block;font-size:12px">进工具池，可被 LLM 主动调用、外部 /v1/tools 可查可声明</span></span>
            </label>
            <label class="radio-row">
              <input type="radio" [(ngModel)]="promoteMode" value="both" />
              <span><b>两者都要</b><span class="muted" style="display:block;font-size:12px">既拦截又当工具（同一段逻辑双通道生效）</span></span>
            </label>
          </div>
        </div>
        <div class="modal-foot">
          <button class="small" (click)="closePromote()">取消</button>
          <button class="small primary" (click)="doPromote()">晋升</button>
        </div>
        </div>
    </div>
  `,
  styles: [`
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
    .row { display:flex; align-items:center; gap:10px; }
    .tag { display:inline-block; padding:2px 8px; border-radius:10px; background:#f0f0f0; font-size:12px; }
    .tag.green { background:#e6f7e9; color:#2e7d32; }
    .code { background:#1e1e1e; color:#d4d4d4; padding:10px; border-radius:8px; font-size:12px; overflow-x:auto; white-space:pre-wrap; }
    .radio-row { display:flex; gap:10px; align-items:flex-start; padding:10px 12px;
      border:1px solid var(--border,#e4e3dd); border-radius:10px; margin-bottom:8px; cursor:pointer; }
    .radio-row:hover { background:#faf9f5; }
    .radio-row input { margin-top:3px; }
  `]
})
export class FastpathComponent implements OnInit {
  builtin: FastMatcher[] = [];
  plugins: FastPlugin[] = [];
  testQuery = '';
  testResult: string | null = null;
  testMethod = '';
  chain: any[] | null = null;
  testing = false;
  genMsg = '';

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.reload();
  }

  reload(): void {
    this.api.getFastpath().subscribe({
      next: (d: any) => {
        this.builtin = d.builtin || [];
        this.plugins = d.plugins || [];
      },
      error: (e: Error) => console.error(e),
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

  promoteTarget: FastPlugin | null = null;
  promoteMode = 'fastpath';

  modeLabel(mode?: string): string {
    if (!mode || mode === 'fastpath') return '已晋升 · 拦截';
    if (mode === 'tool') return '已晋升 · 工具';
    return '已晋升 · 两者';
  }

  openPromote(p: FastPlugin): void {
    this.promoteTarget = p;
    this.promoteMode = 'fastpath';
  }

  closePromote(): void {
    this.promoteTarget = null;
  }

  doPromote(): void {
    const p = this.promoteTarget;
    if (!p) return;
    this.genMsg = '';
    this.api.promoteFastpath(p.name, this.promoteMode).subscribe({
      next: () => { this.promoteTarget = null; this.reload(); },
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
