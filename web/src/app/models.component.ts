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
  specialized: '专用能力（OCR/重排/笔记）',
  other: '内容安全 / 其他',
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
      <button class="primary" (click)="load()" [disabled]="loading()">
        {{ loading() ? '探测中…' : '重新探测' }}
      </button>
    </div>

    <div style="display:flex;gap:12px;align-items:center;margin-bottom:10px;flex-wrap:wrap">
      <input [(ngModel)]="q" placeholder="搜索模型 id / 用途 / Provider…" style="min-width:280px" />
      <span class="muted small">共 {{ models().length }} 条模型记录 · {{ freeCount() }} 个免费（同模型不同 Provider 分行显示）</span>
    </div>
    <div class="muted" style="margin-bottom:8px">
      <ng-container *ngIf="probeAt()">免费/价格状态来自最近一次探测（{{ probeAt() }}，缓存数据），点「重新探测」更新</ng-container>
      <ng-container *ngIf="!probeAt() && !loading()">尚未探测过，点「重新探测」获取实时免费/价格/上下文</ng-container>
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
            <tr><th>模型</th><th>Provider</th><th>评分</th><th>上下文</th><th>价格</th><th>用途</th></tr>
          </thead>
          <tbody>
            <tr *ngFor="let m of byCategory()[cat]" [style.opacity]="m.unavailable ? 0.55 : 1">
              <td class="mono">
                {{ m.id }}
                <span class="badge bad" *ngIf="m.unavailable" [title]="m.unavailable">不可用：{{ m.unavailable.length > 30 ? m.unavailable.slice(0,30)+'…' : m.unavailable }}</span>
              </td>
              <td><span class="badge ok">{{ m.provider }}</span></td>
              <td style="white-space:nowrap">
                <span class="badge" [class.free]="(m.route_score ?? 0) >= 150" [class.bad]="(m.route_score ?? 0) < 50"
                      [title]="'免费+100 / 健康+30 / 延迟分 / 在路由+50 / 不可用-100'">
                  {{ m.route_score ?? 0 }}
                </span>
                <span class="badge" *ngIf="!m.in_route" style="margin-left:4px;background:#f0f0f0;color:#888">未接入</span>
              </td>
              <td class="muted">{{ ctx(m) }}</td>
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
  readonly probeAt = signal('');
  q = '';

  readonly CATEGORY_LABEL = CATEGORY_LABEL;

  /** 上下文：最近一次探测的实时值优先，静态知识表兜底。 */
  ctx(m: CatalogModel): string {
    if (m.context_length) {
      const n = m.context_length;
      if (n >= 1048576) return `${Math.round(n / 1048576)}M`;
      if (n >= 1024) return `${Math.round(n / 1024)}K`;
      return `${n}`;
    }
    return m.context || '—';
  }

  /** 过滤后的模型 */
  readonly filtered = computed(() => {
    const s = this.q.trim().toLowerCase();
    if (!s) return this.models();
    return this.models().filter(
      (m) =>
        m.id.toLowerCase().includes(s) ||
        m.purpose.toLowerCase().includes(s) ||
        m.provider.toLowerCase().includes(s)
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
    // 默认只读缓存快照，不发起上游探测；点「重新探测」才实时查询。
    this.api.modelsCatalog().subscribe({
      next: (r) => {
        this.models.set(r.models);
        if (r.probe_at) this.probeAt.set(new Date(r.probe_at).toLocaleString());
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.loading.set(false);
        this.error.set(e.message);
      },
    });
  }

  /** 手动重新探测所有 Provider（并行、15s 内完成），失败项不阻塞。 */
  load(): void {
    this.loading.set(true);
    this.error.set('');
    this.api.refreshModels().subscribe({
      next: (r) => {
        this.models.set(r.models);
        if (r.probe_at) this.probeAt.set(new Date(r.probe_at).toLocaleString());
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.loading.set(false);
        this.error.set(e.message);
      },
    });
  }
}
