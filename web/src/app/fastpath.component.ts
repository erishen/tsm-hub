import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';

interface FastMatcher { name: string; trigger: string; desc: string; builtin: boolean }
interface FastPlugin { name: string; trigger: string; source: string; promoted: boolean; mtime: number; size: number }

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
              <span class="tag" [class.green]="p.promoted">{{ p.promoted ? '已晋升' : '运行时' }}</span>
            </td>
            <td>
              <button class="small" *ngIf="!p.promoted" (click)="promote(p)">晋升</button>
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
    </div>
  `,
  styles: [`
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
    .row { display:flex; align-items:center; gap:10px; }
    .tag { display:inline-block; padding:2px 8px; border-radius:10px; background:#f0f0f0; font-size:12px; }
    .tag.green { background:#e6f7e9; color:#2e7d32; }
    .code { background:#1e1e1e; color:#d4d4d4; padding:10px; border-radius:8px; font-size:12px; overflow-x:auto; white-space:pre-wrap; }
  `]
})
export class FastpathComponent implements OnInit {
  builtin: FastMatcher[] = [];
  plugins: FastPlugin[] = [];
  testQuery = '';
  testResult: string | null = null;
  testMethod = '';
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
          this.genMsg = '模型认为该问题无法用纯代码确定性解决。';
        }
        this.reload();
      },
      error: (e: Error) => {
        this.testing = false;
        this.genMsg = '生成失败：' + e.message;
      },
    });
  }

  promote(p: FastPlugin): void {
    this.api.promoteFastpath(p.name).subscribe({
      next: () => this.reload(),
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
