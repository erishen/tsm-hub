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
  templateUrl: '../html/skills.component.html',
  styleUrls: ['../css/skills.component.css'],
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
