import { Component, OnInit, signal } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { McpServer } from './models';

@Component({
  selector: 'app-mcps',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './mcps.component.html',
  styleUrls: ['./mcps.component.css'],
})
export class McpsComponent implements OnInit, OnDestroy {
  mcps = signal<McpServer[]>([]);
  candidates = signal<{ server: string; calls: number; key_count: number; tools: { name: string; calls: number }[]; suggested_command?: string }[]>([]);
  loadingCandidates = signal(true); // 外部候选独立 loading（统计查询较慢，避免被误判为"无候选"）
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
  suggestRunning = signal(false);
  suggestNotes = signal('');
  suggestError = signal('');
  form = { name: '', transport: 'stdio', command: '', argsText: '', envText: '', url: '', timeoutSec: 0 };

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

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.editing()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
    this.loadCandidates();
  }


  loadCandidates(): void {
    this.loadingCandidates.set(true);
    this.api.externalMcpCandidates().subscribe({
      next: (r) => { this.candidates.set(r.candidates || []); this.loadingCandidates.set(false); },
      error: () => { this.candidates.set([]); this.loadingCandidates.set(false); },
    });
  }

  /** 删除外部 MCP 候选：加入忽略列表，不再显示（软删除，工具统计仍保留）。 */
  ignoreCandidate(c: { server: string }): void {
    if (!confirm(`确认从候选列表移除「${c.server}」？\n\n这是软删除：工具调用统计仍保留，只是不再出现在候选列表。可在 config.json 的 settings.ignored_mcp_servers 中移除以恢复。`)) return;
    this.api.ignoreExternalMcp(c.server).subscribe({
      next: () => {
        this.candidates.set(this.candidates().filter((x) => x.server !== c.server));
      },
      error: (e: Error) => alert('删除失败：' + e.message),
    });
  }

  /** 从外部候选一键接入：预填 server 名；命中常见 MCP 包映射时自动预填启动命令，保存走 adopt 路径。 */
  openAdoptMcp(c: { server: string; suggested_command?: string }): void {
    this.form = { name: c.server, transport: 'stdio', command: c.suggested_command || '', argsText: '', envText: '', url: '', timeoutSec: 0 };
    this.suggestNotes.set('');
    this.suggestError.set('');
    this.editing.set({ mode: 'adopt' });
  }

  /** 让网关 LLM 根据候选 server/工具名推断连接方式并预填表单。 */
  suggestMcp(): void {
    const c = this.candidates().find((x) => x.server === this.form.name);
    if (!c) return;
    this.suggestRunning.set(true);
    this.suggestNotes.set('');
    this.suggestError.set('');
    this.api.suggestExternalMcp(c.server, c.tools.map((t) => ({ name: t.name, calls: t.calls }))).subscribe({
      next: (r) => {
        this.suggestRunning.set(false);
        const s = r.suggestion;
        if (s.transport === 'http') {
          this.form.transport = 'http';
          this.form.url = s.url || this.form.url;
        } else {
          this.form.transport = 'stdio';
          if (s.command) this.form.command = s.command;
          this.form.argsText = (s.args || []).join(' ');
          if (s.env_hint) {
            this.form.envText = s.env_hint;
          }
        }
        this.suggestNotes.set(s.notes || 'AI 建议已预填，请核对后保存');
      },
      error: (e: Error) => {
        this.suggestRunning.set(false);
        this.suggestError.set(e.message || 'AI 建议失败');
      },
    });
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
      timeoutSec: m?.timeout_sec || 0,
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
      timeoutSec: 0,
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
    const body = { command, args, env, transport, url, timeout_sec: Number(this.form.timeoutSec) || 0 };
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
