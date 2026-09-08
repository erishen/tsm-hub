import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ApiService } from './api.service';
import { Balance, ProviderBalance } from './models';

@Component({
  selector: 'app-balances',
  standalone: true,
  imports: [CommonModule],
  template: `
    <div class="page-head">
      <div>
        <h1>额度</h1>
        <div class="sub">用各 Provider 已保存的 Key 查询账户余额 / token 可使用总量（自动探测，无需每次输入 Key）</div>
      </div>
      <button class="primary" (click)="load(true)" [disabled]="loading()">
        {{ loading() ? '查询中…' : '刷新' }}
      </button>
    </div>

    <div class="muted" style="margin-bottom:8px">
      <ng-container *ngIf="refreshedAt()">更新于 {{ refreshedAt() }}</ng-container>
      <ng-container *ngIf="cached()">（5 分钟内缓存，点「刷新」强制更新）</ng-container>
    </div>
    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <table *ngIf="balances().length; else none">
      <thead>
        <tr>
          <th>Provider</th><th>额度 / token 可使用总量</th><th>状态</th>
        </tr>
      </thead>
      <tbody>
        <tr *ngFor="let b of balances()">
          <td>
            <span class="mono">{{ b.name || b.id }}</span>
            <span class="muted small" style="margin-left:6px">{{ b.id }}</span>
          </td>
          <td>
            <ng-container *ngIf="b.balance; else noBal">
              <strong>{{ balanceText(b.balance!) }}</strong>
              <span class="badge free" *ngIf="freeBadge(b.balance!)" style="margin-left:8px">{{ freeBadge(b.balance!) }}</span>
            </ng-container>
            <ng-template #noBal><span class="muted">—</span></ng-template>
          </td>
          <td>
            <ng-container *ngIf="b.balance">
              <span class="badge ok">正常</span>
            </ng-container>
            <ng-container *ngIf="!b.balance">
              <span class="badge warn">{{ statusText(b) }}</span>
            </ng-container>
          </td>
        </tr>
      </tbody>
    </table>
    <ng-template #none>
      <div class="empty" *ngIf="!loading()">还没有 Provider，或全部无 Key</div>
    </ng-template>
  `,
})
export class BalancesComponent implements OnInit {
  readonly balances = signal<ProviderBalance[]>([]);
  readonly loading = signal(false);
  readonly error = signal('');
  readonly refreshedAt = signal('');
  readonly cached = signal(false);

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load(false);
  }

  load(force: boolean): void {
    this.loading.set(true);
    this.error.set('');
    this.api.providerBalances(force).subscribe({
      next: (r) => {
        this.balances.set(r.balances);
        this.refreshedAt.set(new Date(r.at).toLocaleTimeString());
        this.cached.set(!force);
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.loading.set(false);
        this.error.set(e.message);
      },
    });
  }

  statusText(b: ProviderBalance): string {
    if (b.error === 'no_key') return '未配置 Key';
    return '无法获取额度';
  }

  /** 免费徽标：Moonshot 券余额>0 且现金=0 → 全赠送；DeepSeek 含赠送。 */
  freeBadge(b?: Balance): string {
    if (!b) return '';
    if (b.kind === 'moonshot' && b.voucher && !b.cash) return '全赠送额度';
    if (b.kind === 'deepseek' && b.granted) return '含赠送';
    return '';
  }

  balanceText(b: Balance): string {
    switch (b.kind) {
      case 'moonshot':
        return `可用 ¥${b.available?.toFixed(2)} · 券 ¥${b.voucher?.toFixed(2)} · 现金 ¥${b.cash?.toFixed(2)}`;
      case 'deepseek': {
        const cur = b.currency || '';
        return `总余额 ${b.total?.toFixed(2)}${cur} · 赠送 ${b.granted?.toFixed(2)}${cur} · 充值 ${b.topped_up?.toFixed(2)}${cur}`;
      }
      case 'openai':
        return b.hard_limit_usd
          ? `订阅上限 $${b.hard_limit_usd}`
          : `已用 $${b.total_usage_usd?.toFixed(2)}`;
      default:
        return '';
    }
  }
}
