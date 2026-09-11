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
  templateUrl: './models.component.html',
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
