import { Component, OnDestroy, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { Route } from './models';

interface PgMeta {
  ok: boolean;
  status: number;
  provider: string;
  latencyMs: number;
  ttfbMs: number | null;
  usage: any;
}

interface PgRecent {
  time: string;
  model: string;
  ok: boolean;
  status: number;
  provider: string;
  latencyMs: number;
}

interface PgDraft {
  keyText: string;
  model: string;
  system: string;
  user: string;
  stream: boolean;
  temperature: any;
  maxTokens: any;
}

const DRAFT_KEY = 'tsm-hub.playground.draft';

@Component({
  selector: 'app-playground',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>测试</h1>
        <div class="sub">用 Token Key 发请求，验证路由命中与上游连通性</div>
      </div>
    </div>

    <div class="banner error" *ngIf="errorMsg()">{{ errorMsg() }}</div>

    <div class="page-loading" *ngIf="loading()">加载中…</div>
    <div class="pg-grid">
      <div class="card pg-form">
        <h2>请求</h2>

        <div class="form-row">
          <div>
            <label>Token Key</label>
            <input [(ngModel)]="keyText" placeholder="sk-tr-..." />
          </div>
        </div>
        <div class="form-row">
          <div>
            <label>模型</label>
            <div style="display:flex;gap:8px">
              <select [(ngModel)]="model" style="flex:1">
                <option value="" disabled *ngIf="!model">选择模型…</option>
                <option *ngFor="let m of selectModels()" [ngValue]="m">{{ m }}{{ isFree(m) ? ' (FREE)' : '' }}</option>
              </select>
              <button type="button" class="small" (click)="loadModels()" title="重新拉取 Providers 与路由的模型列表">刷新</button>
            </div>
            <div class="muted small" style="margin-top:4px">候选 = Providers 已配置的具体模型 + 路由别名</div>
          </div>
          <div>
            <label>响应模式</label>
            <select [(ngModel)]="stream">
              <option [ngValue]="true">流式 SSE</option>
              <option [ngValue]="false">非流式</option>
            </select>
          </div>
        </div>
        <div class="form-row">
          <div>
            <label>System（可选）</label>
            <textarea [(ngModel)]="system" placeholder="系统提示词"></textarea>
          </div>
        </div>
        <div class="form-row">
          <div>
            <label>User 消息</label>
            <textarea [(ngModel)]="user" placeholder="你好"></textarea>
          </div>
        </div>
        <div class="form-row">
          <div>
            <label>temperature（可选）</label>
            <input type="number" step="0.1" [(ngModel)]="temperature" placeholder="留空=默认" />
          </div>
          <div>
            <label>max_tokens（可选）</label>
            <input type="number" [(ngModel)]="maxTokens" placeholder="留空=默认" />
          </div>
        </div>
        <div style="margin-top:6px; display:flex; align-items:center; gap:10px">
          <button class="primary" (click)="send()" [disabled]="!keyText || !user || busy()">
            {{ busy() ? '发送中…' : '发送' }}
          </button>
          <button class="ghost" (click)="clearDraft()">清空</button>
          <span class="muted small" *ngIf="draftSaved()">已自动保存草稿</span>
          <span class="muted small" *ngIf="!api.loggedIn">未登录时「一键 Mock 联调」不可用</span>
        </div>
      </div>

      <div class="card pg-out">
        <h2>响应</h2>
        <div class="pg-meta" *ngIf="meta() as m">
          <span class="badge" [class.ok]="m.ok" [class.bad]="!m.ok">HTTP {{ m.status }}</span>
          <span class="muted" *ngIf="m.provider">provider: <b>{{ m.provider }}</b></span>
          <span class="muted">耗时 {{ m.latencyMs }} ms</span>
          <span class="muted" *ngIf="m.ttfbMs != null">首字节 {{ m.ttfbMs }} ms</span>
          <span class="muted" *ngIf="m.usage">tokens {{ m.usage.prompt_tokens }}+{{ m.usage.completion_tokens }}={{ m.usage.total_tokens }}</span>
        </div>
        <div class="pg-md" *ngIf="output()" [innerHTML]="renderMd(output())"></div>
        <div class="empty" *ngIf="!output() && !busy()">发送后在此显示回复</div>
        <div class="empty" *ngIf="busy()">等待上游…</div>
      </div>
    </div>

    <div class="card" style="margin-top:18px">
      <h2>最近请求</h2>
      <table *ngIf="recent().length; else noRecent">
        <thead>
          <tr><th>时间</th><th>模型</th><th>状态</th><th>Provider</th><th class="num">耗时</th></tr>
        </thead>
        <tbody>
          <tr *ngFor="let r of recent()">
            <td class="muted">{{ r.time }}</td>
            <td class="mono">{{ r.model }}</td>
            <td><span class="badge" [class.ok]="r.ok" [class.bad]="!r.ok">HTTP {{ r.status }}</span></td>
            <td class="mono">{{ r.provider || '—' }}</td>
            <td class="num">{{ r.latencyMs }} ms</td>
          </tr>
        </tbody>
      </table>
      <ng-template #noRecent><div class="empty">还没有请求记录</div></ng-template>
    </div>
  `,
  styles: [`
    .pg-grid {
      display: grid;
      grid-template-columns: minmax(320px, 1fr) minmax(360px, 1.4fr);
      gap: 18px;
      align-items: start;
    }
    @media (max-width: 880px) {
      .pg-grid { grid-template-columns: 1fr; }
    }
    .pg-form textarea { min-height: 84px; }
    .small { font-size: 12px; }
    .pg-md {
      background: #0d1117;
      color: #e6edf3;
      padding: 14px;
      border-radius: 10px;
      font-size: 13px;
      line-height: 1.65;
      word-break: break-word;
    }
    .pg-md h1, .pg-md h2, .pg-md h3 { color: #f0f6fc; margin: 12px 0 6px; line-height: 1.3; }
    .pg-md h1 { font-size: 18px; }
    .pg-md h2 { font-size: 16px; }
    .pg-md h3 { font-size: 14px; }
    .pg-md p { margin: 6px 0; }
    .pg-md code {
      background: #1f2937; color: #f472b6; padding: 1px 5px; border-radius: 4px;
      font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    }
    .pg-md pre {
      background: #161b22; border: 1px solid #30363d; border-radius: 8px;
      padding: 10px 12px; overflow: auto; margin: 8px 0;
    }
    .pg-md pre code { background: transparent; padding: 0; color: #e6edf3; font-size: 12px; }
    .pg-md a { color: #58a6ff; text-decoration: none; }
    .pg-md a:hover { text-decoration: underline; }
    .pg-md ul, .pg-md ol { margin: 6px 0; padding-left: 22px; }
    .pg-md li { margin: 2px 0; }
    .pg-md blockquote {
      margin: 8px 0; padding: 4px 12px; border-left: 3px solid #30363d;
      color: #8b949e; background: rgba(255,255,255,0.03);
    }
    .pg-md table { border-collapse: collapse; margin: 8px 0; width: 100%; display: block; overflow-x: auto; }
    .pg-md th, .pg-md td { border: 1px solid #30363d; padding: 5px 10px; text-align: left; }
    .pg-md th { background: #161b22; }
    .pg-md hr { border: none; border-top: 1px solid #30363d; margin: 12px 0; }
    .pg-md strong { color: #f0f6fc; }
    .pg-meta {
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
      align-items: center;
      margin-bottom: 12px;
      padding-bottom: 12px;
      border-bottom: 1px solid var(--border);
    }
  `],
})
export class PlaygroundComponent implements OnInit {
  keyText = '';
  model = '';
  system = '';
  user = '你好';
  stream = true;
  temperature: any = '';
  maxTokens: any = '';

  readonly models = signal<string[]>([]);
  /** 目录探测标记为免费的模型 id（免费优先排序用）。 */
  private freeIds = new Set<string>();
  /** 目录里被上游 404 标记为不可用的模型 id → 不可用行数；availIds 是有可用行的 id。 */
  private unavailById = new Map<string, number>();
  private availIds = new Set<string>();
  /** 最近请求记录（组件内，最多 10 条）。 */
  readonly recent = signal<PgRecent[]>([]);
  readonly loading = signal(true);
  readonly busy = signal(false);
  readonly output = signal('');
  readonly meta = signal<PgMeta | null>(null);
  readonly errorMsg = signal('');
  readonly draftSaved = signal(false);

  private lastUsage: any = null;
  private focusHandler: () => void = () => {};

  constructor(public api: ApiService) {}

  ngOnInit(): void {
    this.restoreDraft();
    this.loadModels();
    // 多标签场景：切回本页面时自动刷新模型列表（避免看到旧数据）
    this.focusHandler = () => this.loadModels();
    window.addEventListener('focus', this.focusHandler);
  }

  ngOnDestroy(): void {
    window.removeEventListener('focus', this.focusHandler);
  }

  loadModels(): void {
    const all = new Set<string>();
    this.freeIds.clear();
    this.unavailById.clear();
    this.availIds.clear();
    this.loading.set(true);
    let pending = 3;
    const done = () => {
      if (--pending !== 0) return;
      this.loading.set(false);
      // auto：外部无脑调用入口，恒置顶并作为默认选中（网关按内容自动分流）。
      const arr = ['auto', ...[...all].filter((x) => x !== 'auto')];
      this.models.set(arr);
      if (!this.model && arr.length) this.model = arr[0];
    };
    this.api.listProviders().subscribe({
      next: (r) => (r.providers || []).forEach((p) =>
        (p.models || []).forEach((m) => { if (m && m !== '*') all.add(m); })),
      complete: done, error: done,
    });
    this.api.listRoutes().subscribe({
      next: (r) => (r.routes || []).forEach((rt: Route) => all.add(rt.model)),
      complete: done, error: done,
    });
    // 模型目录快照：哪些模型当前免费（探测结果），哪些被上游 404 判为不可用（冷却期内）
    this.api.modelsCatalog().subscribe({
      next: (r) => (r.models || []).forEach((cm) => {
        if (cm.free) this.freeIds.add(cm.id);
        if (cm.unavailable) {
          this.unavailById.set(cm.id, (this.unavailById.get(cm.id) ?? 0) + 1);
        } else {
          this.availIds.add(cm.id);
        }
      }),
      complete: done, error: done,
    });
  }

  /** 免费判定：目录探测标记 free，或模型 id 带 :free 后缀（全局规则）。 */
  isFree(m: string): boolean {
    return this.freeIds.has(m) || m.includes(':free');
  }

  /** 不可用判定：目录里该模型的所有 provider 行都被 404 标记（至少一行为可用则保留）。 */
  isUnavailable(m: string): boolean {
    return (this.unavailById.get(m) ?? 0) > 0 && !this.availIds.has(m);
  }

  /** 下拉候选：auto 恒置顶；已配置模型 + 当前值（当前值若不在候选中则保留显示，避免选择框空白）。
   *  全不可用的模型排除；免费模型排前面，其余保持原有顺序。 */
  selectModels(): string[] {
    const ms = this.models();
    const keep = ms.filter((m) => !this.isUnavailable(m));
    const arr = this.model && !keep.includes(this.model) ? [this.model, ...keep] : keep;
    return [...arr].sort((a, b) => {
      if (a === 'auto') return -1;
      if (b === 'auto') return 1;
      return Number(this.isFree(b)) - Number(this.isFree(a));
    });
  }

  /** 记录一次请求结果到「最近请求」列表（最多 10 条）。 */
  private record(status: number, provider: string, latencyMs: number): void {
    const r: PgRecent = {
      time: new Date().toLocaleTimeString(),
      model: this.model || 'smart',
      ok: status >= 200 && status < 300,
      status,
      provider,
      latencyMs,
    };
    this.recent.update((list) => [r, ...list].slice(0, 10));
  }

  private saveDraft(): void {
    const d: PgDraft = {
      keyText: this.keyText, model: this.model, system: this.system,
      user: this.user, stream: this.stream, temperature: this.temperature, maxTokens: this.maxTokens,
    };
    try { localStorage.setItem(DRAFT_KEY, JSON.stringify(d)); this.draftSaved.set(true); } catch {}
  }

  private restoreDraft(): void {
    try {
      const raw = localStorage.getItem(DRAFT_KEY);
      if (!raw) return;
      const d = JSON.parse(raw) as PgDraft;
      this.keyText = d.keyText ?? '';
      this.model = d.model ?? '';
      this.system = d.system ?? '';
      this.user = d.user ?? '你好';
      this.stream = d.stream ?? true;
      this.temperature = d.temperature ?? '';
      this.maxTokens = d.maxTokens ?? '';
    } catch {}
  }

  private esc(s: string): string {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  /** 行内 markdown：`code` → **bold** → *italic* → [link](url)（仅 http/https）。 */
  private inlineMd(s: string): string {
    const esc = this.esc(s);
    return esc
      .replace(/`([^`]+)`/g, '<code>$1</code>')
      .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
      .replace(/\*([^*]+)\*/g, '<em>$1</em>')
      .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  }

  /** 轻量 Markdown 渲染（零依赖）。先整体 HTML 转义，再解析；原始 HTML 一律不可执行。 */
  renderMd(text: string): string {
    if (!text) return '';
    const lines = text.replace(/\r\n/g, '\n').split('\n');
    const out: string[] = [];
    let i = 0;
    let codeBuf: string[] = [];
    let codeLang = '';
    const flushCode = (): void => {
      if (!codeBuf.length) return;
      const lang = codeLang ? ' class="lang-' + this.esc(codeLang) + '"' : '';
      out.push('<pre><code' + lang + '>' + this.esc(codeBuf.join('\n')) + '</code></pre>');
      codeBuf = [];
      codeLang = '';
    };

    while (i < lines.length) {
      const line = lines[i];

      // 代码块 ```lang ... ```
      if (/^```/.test(line)) {
        flushCode();
        if (!codeBuf.length) {
          codeLang = line.slice(3).trim();
          codeBuf = [];
          i++;
          while (i < lines.length && !/^```/.test(lines[i])) { codeBuf.push(lines[i]); i++; }
          i++; // 跳过闭合 ```
          flushCode();
        }
        continue;
      }

      // 标题
      const h = line.match(/^(#{1,6})\s+(.*)$/);
      if (h) {
        out.push('<h' + h[1].length + '>' + this.inlineMd(h[2]) + '</h' + h[1].length + '>');
        i++;
        continue;
      }

      // 分割线
      if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
        out.push('<hr>');
        i++;
        continue;
      }

      // 引用块
      if (/^>\s?/.test(line)) {
        const q: string[] = [];
        while (i < lines.length && /^>\s?/.test(lines[i])) { q.push(lines[i].replace(/^>\s?/, '')); i++; }
        out.push('<blockquote>' + q.map((x) => this.inlineMd(x)).join('<br>') + '</blockquote>');
        continue;
      }

      // 表格：表头行 + 分隔行（|---|---|）
      if (line.includes('|') && i + 1 < lines.length && /^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(lines[i + 1].trim()) && lines[i + 1].includes('-')) {
        const splitRow = (r: string): string[] =>
          r.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
        const heads = splitRow(line);
        i += 2;
        const rows: string[][] = [];
        while (i < lines.length && lines[i].includes('|')) { rows.push(splitRow(lines[i])); i++; }
        let t = '<table><thead><tr>';
        heads.forEach((c) => { t += '<th>' + this.inlineMd(c) + '</th>'; });
        t += '</tr></thead><tbody>';
        rows.forEach((r) => {
          t += '<tr>';
          heads.forEach((_, k) => { t += '<td>' + this.inlineMd(r[k] ?? '') + '</td>'; });
          t += '</tr>';
        });
        t += '</tbody></table>';
        out.push(t);
        continue;
      }

      // 无序列表
      const ul = line.match(/^\s*[-*+]\s+(.*)$/);
      if (ul) {
        const items: string[] = [ul[1]];
        i++;
        while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) { items.push(lines[i].replace(/^\s*[-*+]\s+/, '')); i++; }
        out.push('<ul>' + items.map((x) => '<li>' + this.inlineMd(x) + '</li>').join('') + '</ul>');
        continue;
      }

      // 有序列表
      const ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
      if (ol) {
        const items: string[] = [ol[1]];
        i++;
        while (i < lines.length && /^\s*\d+[.)]\s+/.test(lines[i])) { items.push(lines[i].replace(/^\s*\d+[.)]\s+/, '')); i++; }
        out.push('<ol>' + items.map((x) => '<li>' + this.inlineMd(x) + '</li>').join('') + '</ol>');
        continue;
      }

      // 空行
      if (!line.trim()) { i++; continue; }

      // 普通段落（连续非空行合并）
      const para: string[] = [line];
      i++;
      while (i < lines.length && lines[i].trim() && !/^(#{1,6})\s|^```|^>\s?|^\s*[-*+]\s|^\s*\d+[.)]\s/.test(lines[i]) && !(lines[i].includes('|') && i + 1 < lines.length && /^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(lines[i + 1].trim()) && lines[i + 1].includes('-'))) {
        para.push(lines[i]);
        i++;
      }
      out.push('<p>' + para.map((x) => this.inlineMd(x)).join('<br>') + '</p>');
    }
    flushCode();
    return out.join('\n');
  }

  clearDraft(): void {
    this.keyText = '';
    this.model = '';
    this.system = '';
    this.user = '你好';
    this.stream = true;
    this.temperature = '';
    this.maxTokens = '';
    this.output.set('');
    this.meta.set(null);
    this.errorMsg.set('');
    try { localStorage.removeItem(DRAFT_KEY); } catch {}
    this.draftSaved.set(false);
  }

  async send(): Promise<void> {
    if (!this.keyText || !this.user || this.busy()) return;
    this.busy.set(true);
    this.errorMsg.set('');
    this.output.set('');
    this.meta.set(null);
    this.lastUsage = null;

    const messages: any[] = [];
    if (this.system) messages.push({ role: 'system', content: this.system });
    messages.push({ role: 'user', content: this.user });

    const body: any = { model: this.model || 'smart', messages, stream: !!this.stream };
    if (this.temperature !== '' && this.temperature != null) body.temperature = Number(this.temperature);
    if (this.maxTokens !== '' && this.maxTokens != null) body.max_tokens = Number(this.maxTokens);

    const t0 = performance.now();
    let ttfb: number | null = null;
    try {
      const res = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + this.keyText },
        body: JSON.stringify(body),
      });
      ttfb = Math.round(performance.now() - t0);
      const provider = res.headers.get('X-LLM-Router-Provider') || '';

      if (!res.ok) {
        const txt = await res.text();
        let msg = txt;
        try { const j = JSON.parse(txt); if (j?.error?.message) msg = j.error.message; } catch {}
        const latency = Math.round(performance.now() - t0);
        this.record(res.status, provider, latency);
        this.meta.set({ ok: false, status: res.status, provider, latencyMs: latency, ttfbMs: ttfb, usage: null });
        this.errorMsg.set('请求失败 (' + res.status + '): ' + msg);
        this.busy.set(false);
        return;
      }

      const latency = Math.round(performance.now() - t0);
      if (this.stream) {
        const reader = res.body!.getReader();
        const dec = new TextDecoder();
        let buf = '';
        let out = '';
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          let nl: number;
          while ((nl = buf.indexOf('\n')) >= 0) {
            const line = buf.slice(0, nl).trim();
            buf = buf.slice(nl + 1);
            if (!line) continue;
            if (line.startsWith('data:')) {
              const data = line.slice(5).trim();
              if (data === '[DONE]') continue;
              try {
                const j = JSON.parse(data);
                const c = j.choices?.[0]?.delta?.content ?? j.choices?.[0]?.message?.content ?? '';
                if (c) { out += c; this.output.set(out); }
                if (j.usage) this.lastUsage = j.usage;
              } catch {}
            }
          }
        }
        this.record(res.status, provider, latency);
        this.meta.set({ ok: true, status: res.status, provider, latencyMs: latency, ttfbMs: ttfb, usage: this.lastUsage });
      } else {
        const j = await res.json();
        this.output.set(j.choices?.[0]?.message?.content ?? '');
        this.record(res.status, provider, latency);
        this.meta.set({ ok: true, status: res.status, provider, latencyMs: latency, ttfbMs: ttfb, usage: j.usage || null });
      }
      this.saveDraft();
    } catch (e: any) {
      const latency = Math.round(performance.now() - t0);
      this.record(0, '', latency);
      this.errorMsg.set('网络错误: ' + (e?.message || e));
      this.meta.set({ ok: false, status: 0, provider: '', latencyMs: latency, ttfbMs: ttfb, usage: null });
    }
    this.busy.set(false);
  }
}
