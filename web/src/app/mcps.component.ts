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
      <h2>常用模板</h2>
      <div class="muted small" style="margin-bottom:10px">
        参考 resolve-studio 的常用 MCP 接入清单。点「使用此模板」预填表单，确认路径 / Token 后保存。
      </div>
      <div class="tpl-grid">
        <div class="tpl-card" *ngFor="let t of templates">
          <div class="tpl-head">
            <span class="mono tpl-id">{{ t.id }}</span>
            <button class="small primary" (click)="useTemplate(t)">使用此模板</button>
          </div>
          <div class="muted small tpl-desc">{{ t.desc }}</div>
          <div class="tpl-needs" *ngIf="t.needs">⚠ {{ t.needs }}</div>
        </div>
      </div>
    </div>

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
            <td class="col-name"><span class="mono">{{ m.name }}</span></td>
            <td class="col-cmd"><span class="mono small ellipsis" [title]="m.transport === 'http' ? (m.url || '') : (m.command || '')">{{ m.transport === 'http' ? (m.url || '—') : (m.command || '—') }}</span></td>
            <td class="col-args"><span class="mono small ellipsis" [title]="m.transport === 'http' ? 'HTTP' : ((m.args || []).join(' ') || '')">{{ m.transport === 'http' ? 'HTTP' : ((m.args || []).join(' ') || '—') }}</span></td>
            <td class="col-status">
              <span class="badge nowrap" [class.ok]="m.connected" [class.err]="!m.connected">
                {{ m.connected ? '已连接' : '未连接' }}
              </span>
            </td>
            <td class="col-tools"><span class="mono small ellipsis" [title]="(m.tools || []).join(', ')">{{ m.tools.length ? m.tools.join(', ') : '—' }}</span></td>
            <td>
              <div style="display:flex;gap:6px">
                <button class="small" (click)="openEdit(m)">编辑</button>
                <button class="small danger" (click)="remove(m)">删除</button>
              </div>
            </td>
          </tr>
          <tr *ngIf="loadingMcps()">
            <td colspan="6" style="padding:6px 0">
              <div class="skel-row"></div>
              <div class="skel-row" style="width:72%"></div>
            </td>
          </tr>
          <tr *ngIf="!loadingMcps() && !mcps().length">
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
      <ng-template #noTools><div class="empty">工具池为空</div></ng-template>
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

    <!-- 编辑弹窗 -->
    <div class="modal-backdrop" *ngIf="editing()" (click)="closeEdit()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">{{ editing()!.mode === 'edit' ? '✎' : '+' }}</div>
          <div class="modal-titles">
            <h2>{{ editing()!.mode === 'edit' ? '编辑 MCP Server' : '添加 MCP Server' }}</h2>
            <div class="sub">{{ editing()!.mode === 'edit' ? '修改后立即重建连接，配置持久化到 settings.mcps' : '连接 stdio 本地进程或 Streamable HTTP 远程的 MCP server' }}</div>
          </div>
          <button class="icon" (click)="closeEdit()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <h3>标识</h3>
            <div class="form-row">
              <div>
                <label>名称 <span class="req">*</span></label>
                <input [(ngModel)]="form.name" placeholder="如 fs / think / serena" [disabled]="editing()!.mode === 'edit'" />
                <div class="muted small">唯一标识；工具名将形如 mcp_&lt;名称&gt;_&lt;tool&gt;</div>
              </div>
            </div>
          </div>
          <div class="form-section">
            <h3>连接</h3>
            <div class="form-row">
              <div>
                <label>传输方式</label>
                <select [(ngModel)]="form.transport">
                  <option value="stdio">stdio（本地子进程）</option>
                  <option value="http">http（Streamable HTTP 远程）</option>
                </select>
              </div>
            </div>
            <ng-container *ngIf="form.transport === 'http'">
              <label>Endpoint URL <span class="req">*</span></label>
              <input [(ngModel)]="form.url" placeholder="如 http://127.0.0.1:8787/mcp" />
              <div class="muted small">远程 MCP server 地址（支持 application/json 与 SSE 响应）</div>
            </ng-container>
            <ng-container *ngIf="form.transport !== 'http'">
              <div class="form-row">
                <div>
                  <label>命令 <span class="req">*</span></label>
                  <input [(ngModel)]="form.command" placeholder="如 ./mcp/node_modules/.bin/mcp-server-filesystem" />
                </div>
              </div>
              <div class="form-row">
                <div>
                  <label>参数（空格分隔）</label>
                  <input [(ngModel)]="form.argsText" placeholder="如 /path/to/workspace" />
                </div>
              </div>
              <div class="form-row">
                <div>
                  <label>环境变量（可选，KEY=VALUE 每行一个）</label>
                  <textarea [(ngModel)]="form.envText" rows="2" placeholder="如 GITHUB_TOKEN=ghp_xxx"></textarea>
                </div>
              </div>
            </ng-container>
          </div>
        </div>
        <div class="modal-foot">
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
    .req { color:#d33; }
    .test-result { margin-top:12px; }
    table.tbl { table-layout: fixed; }
    table.tbl th:nth-child(1), table.tbl td:nth-child(1) { width: 11%; }
    table.tbl th:nth-child(2), table.tbl td:nth-child(2) { width: 24%; }
    table.tbl th:nth-child(3), table.tbl td:nth-child(3) { width: 21%; }
    table.tbl th:nth-child(4), table.tbl td:nth-child(4) { width: 9%; }
    table.tbl th:nth-child(5), table.tbl td:nth-child(5) { width: 23%; }
    .nowrap { white-space: nowrap; }
    .ellipsis { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .tool-grid { grid-template-columns: repeat(auto-fill,minmax(240px,1fr)); }
    .tool-card { min-width: 0; overflow: hidden; }
    .tool-name .tname { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .tool-name .badge, .tool-name button { flex: 0 0 auto; white-space: nowrap; }
    .tool-desc { word-break: break-word; }
    .test-result pre { background:#f6f5f1; border:1px solid var(--border,#e4e3dd); border-radius:8px; padding:10px; font-size:12px; white-space:pre-wrap; word-break:break-all; max-height:220px; overflow:auto; margin:0; }
    .tpl-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(300px,1fr)); gap:10px; }
    .tpl-card { border:1px solid var(--border,#e4e3dd); border-radius:10px; padding:10px 12px; background:#fbfaf7; }
    .tpl-head { display:flex; align-items:center; gap:8px; justify-content:space-between; margin-bottom:4px; }
    .tpl-id { font-weight:600; font-size:13px; }
    .tpl-desc { font-size:12px; line-height:1.45; }
    .tpl-needs { margin-top:6px; font-size:12px; color:#b58900; }
    .skel-row { height:14px; border-radius:6px; margin:5px 2px;
      background:linear-gradient(90deg,#efece4 25%,#f8f6f1 37%,#efece4 63%);
      background-size:400% 100%; animation:skel 1.2s ease infinite; }
    .skel-bar { height:12px; border-radius:6px;
      background:linear-gradient(90deg,#efece4 25%,#f8f6f1 37%,#efece4 63%);
      background-size:400% 100%; animation:skel 1.2s ease infinite; }
    @keyframes skel { 0% {background-position:100% 0;} 100% {background-position:0 0;} }
  `],
})
export class McpsComponent implements OnInit {
  mcps = signal<McpServer[]>([]);
  tools = signal<ToolInfo[]>([]);
  error = signal('');
  saved = signal('');
  loadingMcps = signal(false);
  loadingTools = signal(false);
  editing = signal<{ mode: 'add' | 'edit'; server?: McpServer } | null>(null);
  testing = signal<ToolInfo | null>(null);
  testArgs: Record<string, string> = {};
  testResult = signal<string | null>(null);
  testError = signal('');
  testRunning = signal(false);
  saving = signal(false);
  form = { name: '', transport: 'stdio', command: '', argsText: '', envText: '', url: '' };

  // 常用 MCP 模板（参考 resolve-studio 的 MCP 接入清单；包名均已在本机验证可用）。
  templates: {
    id: string; desc: string; needs?: string;
    command: string; args: string[]; env?: Record<string, string>;
  }[] = [
    {
      id: 'fs', command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-filesystem', '/Users/erishen/Workspace/CNB/individular-invest'],
      desc: '文件系统读写：让 agent 读取/写入本地工作区文件',
      needs: 'allowed directory 默认填了项目根，可改成你想让 agent 访问的目录',
    },
    {
      id: 'think', command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-sequential-thinking'],
      desc: '结构化推理：让 agent 分步骤推演复杂问题，零外部依赖',
    },
    {
      id: 'memory', command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-memory'],
      desc: '跨会话长期记忆（内置 remember/recall 已有类似能力）',
    },
    {
      id: 'serena', command: 'uvx',
      args: ['--from', 'git+https://github.com/oraios/serena', 'serena', 'start-mcp-server'],
      desc: '代码库语义理解与编辑：符号检索、全文搜索、代码编辑、项目记忆（LSP 级）',
      needs: 'Python 项目，需本机装 uv（已有）；默认索引网关所在项目，可在参数里加项目路径',
    },
    {
      id: 'github', command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-github'],
      env: { GITHUB_TOKEN: '' },
      desc: 'GitHub：issues / PR / 仓库操作',
      needs: '需要 GITHUB_TOKEN（表单已预填占位，填上再保存）',
    },
    {
      id: 'playwright', command: 'npx',
      args: ['-y', '@playwright/mcp@latest'],
      desc: '浏览器自动化：让 agent 打开网页、点击、填表、截图',
      needs: '需本机已装 playwright 浏览器内核（npx 首次会自动拉）',
    },
    {
      id: 'brave', command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-brave-search'],
      env: { BRAVE_API_KEY: '' },
      desc: '网页搜索：Brave Search API',
      needs: '需要 BRAVE_API_KEY（表单已预填占位，填上再保存）',
    },
  ];

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
    this.loadTools();
  }

  // 保证 loading 骨架至少可见 350ms，避免接口太快导致闪烁不可见。
  private minShown(start: number, flag: { done: () => void }): void {
    const el = Date.now() - start;
    if (el >= 350) { flag.done(); return; }
    setTimeout(flag.done, 350 - el);
  }

  load(): void {
    this.loadingMcps.set(true);
    const t0 = Date.now();
    this.api.listMcps().subscribe({
      next: (r) => {
        this.mcps.set(r.mcps || []);
        this.minShown(t0, { done: () => this.loadingMcps.set(false) });
      },
      error: (e) => { this.error.set(e.error?.error?.message || '加载 MCP 配置失败'); this.minShown(t0, { done: () => this.loadingMcps.set(false) }); },
    });
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
        this.load();
      },
      error: (e) => { this.loadingTools.set(false); this.error.set(e.error?.error?.message || '刷新失败'); },
    });
  }

  openEdit(m: McpServer | null): void {
    this.error.set('');
    this.saved.set('');
    this.editing.set(m ? { mode: 'edit', server: m } : { mode: 'add' });
    this.form = {
      name: m?.name || '',
      transport: m?.transport === 'http' ? 'http' : 'stdio',
      command: m?.command || '',
      argsText: (m?.args || []).join(' '),
      envText: Object.entries(m?.env || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
      url: m?.url || '',
    };
  }

  closeEdit(): void {
    this.editing.set(null);
  }

  // useTemplate 用常用模板预填添加表单。
  useTemplate(t: { id: string; command: string; args: string[]; env?: Record<string, string> }): void {
    this.error.set('');
    this.saved.set('');
    this.editing.set({ mode: 'add' });
    this.form = {
      name: t.id,
      transport: 'stdio',
      command: t.command,
      argsText: t.args.join(' '),
      envText: Object.entries(t.env || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
      url: '',
    };
  }

  save(): void {
    const name = this.form.name.trim();
    const transport = this.form.transport === 'http' ? 'http' : 'stdio';
    const command = this.form.command.trim();
    const url = this.form.url.trim();
    if (!name) {
      this.error.set('名称必填');
      return;
    }
    if (transport === 'http' && !url) {
      this.error.set('http 传输需要 Endpoint URL');
      return;
    }
    if (transport !== 'http' && !command) {
      this.error.set('stdio 传输需要命令');
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
    this.api.saveMcp(name, { command, args, env, transport, url }).subscribe({
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
    this.testRunning.set(true);
    this.testError.set('');
    this.testResult.set(null);
    this.api.invokeTool(t.name, args).subscribe({
      next: (r) => {
        this.testRunning.set(false);
        this.testResult.set(r.result);
      },
      error: (e) => {
        this.testRunning.set(false);
        this.testError.set(e.error?.error?.message || '调用失败');
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
