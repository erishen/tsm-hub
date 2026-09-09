import { Component, OnInit, computed, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { CatalogModel } from './models';

const CATEGORY_LABEL: Record<string, string> = {
  text: '文本对话 / 编码',
  vision: '视觉理解',
  image: '图像生成',
  video: '视频生成',
  audio: '音频生成',
  embedding: '向量嵌入',
  other: '专用 / 其他',
};

@Component({
  selector: 'app-models',
  standalone: true,
  imports: [CommonModule, FormsModule],
  template: `
    <div class="page-head">
      <div>
        <h1>模型目录</h1>
        <div class="sub">各 Provider 已配置模型的归类与用途（知识表核验于 2026-09-09；未收录模型按 id 推断）</div>
      </div>
    </div>

    <div style="display:flex;gap:12px;align-items:center;margin-bottom:10px;flex-wrap:wrap">
      <input [(ngModel)]="q" placeholder="搜索模型 id / 用途 / Provider…" style="min-width:280px" />
      <span class="muted small">共 {{ models().length }} 个模型 · {{ freeCount() }} 个免费</span>
    </div>
    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <ng-container *ngFor="let cat of categories()">
      <ng-container *ngIf="byCategory()[cat]?.length">
        <h3 style="margin:18px 0 8px">
          {{ CATEGORY_LABEL[cat] }}
          <span class="muted small" style="font-weight:400">（{{ byCategory()[cat].length }}）</span>
        </h3>
        <table>
          <thead>
            <tr><th>模型</th><th>Provider</th><th>上下文</th><th>价格</th><th>用途</th></tr>
          </thead>
          <tbody>
            <tr *ngFor="let m of byCategory()[cat]">
              <td class="mono">{{ m.id }}</td>
              <td>
                <span class="badge ok" *ngFor="let p of m.providers" style="margin-right:4px">{{ p }}</span>
              </td>
              <td class="muted">{{ m.context || '—' }}</td>
              <td>
                <span class="badge free" *ngIf="m.free">FREE</span>
                <span class="muted small" *ngIf="!m.free && m.pricing">$ {{ m.pricing.prompt }}/{{ m.pricing.completion }}M</span>
                <span class="muted" *ngIf="!m.free && !m.pricing">—</span>
              </td>
              <td>{{ m.purpose }}</td>
            </tr>
          </tbody>
        </table>
      </ng-container>
    </ng-container>

    <div class="empty" *ngIf="!loading() && !models().length">还没有配置模型，去 Providers 页添加</div>
  `,
})
export class ModelsComponent implements OnInit {
  readonly models = signal<CatalogModel[]>([]);
  readonly loading = signal(false);
  readonly error = signal('');
  q = '';

  readonly CATEGORY_LABEL = CATEGORY_LABEL;

  /** 过滤后的模型 */
  readonly filtered = computed(() => {
    const s = this.q.trim().toLowerCase();
    if (!s) return this.models();
    return this.models().filter(
      (m) =>
        m.id.toLowerCase().includes(s) ||
        m.purpose.toLowerCase().includes(s) ||
        m.providers.some((p) => p.toLowerCase().includes(s))
    );
  });

  readonly freeCount = computed(() => this.models().filter((m) => m.free).length);

  readonly categories = computed(() => {
    const seen: string[] = [];
    for (const m of this.filtered()) {
      if (!seen.includes(m.category)) seen.push(m.category);
    }
    // 固定展示顺序
    return Object.keys(CATEGORY_LABEL).filter((c) => seen.includes(c));
  });

  readonly byCategory = computed(() => {
    const g: Record<string, CatalogModel[]> = {};
    for (const m of this.filtered()) {
      if (!g[m.category]) g[m.category] = [];
      g[m.category].push(m);
    }
    return g;
  });

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.loading.set(true);
    this.api.modelsCatalog().subscribe({
      next: (r) => {
        this.models.set(r.models);
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.loading.set(false);
        this.error.set(e.message);
      },
    });
  }
}
