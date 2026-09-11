import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { RouterLink } from '@angular/router';
import { ApiService } from './api.service';
import { AppScenario, RecommendationsResp } from './models';

@Component({
  selector: 'app-recommendations',
  standalone: true,
  imports: [CommonModule, FormsModule, RouterLink],
  template: `
    <div class="page-head">
      <div>
        <h1>应用推荐</h1>
        <div class="sub">根据当前模型池能力（{{ total() }} 个模型，{{ freeCount() }} 个免费），推荐可做的应用开发场景</div>
      </div>
      <button class="primary" (click)="load()" [disabled]="loading()">
        {{ loading() ? '刷新中…' : '刷新推荐' }}
      </button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <!-- 统计卡片 -->
    <div class="stat-row" *ngIf="!loading() && scenarios().length">
      <div class="stat-card">
        <div class="stat-num">{{ total() }}</div>
        <div class="stat-label">可用模型</div>
      </div>
      <div class="stat-card">
        <div class="stat-num free">{{ freeCount() }}</div>
        <div class="stat-label">免费模型</div>
      </div>
      <div class="stat-card">
        <div class="stat-num">{{ scenarios().length }}</div>
        <div class="stat-label">推荐场景</div>
      </div>
      <div class="stat-card">
        <div class="stat-num">{{ freePct() }}%</div>
        <div class="stat-label">免费占比</div>
      </div>
    </div>

    <!-- loading 骨架 -->
    <div class="page-loading" *ngIf="loading()">
      <div class="skel-row" style="height:120px"></div>
      <div class="skel-row" style="height:200px"></div>
      <div class="skel-row" style="height:200px"></div>
    </div>

    <!-- 场景卡片网格 -->
    <div class="scenario-grid" *ngIf="!loading() && scenarios().length">
      <div class="scenario-card" *ngFor="let sc of scenarios()">
        <div class="scenario-head">
          <span class="scenario-icon">{{ sc.icon }}</span>
          <div class="scenario-title">
            <h3>{{ sc.name }}</h3>
            <div class="scenario-desc">{{ sc.description }}</div>
          </div>
        </div>

        <!-- 关键能力标签 -->
        <div class="feature-tags">
          <span class="tag" *ngFor="let f of sc.features">{{ f }}</span>
        </div>

        <!-- 推荐模型 -->
        <div class="rec-models">
          <div class="rec-model-label">推荐模型（免费优先）</div>
          <div class="rec-model-item" *ngFor="let m of sc.models; let i = index">
            <span class="rec-rank">{{ i + 1 }}</span>
            <div class="rec-model-info">
              <div class="rec-model-name">
                <span class="mono">{{ m.id }}</span>
                <span class="badge free" *ngIf="m.free">FREE</span>
                <span class="badge" *ngIf="!m.free" style="background:#f0f0f0;color:#888">付费</span>
              </div>
              <div class="rec-model-meta">
                <span class="badge ok">{{ m.provider }}</span>
                <span class="muted small" *ngIf="m.context_length">{{ formatCtx(m.context_length) }}</span>
                <span class="muted small">评分 {{ m.route_score }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- 示例提示词 -->
        <div class="example-box">
          <div class="example-label">示例提示词</div>
          <div class="example-text">{{ sc.example }}</div>
          <button class="btn-small" (click)="copyExample(sc.example)">复制</button>
        </div>

        <!-- 操作 -->
        <div class="scenario-actions">
          <a class="btn-small primary" [routerLink]="['/playground']" [queryParams]="{ model: sc.models[0].id }">去测试</a>
          <a class="btn-small" [routerLink]="['/models']">查看全部模型</a>
        </div>
      </div>
    </div>

    <div class="empty" *ngIf="!loading() && !scenarios().length && !error()">
      暂无可推荐的应用场景，请先在 Providers 页配置模型
    </div>
  `,
  styles: [`
    .stat-row { display: flex; gap: 12px; margin: 16px 0; flex-wrap: wrap; }
    .stat-card { flex: 1; min-width: 120px; padding: 16px; background: var(--card-color); border: 1px solid var(--border-color); border-radius: 10px; text-align: center; }
    .stat-num { font-size: 28px; font-weight: 700; color: var(--text-color); }
    .stat-num.free { color: #52c41a; }
    .stat-label { font-size: 12px; color: var(--text-secondary); margin-top: 4px; }

    .scenario-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(420px, 1fr)); gap: 16px; margin-top: 16px; }
    .scenario-card { background: var(--card-color); border: 1px solid var(--border-color); border-radius: 12px; padding: 18px; display: flex; flex-direction: column; gap: 14px; }
    .scenario-head { display: flex; gap: 12px; align-items: flex-start; }
    .scenario-icon { font-size: 32px; line-height: 1; flex-shrink: 0; }
    .scenario-title h3 { margin: 0 0 4px; font-size: 16px; }
    .scenario-desc { font-size: 13px; color: var(--text-secondary); line-height: 1.5; }

    .feature-tags { display: flex; gap: 6px; flex-wrap: wrap; }
    .tag { font-size: 11px; padding: 2px 8px; border-radius: 10px; background: rgba(94,134,255,0.1); color: #5e86ff; }

    .rec-models { background: rgba(0,0,0,0.02); border-radius: 8px; padding: 10px 12px; }
    .rec-model-label { font-size: 11px; color: var(--text-secondary); margin-bottom: 8px; font-weight: 600; }
    .rec-model-item { display: flex; gap: 10px; align-items: center; padding: 6px 0; border-bottom: 1px solid rgba(0,0,0,0.05); }
    .rec-model-item:last-child { border-bottom: none; }
    .rec-rank { width: 20px; height: 20px; border-radius: 50%; background: #5e86ff; color: #fff; font-size: 11px; display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
    .rec-model-info { flex: 1; min-width: 0; }
    .rec-model-name { display: flex; align-items: center; gap: 6px; font-size: 13px; }
    .rec-model-name .mono { font-family: monospace; font-size: 12px; }
    .rec-model-meta { display: flex; gap: 8px; align-items: center; margin-top: 3px; }

    .example-box { background: rgba(0,0,0,0.02); border-radius: 8px; padding: 10px 12px; position: relative; }
    .example-label { font-size: 11px; color: var(--text-secondary); margin-bottom: 6px; font-weight: 600; }
    .example-text { font-size: 12px; color: var(--text-color); line-height: 1.6; white-space: pre-wrap; padding-right: 50px; }
    .btn-small { font-size: 11px; padding: 3px 10px; border-radius: 6px; border: 1px solid var(--border-color); background: var(--card-color); cursor: pointer; color: var(--text-color); }
    .btn-small.primary { background: #5e86ff; color: #fff; border-color: #5e86ff; }
    .example-box .btn-small { position: absolute; top: 8px; right: 8px; }

    .scenario-actions { display: flex; gap: 8px; margin-top: auto; }
    .scenario-actions .btn-small { text-decoration: none; display: inline-flex; align-items: center; }
  `],
})
export class RecommendationsComponent implements OnInit {
  readonly scenarios = signal<AppScenario[]>([]);
  readonly total = signal(0);
  readonly freeCount = signal(0);
  readonly loading = signal(false);
  readonly error = signal('');

  readonly freePct = () => (this.total() ? Math.round((this.freeCount() / this.total()) * 100) : 0);

  constructor(public api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.loading.set(true);
    this.error.set('');
    this.api.getModelRecommendations().subscribe({
      next: (resp: RecommendationsResp) => {
        this.scenarios.set(resp.scenarios || []);
        this.total.set(resp.total_models || 0);
        this.freeCount.set(resp.free_models || 0);
        this.loading.set(false);
      },
      error: (err: any) => {
        this.error.set(err?.error?.error || err?.message || '加载失败');
        this.loading.set(false);
      },
    });
  }

  formatCtx(n: number): string {
    if (n >= 1000000) return (n / 1000000).toFixed(0) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(0) + 'K';
    return String(n);
  }

  copyExample(text: string): void {
    navigator.clipboard.writeText(text).catch(() => {});
  }
}
