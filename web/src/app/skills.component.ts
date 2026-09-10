import { Component, OnInit, signal, computed } from '@angular/core';
import { effect, OnDestroy } from '@angular/core';
import { lockBody, unlockBody } from './scroll-lock';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { SkillDetail, SkillSummary } from './models';

interface SkillCandidate {
  name: string;
  calls: number;
  key_count: number;
  adopted: boolean;
  description?: string;
  adopted_at?: string;
  kind?: string;
}

@Component({
  selector: 'app-skills',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>技能库</h1>
        <div class="sub" *ngIf="dir()">挂载自 <span class="mono">{{ dir() }}</span>（Agent Skills 标准）</div>
        <div class="sub" *ngIf="!dir()">未配置 skills_dir，可在 data/config.json 的 settings 里指向技能库目录</div>
      </div>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="page-loading" *ngIf="loading()">加载中…</div>
    <div class="card" *ngIf="detail()">
      <div style="display:flex;align-items:flex-start;gap:10px">
        <div style="flex:1">
          <h2 style="margin:0">
            <span class="mono">{{ detail()!.name }}</span>
            <span class="badge" style="vertical-align:middle">SKILL.md</span>
          </h2>
          <div class="muted" style="margin-top:6px">{{ detail()!.description }}</div>
          <div class="muted small" style="margin-top:4px">
            {{ detail()!.size }} B · 更新于 {{ detail()!.updated }}
            <span *ngIf="detail()!.has_scripts"> · scripts: {{ (detail()!.scripts || []).join(', ') }}</span>
          </div>
        </div>
        <div style="flex:0 0 auto;display:flex;gap:8px;flex-wrap:wrap;justify-content:flex-end">
          <button (click)="previewMode.set('preview')" [class.primary]="previewMode()==='preview'">预览</button>
          <button (click)="previewMode.set('raw')" [class.primary]="previewMode()==='raw'">原始</button>
          <button (click)="back()">← 返回列表</button>
          <button class="primary" (click)="copyRaw()">{{ copied() ? '已复制 ✓' : '复制 SKILL.md' }}</button>
        </div>
      </div>
      <div class="skill-md-preview" *ngIf="previewMode()==='preview'" [innerHTML]="renderedMd()"></div>
      <pre class="skill-md" *ngIf="previewMode()==='raw'">{{ detail()!.raw }}</pre>
    </div>

    <div class="card" *ngIf="!detail()">
      <h2>技能（{{ skills().length }}）</h2>
      <div class="skill-grid" *ngIf="skills().length; else none">
        <div class="skill-card" *ngFor="let s of skills()" (click)="open(s)">
          <div class="skill-name">
            <span class="mono">{{ s.name }}</span>
            <span class="badge ok" *ngIf="s.has_scripts" title="含脚本">scripts</span>
            <span class="badge" *ngIf="!s.has_scripts">纯指令</span>
          </div>
          <div class="muted skill-desc">{{ s.description || '（无描述）' }}</div>
          <div class="skill-foot muted small" *ngIf="s.has_scripts">
            {{ (s.scripts || []).join(', ') }}
          </div>
        </div>
      </div>
      <ng-template #none><div class="empty">技能库为空或未配置 skills_dir</div></ng-template>
    </div>

    <div class="card" *ngIf="!detail()">
      <h2>外部技能候选（{{ candidates().length }}）
        <span class="muted" style="font-weight:400;font-size:12px">
          （调用方声明过、网关未收录的技能类能力；可择优录用为技能，skill-run 调用时注入指令说明）
        </span>
      </h2>
      <div class="banner error" *ngIf="candError()">{{ candError() }}</div>
      <div class="muted small" style="margin-bottom:8px">
        已录用技能（<span class="mono">kind=skill</span>）会出现在模型可感知的技能清单；未录用候选带 <span class="mono">skill:</span>/<span class="mono">skill_</span> 前缀。
      </div>
      <div class="skel-row" *ngIf="loadingCandidates()" style="max-width:640px;margin:10px 0"></div>
      <table class="tbl" *ngIf="!loadingCandidates() && candidates().length; else noneCand">
        <thead>
          <tr><th>名称</th><th>说明</th><th style="width:110px">调用</th><th style="width:220px">操作</th></tr>
        </thead>
        <tbody>
          <tr *ngFor="let c of candidates()">
            <td class="col-name"><span class="mono small" [title]="c.name">{{ c.name }}</span></td>
            <td>
              <span *ngIf="c.adopted && c.description" class="muted small">{{ c.description }}</span>
              <span *ngIf="c.adopted" class="badge ok" style="margin-left:6px">已录用</span>
              <span *ngIf="!c.adopted" class="badge">候选</span>
              <div class="muted small" *ngIf="c.adopted && c.adopted_at">录用于 {{ c.adopted_at }}</div>
            </td>
            <td><span class="mono small">{{ c.calls || 0 }} 次 · {{ c.key_count || 0 }} key</span></td>
            <td>
              <button class="small primary" *ngIf="!c.adopted" (click)="openAdopt(c)">录用为技能</button>
              <button class="small danger" *ngIf="c.adopted" (click)="unadopt(c)">取消录用</button>
            </td>
          </tr>
        </tbody>
      </table>
      <ng-template #noneCand><div class="empty">暂无外部技能候选 —— 调用方声明 <span class="mono">skill:</span> 前缀的未收录能力时会出现在这里</div></ng-template>
    </div>

    <!-- 录用技能弹窗 -->
    <div class="modal-backdrop" *ngIf="adopting()" (click)="closeAdopt()">
      <div class="modal" (click)="$event.stopPropagation()">
        <div class="modal-head">
          <div class="modal-icon">✦</div>
          <div class="modal-titles">
            <h2>录用为技能 <span class="mono">{{ adopting()!.name }}</span></h2>
            <div class="sub">录入技能指令说明，skill-run 调用时注入给模型（仅登记说明，执行在调用方侧）</div>
          </div>
          <button class="icon" (click)="closeAdopt()" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <label>指令说明（SKILL.md 风格）</label>
          <textarea rows="6" [(ngModel)]="adoptDesc" placeholder="描述该技能的用途、输入输出、使用边界…"></textarea>
        </div>
        <div class="modal-foot">
          <button (click)="closeAdopt()">取消</button>
          <button class="primary" (click)="confirmAdopt()" [disabled]="adoptSaving()">{{ adoptSaving() ? '录用中…' : '确认录用' }}</button>
        </div>
      </div>
    </div>
  `,
  styles: [`
    .skill-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
    .skill-card {
      border: 1px solid var(--border); border-radius: 12px; padding: 12px 14px;
      cursor: pointer; transition: border-color .15s, box-shadow .15s;
    }
    .skill-card:hover { border-color: var(--primary); box-shadow: 0 4px 14px -6px rgba(15,23,42,.15); }
    .skill-name { display: flex; align-items: center; gap: 8px; font-size: 15px; font-weight: 600; }
    .skill-desc { margin-top: 6px; font-size: 13px; line-height: 1.5; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
    .skill-foot { margin-top: 8px; font-size: 11px; color: var(--muted); }
    .skill-md {
      margin-top: 14px; background: #0d1117; color: #e6edf3; border-radius: 10px;
      padding: 14px 16px; font-size: 12.5px; line-height: 1.6; overflow-x: auto;
      white-space: pre-wrap; word-break: break-word; max-height: 70vh; overflow-y: auto;
    }
    .skill-md-preview {
      margin-top: 14px; padding: 18px 22px; background: var(--card-color);
      border: 1px solid var(--border); border-radius: 10px; max-height: 70vh; overflow-y: auto;
      font-size: 13.5px; line-height: 1.7; color: var(--text-color);
    }
    .skill-md-preview .md-h1 { font-size: 22px; font-weight: 700; margin: 18px 0 10px; padding-bottom: 8px; border-bottom: 1px solid var(--border); }
    .skill-md-preview .md-h2 { font-size: 18px; font-weight: 700; margin: 16px 0 8px; }
    .skill-md-preview .md-h3 { font-size: 15px; font-weight: 600; margin: 14px 0 6px; }
    .skill-md-preview .md-h4, .skill-md-preview .md-h5, .skill-md-preview .md-h6 { font-size: 14px; font-weight: 600; margin: 12px 0 4px; }
    .skill-md-preview .md-p { margin: 8px 0; }
    .skill-md-preview .md-ul, .skill-md-preview .md-ol { margin: 8px 0; padding-left: 24px; }
    .skill-md-preview .md-ul li, .skill-md-preview .md-ol li { margin: 4px 0; }
    .skill-md-preview .md-quote { margin: 10px 0; padding: 8px 14px; border-left: 3px solid var(--primary); background: rgba(94,134,255,0.05); border-radius: 0 6px 6px 0; }
    .skill-md-preview .md-quote p { margin: 4px 0; color: var(--text-secondary); }
    .skill-md-preview .md-code { background: #0d1117; color: #e6edf3; border-radius: 8px; padding: 12px 14px; margin: 10px 0; overflow-x: auto; font-size: 12.5px; line-height: 1.6; }
    .skill-md-preview .md-inline-code { background: rgba(94,134,255,0.1); color: var(--primary); padding: 1px 5px; border-radius: 4px; font-size: 12.5px; font-family: monospace; }
    .skill-md-preview .md-table { border-collapse: collapse; margin: 10px 0; width: 100%; font-size: 12.5px; }
    .skill-md-preview .md-table th, .skill-md-preview .md-table td { border: 1px solid var(--border); padding: 6px 10px; text-align: left; }
    .skill-md-preview .md-table th { background: rgba(0,0,0,0.03); font-weight: 600; }
    .skill-md-preview .md-hr { border: none; border-top: 1px solid var(--border); margin: 16px 0; }
    .skill-md-preview a { color: var(--primary); text-decoration: none; }
    .skill-md-preview a:hover { text-decoration: underline; }
    .skill-md-preview strong { font-weight: 600; }
  `],
})
export class SkillsComponent implements OnInit, OnDestroy {
  readonly skills = signal<SkillSummary[]>([]);
  readonly dir = signal('');
  readonly detail = signal<SkillDetail | null>(null);
  readonly error = signal('');
  readonly loading = signal(true);
  readonly copied = signal(false);
  readonly candidates = signal<SkillCandidate[]>([]);
  readonly candError = signal('');
  readonly loadingCandidates = signal(true);
  readonly adopting = signal<SkillCandidate | null>(null);
  readonly adoptDesc = signal('');
  readonly adoptSaving = signal(false);
  readonly previewMode = signal<'preview' | 'raw'>('preview');
  readonly renderedMd = computed(() => this.renderMarkdown(this.detail()?.raw || ''));

  
  /** 弹窗滚动锁：打开时锁 body，关闭/销毁时恢复（防止滚动穿透母页面）。 */
  private readonly bodyLock = effect(() => {
    lockBody(!!(this.adopting()));
  });

  ngOnDestroy(): void {
    unlockBody();
  }

constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
    this.loadCandidates();
  }

  load(): void {
    this.loading.set(true);
    this.api.listSkills().subscribe({
      next: (r) => { this.skills.set(r.skills ?? []); this.dir.set(r.dir || ''); this.loading.set(false); },
      error: (e: Error) => { this.error.set(e.message); this.loading.set(false); },
    });
  }

  loadCandidates(): void {
    this.loadingCandidates.set(true);
    this.api.externalSkillCandidates().subscribe({
      next: (r) => { this.candidates.set(r.candidates || []); this.loadingCandidates.set(false); },
      error: (e: Error) => { this.candError.set('加载失败：' + e.message); this.loadingCandidates.set(false); },
    });
  }

  openAdopt(c: SkillCandidate): void {
    this.adopting.set(c);
    this.adoptDesc.set(c.description || '');
  }

  closeAdopt(): void {
    if (this.adoptSaving()) return;
    this.adopting.set(null);
  }

  confirmAdopt(): void {
    const c = this.adopting();
    if (!c) return;
    this.adoptSaving.set(true);
    this.api.adoptExternalTool(c.name, {
      description: this.adoptDesc(),
      kind: 'skill',
      impl_type: 'none',
    }).subscribe({
      next: () => {
        this.adoptSaving.set(false);
        this.adopting.set(null);
        this.loadCandidates();
      },
      error: (e: Error) => { this.adoptSaving.set(false); this.candError.set('录用失败：' + e.message); },
    });
  }

  unadopt(c: SkillCandidate): void {
    if (!confirm(`取消录用技能「${c.name}」？`)) return;
    this.api.deleteExternalTool(c.name).subscribe({
      next: () => this.loadCandidates(),
      error: (e: Error) => this.candError.set('取消失败：' + e.message),
    });
  }

  open(s: SkillSummary): void {
    this.api.getSkill(s.name).subscribe({
      next: (d) => this.detail.set(d),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  back(): void {
    this.detail.set(null);
  }

  copyRaw(): void {
    const raw = this.detail()?.raw ?? '';
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(raw).then(
        () => { this.copied.set(true); setTimeout(() => this.copied.set(false), 2000); },
        () => {},
      );
    }
  }

  /** 轻量级 Markdown 渲染器（覆盖 SKILL.md 常见语法，无外部依赖） */
  private renderMarkdown(md: string): string {
    if (!md) return '';
    // 1. 先提取代码块，保护里面的内容
    const codeBlocks: string[] = [];
    md = md.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
      const idx = codeBlocks.length;
      codeBlocks.push(`<pre class="md-code"><code class="language-${lang || 'text'}">${this.escapeHtml(code)}</code></pre>`);
      return `\x00CODE${idx}\x00`;
    });
    // 2. 转义 HTML
    md = this.escapeHtml(md);
    // 3. 行内语法
    md = md.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    md = md.replace(/(?<!\*)\*([^*\n]+?)\*(?!\*)/g, '<em>$1</em>');
    md = md.replace(/`([^`]+)`/g, '<code class="md-inline-code">$1</code>');
    md = md.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    // 4. 按行处理块级语法
    const lines = md.split('\n');
    const html: string[] = [];
    let inList = false; let listType = '';
    let inQuote = false;
    let inTable = false; let tableRows: string[] = [];
    const closeList = () => { if (inList) { html.push(`</${listType}>`); inList = false; } };
    const closeQuote = () => { if (inQuote) { html.push('</blockquote>'); inQuote = false; } };
    const closeTable = () => {
      if (inTable && tableRows.length >= 2) {
        const header = tableRows[0].split('|').filter((c: string) => c.trim()).map((c: string) => `<th>${c.trim()}</th>`).join('');
        const body = tableRows.slice(2).map((row: string) => {
          const cells = row.split('|').filter((c: string) => c.trim()).map((c: string) => `<td>${c.trim()}</td>`).join('');
          return `<tr>${cells}</tr>`;
        }).join('');
        html.push(`<table class="md-table"><thead><tr>${header}</tr></thead><tbody>${body}</tbody></table>`);
      }
      inTable = false; tableRows = [];
    };
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) { closeList(); closeQuote(); closeTable(); continue; }
      if (/^---+$/.test(trimmed) || /^\*\*\*+$/.test(trimmed)) {
        closeList(); closeQuote(); closeTable(); html.push('<hr class="md-hr">'); continue;
      }
      const hm = trimmed.match(/^(#{1,6})\s+(.+)$/);
      if (hm) {
        closeList(); closeQuote(); closeTable();
        html.push(`<h${hm[1].length} class="md-h${hm[1].length}">${hm[2]}</h${hm[1].length}>`);
        continue;
      }
      if (trimmed.startsWith('>')) {
        closeList(); closeTable();
        if (!inQuote) { html.push('<blockquote class="md-quote">'); inQuote = true; }
        html.push(`<p>${trimmed.replace(/^>\s?/, '')}</p>`); continue;
      }
      if (trimmed.startsWith('|') && trimmed.endsWith('|')) {
        closeList(); closeQuote();
        if (!inTable) { inTable = true; tableRows = []; }
        tableRows.push(trimmed); continue;
      }
      if (/^[-*]\s+/.test(trimmed)) {
        closeQuote(); closeTable();
        if (!inList || listType !== 'ul') { closeList(); html.push('<ul class="md-ul">'); inList = true; listType = 'ul'; }
        html.push(`<li>${trimmed.replace(/^[-*]\s+/, '')}</li>`); continue;
      }
      if (/^\d+\.\s+/.test(trimmed)) {
        closeQuote(); closeTable();
        if (!inList || listType !== 'ol') { closeList(); html.push('<ol class="md-ol">'); inList = true; listType = 'ol'; }
        html.push(`<li>${trimmed.replace(/^\d+\.\s+/, '')}</li>`); continue;
      }
      closeList(); closeQuote(); closeTable();
      html.push(`<p class="md-p">${trimmed}</p>`);
    }
    closeList(); closeQuote(); closeTable();
    let result = html.join('\n');
    result = result.replace(/\x00CODE(\d+)\x00/g, (_, idx) => codeBlocks[parseInt(idx)]);
    return result;
  }

  private escapeHtml(text: string): string {
    return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }
}
