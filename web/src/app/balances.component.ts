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
      <ng-container *ngIf="refreshedAt()">更新于 {{ refreshedAt() }}（缓存数据，点「刷新」查询最新）</ng-container>
      <ng-container *ngIf="!refreshedAt() && !loading()">尚未查询过，点「刷新」查询各 Provider 额度</ng-container>
    </div>
    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <table *ngIf="balances().length; else none">
      <thead>
        <tr>
          <th>Provider</th><th>额度 / token 可使用总量</th><th>Key 过期</th><th>状态</th>
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
              <strong [title]="balanceNote(b.balance!)">{{ balanceText(b.balance!) }}</strong>
              <a *ngIf="b.balance!.url" [href]="b.balance!.url" target="_blank" rel="noopener"
                 class="muted small" style="margin-left:8px">控制台 ↗</a>
              <span class="badge free" *ngIf="freeBadge(b.balance!)" style="margin-left:8px">{{ freeBadge(b.balance!) }}</span>
            </ng-container>
            <ng-template #noBal><span class="muted">—</span></ng-template>
          </td>
          <td>
            <ng-container *ngIf="b.balance">
              <span class="badge warn" *ngIf="keyExpiryClass(b.balance!) === 'warn'">{{ keyExpiry(b.balance!) }}</span>
              <span class="badge bad" *ngIf="keyExpiryClass(b.balance!) === 'err'">{{ keyExpiry(b.balance!) }}</span>
              <span class="muted" *ngIf="!keyExpiryClass(b.balance!)">{{ keyExpiry(b.balance!) }}</span>
            </ng-container>
            <ng-container *ngIf="!b.balance"><span class="muted">—</span></ng-container>
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
        this.refreshedAt.set(r.at ? new Date(r.at).toLocaleTimeString() : '');
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.loading.set(false);
        this.error.set(e.message);
      },
    });
  }

  /** Key 过期：仅 OpenRouter API 返回有效期；临近 14 天显示剩余天数（黄），已过期标红。 */
  keyExpiry(b?: Balance): string {
    if (!b || b.kind !== 'openrouter' || !b.expires_at) return '—';
    const days = Math.ceil((new Date(b.expires_at).getTime() - Date.now()) / 86400000);
    const base = b.expires_at.slice(0, 10);
    if (days < 0) return base + '（已过期）';
    if (days <= 14) return base + `（剩 ${days} 天）`;
    return base;
  }

  keyExpiryClass(b?: Balance): string {
    if (!b || b.kind !== 'openrouter' || !b.expires_at) return '';
    const days = Math.ceil((new Date(b.expires_at).getTime() - Date.now()) / 86400000);
    if (days < 0) return 'err';
    if (days <= 14) return 'warn';
    return '';
  }

  statusText(b: ProviderBalance): string {
    if (b.error === 'no_key') return '未配置 Key';
    return '无法获取额度';
  }

  /** 免费徽标：Moonshot 券余额>0 且现金=0 → 全赠送；DeepSeek 含赠送；OpenRouter 免费层。 */
  freeBadge(b?: Balance): string {
    if (!b) return '';
    if (b.kind === 'moonshot' && b.voucher && !b.cash) return '全赠送额度';
    if (b.kind === 'deepseek' && b.granted) return '含赠送';
    if (b.kind === 'openrouter' && b.is_free_tier) return '免费层';
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
      case 'openrouter': {
        // /credits 接口：总额度 total_credits + 总已用 total_usage
        if (b.total_credits != null) {
          return `已用 $${b.total_usage?.toFixed(2) ?? '0.00'} / 总额度 $${b.total_credits.toFixed(2)}`;
        }
        const used = `已用 $${b.usage?.toFixed(2) ?? '0.00'}`;
        const limit = b.limit != null
          ? ` / 上限 $${b.limit}`
          : '（无额度上限）';
        const exp = b.expires_at ? ` · 有效期至 ${b.expires_at.slice(0, 10)}` : '';
        return used + limit + exp;
      }
      case 'platform_note': {
        const parts = [b.plan ?? '', b.quota ?? '', b.reset ?? ''].filter(Boolean);
        return parts.length ? parts.join(' · ') : '—';
      }
      default:
        return '';
    }
  }

  /** 无公开余额接口平台的说明（作为 hover 提示）。 */
  balanceNote(b?: Balance): string {
    if (!b || b.kind !== 'platform_note') return '';
    return b.note ?? '';
  }
}
