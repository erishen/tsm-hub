import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ApiService } from './api.service';
import { Balance, ProviderBalance } from './models';

@Component({
  selector: 'app-balances',
  standalone: true,
  imports: [CommonModule],
  templateUrl: './balances.component.html',
  styleUrls: ['./balances.component.css'],
})
export class BalancesComponent implements OnInit {
  readonly balances = signal<ProviderBalance[]>([]);
  readonly loading = signal(false);
  readonly error = signal('');
  readonly refreshedAt = signal('');
  readonly probeAt = signal('');

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
        const pa = (r.balances || [])
          .map((b) => b.probe_at)
          .filter(Boolean)
          .sort()
          .pop();
        this.probeAt.set(pa ? new Date(pa).toLocaleString() : '');
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.loading.set(false);
        this.error.set(e.message);
      },
    });
  }

  /** Key 过期：仅 OpenRouter API 返回有效期 */
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

  /** 免费徽标 */
  freeBadge(b?: Balance): string {
    if (!b) return '';
    if (b.kind === 'moonshot' && b.voucher && !b.cash) return '全赠送额度';
    if (b.kind === 'deepseek' && b.granted) return '含赠送';
    if (b.kind === 'openrouter' && b.is_free_tier) return '免费层';
    return '';
  }

  /** Moonshot 赠送券占比 */
  voucherPct(b?: Balance): number {
    if (!b) return 0;
    const total = (b.voucher || 0) + (b.cash || 0);
    return total > 0 ? ((b.voucher || 0) / total) * 100 : 0;
  }

  /** OpenRouter 已用金额 */
  usageAmount(b?: Balance): number {
    if (!b) return 0;
    if (b.total_usage != null) return b.total_usage;
    if (b.total_usage_usd != null) return b.total_usage_usd;
    return b.usage || 0;
  }

  /** OpenRouter 已用百分比（-1 表示无法计算） */
  usagePct(b?: Balance): number {
    if (!b) return -1;
    const used = this.usageAmount(b);
    if (b.total_credits != null && b.total_credits > 0) return (used / b.total_credits) * 100;
    if (b.limit != null && b.limit > 0) return (used / b.limit) * 100;
    return -1;
  }

  /** 解析 platform_note 的 quota 字符串为模型列表 */
  parseQuota(quota?: string): { model: string; amount: string }[] {
    if (!quota) return [];
    return quota.split('·').map((s) => s.trim()).filter(Boolean).map((s) => {
      const parts = s.split(/\s+/);
      if (parts.length >= 2) {
        return { model: parts[0], amount: parts.slice(1).join(' ') };
      }
      return { model: s, amount: '' };
    });
  }
}
