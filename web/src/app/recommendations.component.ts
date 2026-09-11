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
  templateUrl: './recommendations.component.html',
  styleUrls: ['./recommendations.component.css'],
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
