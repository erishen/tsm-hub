import { Component, OnInit, signal } from '@angular/core';
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

interface PgDraft {
  keyText: string;
  model: string;
  system: string;
  user: string;
  stream: boolean;
  temperature: any;
  maxTokens: any;
}

const DRAFT_KEY = 'llm-router.playground.draft';

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
    <div class="banner ok" *ngIf="mockMsg()">{{ mockMsg() }}</div>

    <div class="pg-grid">
      <div class="card pg-form">
        <h2>请求</h2>

        <!-- 联调：一键配置 mock provider + 调试 key -->
        <div class="pg-mock">
          <div class="pg-mock-head">
            <span>本机联调（无需真实 Key）</span>
            <button class="ghost" (click)="ensureMock()" [disabled]="mockBusy() || !api.loggedIn">
              {{ mockBusy() ? '配置中…' : '一键配置 Mock 联调' }}
            </button>
          </div>
          <div class="muted small">
            点击后会自动建好指向 <code>localhost:8799</code> 的 provider 与调试 Key，并填入下方 Token Key。
            需先在本机启动 mock 上游：<code>make mock-run</code>
          </div>
        </div>

        <div class="form-row">
          <div>
            <label>Token Key</label>
            <input [(ngModel)]="keyText" placeholder="sk-tr-..." />
          </div>
        </div>
        <div class="form-row">
          <div>
            <label>模型</label>
            <input [(ngModel)]="model" list="pg-models" placeholder="gpt-4o 或路由别名" />
            <datalist id="pg-models">
              <option *ngFor="let m of models()" [value]="m"></option>
            </datalist>
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
        <pre class="pg-pre" *ngIf="output()">{{ output() }}</pre>
        <div class="empty" *ngIf="!output() && !busy()">发送后在此显示回复</div>
        <div class="empty" *ngIf="busy()">等待上游…</div>
      </div>
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
    .pg-mock {
      background: var(--bg);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      padding: 12px 14px;
      margin-bottom: 14px;
    }
    .pg-mock-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      flex-wrap: wrap;
    }
    .pg-mock-head > span { font-weight: 600; font-size: 13px; }
    .small { font-size: 12px; }
    .pg-pre {
      white-space: pre-wrap;
      word-break: break-word;
      background: #0d1117;
      color: #e6edf3;
      padding: 14px;
      border-radius: 10px;
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 13px;
      line-height: 1.6;
      max-height: 60vh;
      overflow: auto;
      margin: 0;
    }
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
  readonly busy = signal(false);
  readonly output = signal('');
  readonly meta = signal<PgMeta | null>(null);
  readonly errorMsg = signal('');
  readonly mockBusy = signal(false);
  readonly mockMsg = signal('');
  readonly draftSaved = signal(false);

  private lastUsage: any = null;

  constructor(public api: ApiService) {}

  ngOnInit(): void {
    this.restoreDraft();
    this.loadModels();
  }

  loadModels(): void {
    const all = new Set<string>();
    let pending = 2;
    const done = () => {
      if (--pending !== 0) return;
      const arr = [...all];
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

  ensureMock(): void {
    if (this.mockBusy() || !this.api.loggedIn) return;
    this.mockBusy.set(true);
    this.mockMsg.set('');
    this.errorMsg.set('');
    this.api.ensureMockLocal().subscribe({
      next: (key) => {
        this.keyText = key;
        if (!this.model) this.model = 'mock-model';
        this.stream = true;
        this.user = this.user || '你好';
        this.saveDraft();
        this.mockBusy.set(false);
        this.mockMsg.set('已配置指向 localhost:8799 的 provider 与调试 Key，并已填入 Token Key。请先运行 make mock-run 启动 mock 上游，然后点「发送」。');
      },
      error: (e) => {
        this.mockBusy.set(false);
        this.errorMsg.set('Mock 联调配置失败: ' + (e?.message || e));
      },
    });
  }

  async send(): Promise<void> {
    if (!this.keyText || !this.user || this.busy()) return;
    this.busy.set(true);
    this.errorMsg.set('');
    this.mockMsg.set('');
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
        this.meta.set({ ok: false, status: res.status, provider, latencyMs: Math.round(performance.now() - t0), ttfbMs: ttfb, usage: null });
        this.errorMsg.set('请求失败 (' + res.status + '): ' + msg);
        this.busy.set(false);
        return;
      }

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
        this.meta.set({ ok: true, status: res.status, provider, latencyMs: Math.round(performance.now() - t0), ttfbMs: ttfb, usage: this.lastUsage });
      } else {
        const j = await res.json();
        this.output.set(j.choices?.[0]?.message?.content ?? '');
        this.meta.set({ ok: true, status: res.status, provider, latencyMs: Math.round(performance.now() - t0), ttfbMs: ttfb, usage: j.usage || null });
      }
      this.saveDraft();
    } catch (e: any) {
      this.errorMsg.set('网络错误: ' + (e?.message || e));
      this.meta.set({ ok: false, status: 0, provider: '', latencyMs: Math.round(performance.now() - t0), ttfbMs: ttfb, usage: null });
    }
    this.busy.set(false);
  }
}
