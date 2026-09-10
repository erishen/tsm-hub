import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { McpServer } from './models';

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
            <td class="col-tools">
              <div class="mcp-chips" *ngIf="(m.tools || []).length">
                <button class="chip" *ngFor="let t of m.tools" (click)="toggleExpanded(m.name)"
                        [class.active]="expandedName() === m.name" [title]="toolDesc(m, t)">{{ t }}</button>
              </div>
              <span *ngIf="!(m.tools || []).length">—</span>
            </td>
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
              <div class="skel-row" style="width:48%"></div>
            </td>
          </tr>
          <tr *ngIf="!loadingMcps() && error() && !mcps().length">
            <td colspan="6" class="empty">
              <span class="err-text">{{ error() }}</span>
              <button class="small" style="margin-left:10px" (click)="load()">重试</button>
            </td>
          </tr>
          <tr *ngIf="!loadingMcps() && !error() && !mcps().length">
            <td colspan="6" class="empty">未配置 MCP server。示例：npx &#64;modelcontextprotocol/server-fetch</td>
          </tr>
        </tbody>
      </table>
      <div class="mcp-detail" *ngIf="expandedName() && detailServer()">
        <div class="detail-head">
          <span class="mono">{{ expandedName() }}</span>
          <span class="muted" style="font-size:12px">工具详情（{{ (detailServer()?.tool_details || []).length }}）</span>
          <button class="small" style="margin-left:auto" (click)="closeExpanded()">收起</button>
        </div>
        <div class="mcp-detail" *ngIf="(detailServer()?.tool_details || []).length; else noDetail">
          <div class="tool-detail" *ngFor="let d of detailServer()!.tool_details">
            <div class="td-head"><span class="mono">{{ d.name }}</span></div>
            <div class="muted td-desc">{{ d.description || '（无描述）' }}</div>
            <pre class="schema" *ngIf="schemaJson(d.input_schema)">{{ schemaJson(d.input_schema) }}</pre>
          </div>
        </div>
        <ng-template #noDetail><div class="empty">该 server 未提供工具 schema</div></ng-template>
      </div>
    </div>

    <div class="card">
      <h2>外部 MCP 候选（{{ candidates().length }}）<span class="muted" style="font-weight:400;font-size:12px">（调用方声明过的 server__tool 风格工具，尚未接入网关；可一键接入并常驻连接）</span></h2>
      <table class="tbl" *ngIf="candidates().length; else noneCand">
        <thead>
          <tr>
            <th>Server</th><th class="num">调用次数</th><th class="num">使用方（key 数）</th><th>高频工具</th><th style="width:130px">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr *ngFor="let c of candidates()">
            <td class="col-name"><span class="mono">{{ c.server }}</span></td>
            <td class="num">{{ c.calls }}</td>
            <td class="num">{{ c.key_count }}</td>
            <td>
              <div class="mcp-chips" *ngIf="c.tools.length">
                <span class="chip" *ngFor="let t of c.tools.slice(0,5)" [title]="t.name">{{ t.name.split('__')[1] }} ×{{ t.calls }}</span>
              </div>
              <span *ngIf="!c.tools.length">—</span>
            </td>
            <td>
              <button class="small primary" (click)="openAdoptMcp(c)">接入</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneCand><div class="empty">暂无外部 MCP 候选 —— 调用方声明的 server__tool 风格工具（未命中网关能力）会出现在这里</div></ng-template>
    </div>

    <!-- 编辑弹窗 -->
    <div class="modal-backdrop" *ngIf="editing()" (click)="closeEdit()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">{{ editing()!.mode === 'edit' ? '✎' : editing()!.mode === 'adopt' ? '⇪' : '+' }}</div>
          <div class="modal-titles">
            <h2>{{ editing()!.mode === 'edit' ? '编辑 MCP Server' : editing()!.mode === 'adopt' ? '接入外部 MCP' : '添加 MCP Server' }}</h2>
            <div class="sub">{{ editing()!.mode === 'edit' ? '修改后立即重建连接，配置持久化到 settings.mcps' : editing()!.mode === 'adopt' ? '外部调用方声明的 server，提供连接信息后接入网关常驻' : '连接 stdio 本地进程或 Streamable HTTP 远程的 MCP server' }}</div>
          </div>
          <button class="icon" (click)="closeEdit()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="form-section">
            <h3>标识</h3>
            <div class="form-row">
              <div>
                <label>名称 <span class="req">*</span></label>
                <input [(ngModel)]="form.name" placeholder="如 fs / think / serena" [disabled]="editing()!.mode !== 'add'" />
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
    .mcp-chips { display:flex; flex-wrap:wrap; gap:4px; }
    .chip { border:1px solid var(--border,#e4e3dd); border-radius:999px; padding:2px 10px; font-size:12px; cursor:pointer; background:transparent; color:inherit; }
    .chip:hover { background:rgba(0,0,0,0.04); }
    .chip.active { border-color:#3b82f6; color:#3b82f6; background:rgba(59,130,246,0.08); }
    .detail-row td { background:rgba(0,0,0,0.015); }
    .mcp-detail { display:flex; flex-direction:column; gap:10px; padding:4px 0; }
    .tool-detail { border:1px solid var(--border,#e4e3dd); border-radius:10px; padding:8px 12px; }
    .td-head { font-weight:600; margin-bottom:2px; }
    .td-desc { font-size:12px; margin-bottom:6px; }
    pre.schema { background:rgba(0,0,0,0.03); border:1px solid var(--border,#e4e3dd); border-radius:8px; padding:8px 10px; font-size:11px; overflow:auto; max-height:240px; margin:0; }
    .tool-schema summary { cursor:pointer; font-size:12px; color:#3b82f6; margin-top:6px; user-select:none; }
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
    .err-text { color:#d33; font-size:13px; }
    .skel-row { height:14px; border-radius:6px; margin:5px 2px;
      background:linear-gradient(90deg,#efece4 25%,#f8f6f1 37%,#efece4 63%);
      background-size:400% 100%; animation:skel 1.2s ease infinite; }
    @keyframes skel { 0% {background-position:100% 0;} 100% {background-position:0 0;} }
  `],
})
export class McpsComponent implements OnInit {
  mcps = signal<McpServer[]>([]);
  candidates = signal<{ server: string; calls: number; key_count: number; tools: { name: string; calls: number }[] }[]>([]);
  error = signal('');
  saved = signal('');
  loadingMcps = signal(true);
  editing = signal<{ mode: 'add' | 'edit' | 'adopt'; server?: McpServer } | null>(null);
  /** 当前展开详情（工具 chips 点击）的 MCP server 名 */
  expandedName = signal<string | null>(null);

  toggleExpanded(name: string): void {
    this.expandedName.set(this.expandedName() === name ? null : name);
  }

  closeExpanded(): void {
    this.expandedName.set(null);
  }

  detailServer(): McpServer | null {
    const n = this.expandedName();
    if (!n) return null;
    return this.mcps().find((m) => m.name === n) || null;
  }

  toolDesc(m: McpServer, toolName: string): string {
    const d = (m.tool_details || []).find((x) => x.name === toolName);
    return d && d.description ? d.description : toolName;
  }

  schemaJson(o: Record<string, unknown> | undefined): string {
    if (!o || !Object.keys(o).length) return '';
    try { return JSON.stringify(o, null, 2); } catch { return ''; }
  }
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
    this.loadCandidates();
  }


  loadCandidates(): void {
    this.api.externalMcpCandidates().subscribe({
      next: (r) => this.candidates.set(r.candidates || []),
      error: () => this.candidates.set([]),
    });
  }

  /** 从外部候选一键接入：预填 server 名为候选名，保存走 adopt 路径。 */
  openAdoptMcp(c: { server: string }): void {
    this.form = { name: c.server, transport: 'stdio', command: '', argsText: '', envText: '', url: '' };
    this.editing.set({ mode: 'adopt' });
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
    const body = { command, args, env, transport, url };
    const mode = this.editing()!.mode;
    const req = mode === 'adopt'
      ? this.api.adoptExternalMcp(name, body)
      : this.api.saveMcp(name, body);
    req.subscribe({
      next: () => {
        this.saving.set(false);
        this.editing.set(null);
        this.saved.set(mode === 'adopt' ? '外部 MCP ' + name + ' 已接入网关' : '已保存，MCP server 已重建连接');
        this.load();
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
      },
      error: (e) => this.error.set(e.error?.error?.message || '删除失败'),
    });
  }

}
