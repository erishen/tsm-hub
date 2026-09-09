import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { McpServer, ToolInfo } from './models';

@Component({
  selector: 'app-mcps',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>MCP · 工具池</h1>
        <div class="sub">stdio 型 MCP server 注册进网关 agent 工具池（mcp_&lt;server&gt;_&lt;tool&gt;），工具由网关服务端执行</div>
      </div>
      <button class="primary" (click)="openEdit(null)">+ 添加 MCP Server</button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>
    <div class="banner ok" *ngIf="saved()">{{ saved() }}</div>

    <div class="card">
      <h2>MCP Servers（{{ mcps().length }}）</h2>
      <table class="tbl">
        <thead>
          <tr>
            <th>名称</th>
            <th>命令</th>
            <th>参数</th>
            <th>状态</th>
            <th>工具</th>
            <th style="width:150px">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let m of mcps()">
            <td><span class="mono">{{ m.name }}</span></td>
            <td><span class="mono">{{ m.command }}</span></td>
            <td><span class="mono small">{{ (m.args || []).join(' ') || '—' }}</span></td>
            <td>
              <span class="badge" [class.ok]="m.connected" [class.err]="!m.connected">
                {{ m.connected ? '已连接' : '未连接' }}
              </span>
            </td>
            <td>
              <span class="mono small">{{ m.tools.length ? m.tools.join(', ') : '—' }}</span>
            </td>
            <td>
              <div style="display:flex;gap:6px">
                <button class="small" (click)="openEdit(m)">编辑</button>
                <button class="small danger" (click)="remove(m)">删除</button>
              </div>
            </td>
          </tr>
          <tr *ngIf="!mcps().length">
            <td colspan="6" class="empty">未配置 MCP server。示例：npx &#64;modelcontextprotocol/server-fetch</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h2>网关工具池（{{ tools().length }}）</h2>
      <div class="muted small" style="margin-bottom:10px">
        客户端不传 tools 时，网关自动附加以下工具并在服务端执行；点击「刷新」可重新探测 MCP 工具。
      </div>
      <div style="display:flex;gap:8px;margin-bottom:10px">
        <button class="small" (click)="refreshTools()">刷新</button>
        <button class="small" (click)="loadTools()">加载</button>
      </div>
      <div class="tool-grid" *ngIf="tools().length; else noTools">
        <div class="tool-card" *ngFor="let t of tools()">
          <div class="tool-name">
            <span class="mono">{{ t.name }}</span>
            <span class="badge" [class.ok]="t.source.startsWith('mcp:')">{{ srcLabel(t.source) }}</span>
          </div>
          <div class="muted tool-desc">{{ t.description || '（无描述）' }}</div>
        </div>
      </div>
      <ng-template #noTools><div class="empty">工具池为空</div></ng-template>
    </div>

    <!-- 编辑弹窗 -->
    <div class="modal-mask" *ngIf="editing()" (click)="closeEdit()">
      <div class="modal" (click)="$event.stopPropagation()">
        <h2>{{ editing()!.mode === 'edit' ? '编辑 MCP Server' : '添加 MCP Server' }}</h2>
        <div class="sub" *ngIf="editing()!.mode === 'edit'">修改后立即重建连接，配置持久化到 settings.mcps</div>

        <label>名称 <span class="req">*</span></label>
        <input [(ngModel)]="form.name" placeholder="如 fetch / fs / demo" [disabled]="editing()!.mode === 'edit'" />
        <div class="muted small">唯一标识；工具名将形如 mcp_&lt;名称&gt;_&lt;tool&gt;</div>

        <label>命令 <span class="req">*</span></label>
        <input [(ngModel)]="form.command" placeholder="如 npx / python3 / node" />

        <label>参数（空格分隔）</label>
        <input [(ngModel)]="form.argsText" placeholder="如 -y @modelcontextprotocol/server-fetch" />

        <label>环境变量（可选，KEY=VALUE 每行一个）</label>
        <textarea [(ngModel)]="form.envText" rows="2" placeholder="如 MY_TOKEN=abc"></textarea>

        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button (click)="closeEdit()">取消</button>
          <button class="primary" (click)="save()" [disabled]="saving()">{{ saving() ? '保存中…' : '保存' }}</button>
        </div>
      </div>
    </div>
  `,
  styles: [`
    .tool-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(260px,1fr)); gap:10px; }
    .tool-card { border:1px solid var(--border,#e4e3dd); border-radius:10px; padding:10px 12px; }
    .tool-name { display:flex; align-items:center; gap:8px; font-weight:600; }
    .tool-desc { margin-top:4px; font-size:12px; line-height:1.45; }
    .modal-mask { position:fixed; inset:0; background:rgba(0,0,0,.35); display:flex; align-items:center; justify-content:center; z-index:100; }
    .modal { background:#fff; border-radius:14px; padding:20px 22px; width:440px; max-width:92vw; max-height:86vh; overflow:auto; }
    .modal label { display:block; margin:12px 0 4px; font-size:13px; font-weight:600; }
    .modal input, .modal textarea { width:100%; box-sizing:border-box; }
    .req { color:#d33; }
  `],
})
export class McpsComponent implements OnInit {
  mcps = signal<McpServer[]>([]);
  tools = signal<ToolInfo[]>([]);
  error = signal('');
  saved = signal('');
  editing = signal<{ mode: 'add' | 'edit'; server?: McpServer } | null>(null);
  saving = signal(false);
  form = { name: '', command: '', argsText: '', envText: '' };

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
    this.loadTools();
  }

  load(): void {
    this.api.listMcps().subscribe({
      next: (r) => this.mcps.set(r.mcps || []),
      error: (e) => this.error.set(e.error?.error?.message || '加载 MCP 配置失败'),
    });
  }

  loadTools(): void {
    this.api.listTools().subscribe({
      next: (r) => this.tools.set(r.tools || []),
      error: (e) => this.error.set(e.error?.error?.message || '加载工具池失败'),
    });
  }

  refreshTools(): void {
    this.error.set('');
    this.saved.set('');
    this.api.listTools().subscribe({
      next: (r) => {
        this.tools.set(r.tools || []);
        this.saved.set('工具池已刷新');
        this.load();
      },
      error: (e) => this.error.set(e.error?.error?.message || '刷新失败'),
    });
  }

  openEdit(m: McpServer | null): void {
    this.error.set('');
    this.saved.set('');
    this.editing.set(m ? { mode: 'edit', server: m } : { mode: 'add' });
    this.form = {
      name: m?.name || '',
      command: m?.command || '',
      argsText: (m?.args || []).join(' '),
      envText: Object.entries(m?.env || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
    };
  }

  closeEdit(): void {
    this.editing.set(null);
  }

  save(): void {
    const name = this.form.name.trim();
    const command = this.form.command.trim();
    if (!name || !command) {
      this.error.set('名称和命令必填');
      return;
    }
    this.saving.set(true);
    const args = this.form.argsText.trim() ? this.form.argsText.trim().split(/\s+/) : [];
    const env: Record<string, string> = {};
    for (const line of this.form.envText.split('\n')) {
      const l = line.trim();
      if (!l) continue;
      const i = l.indexOf('=');
      if (i <= 0) continue;
      env[l.slice(0, i).trim()] = l.slice(i + 1).trim();
    }
    this.api.saveMcp(name, { command, args, env }).subscribe({
      next: () => {
        this.saving.set(false);
        this.editing.set(null);
        this.saved.set('已保存，MCP server 已重建连接');
        this.load();
        this.loadTools();
      },
      error: (e) => {
        this.saving.set(false);
        this.error.set(e.error?.error?.message || '保存失败');
      },
    });
  }

  remove(m: McpServer): void {
    if (!confirm(`删除 MCP server「${m.name}」？`)) return;
    this.api.deleteMcp(m.name).subscribe({
      next: () => {
        this.saved.set('已删除');
        this.load();
        this.loadTools();
      },
      error: (e) => this.error.set(e.error?.error?.message || '删除失败'),
    });
  }

  srcLabel(src: string): string {
    if (src === 'builtin') return '内置';
    if (src === 'builtin-conditional') return '条件';
    if (src.startsWith('mcp:')) return src.slice(4);
    return src;
  }
}
