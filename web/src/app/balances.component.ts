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
      <ng-container *ngIf="probeAt()"> · 模型/免费状态快照于 {{ probeAt() }}（探测缓存，模型目录页可更新）</ng-container>
    </div>
    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <!-- loading 骨架 -->
    <div class="page-loading" *ngIf="loading()">
      <div class="skel-row" style="height:80px"></div>
      <div class="skel-row" style="height:80px"></div>
      <div class="skel-row" style="height:80px"></div>
    </div>

    <div class="balance-grid" *ngIf="!loading() && balances().length">
      <div class="balance-card" *ngFor="let b of balances()">
        <!-- 头部：Provider 名称 + 状态 -->
        <div class="balance-head">
          <div class="balance-provider">
            <span class="mono">{{ b.name || b.id }}</span>
            <span class="muted small" *ngIf="b.name && b.name !== b.id">{{ b.id }}</span>
          </div>
          <div class="balance-status">
            <span class="badge ok" *ngIf="b.balance">正常</span>
            <span class="badge warn" *ngIf="!b.balance">{{ statusText(b) }}</span>
            <span class="badge free" *ngIf="freeBadge(b.balance)">{{ freeBadge(b.balance) }}</span>
          </div>
        </div>

        <!-- 额度详情 -->
        <div class="balance-body" *ngIf="b.balance; else noBal">
          <!-- Moonshot / Kimi -->
          <ng-container *ngIf="b.balance?.kind === 'moonshot'">
            <div class="balance-main">
              <span class="balance-amount">¥{{ b.balance?.available?.toFixed(2) }}</span>
              <span class="balance-label">可用余额</span>
            </div>
            <div class="balance-detail-row">
              <div class="detail-item">
                <span class="detail-label">赠送券</span>
                <span class="detail-value">¥{{ b.balance?.voucher?.toFixed(2) }}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">现金</span>
                <span class="detail-value">¥{{ b.balance?.cash?.toFixed(2) }}</span>
              </div>
            </div>
            <div class="progress-bar" *ngIf="(b.balance?.voucher || 0) + (b.balance?.cash || 0) > 0">
              <div class="progress-fill" [style.width.%]="voucherPct(b.balance)"></div>
              <div class="progress-label">赠送 {{ voucherPct(b.balance) | number:'1.0-0' }}% · 现金 {{ 100 - voucherPct(b.balance) | number:'1.0-0' }}%</div>
            </div>
          </ng-container>

          <!-- DeepSeek -->
          <ng-container *ngIf="b.balance?.kind === 'deepseek'">
            <div class="balance-main">
              <span class="balance-amount">{{ b.balance?.total?.toFixed(2) }} {{ b.balance?.currency || '' }}</span>
              <span class="balance-label">总余额</span>
            </div>
            <div class="balance-detail-row">
              <div class="detail-item">
                <span class="detail-label">赠送</span>
                <span class="detail-value">{{ b.balance?.granted?.toFixed(2) }} {{ b.balance?.currency || '' }}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">充值</span>
                <span class="detail-value">{{ b.balance?.topped_up?.toFixed(2) }} {{ b.balance?.currency || '' }}</span>
              </div>
            </div>
          </ng-container>

          <!-- OpenRouter -->
          <ng-container *ngIf="b.balance?.kind === 'openrouter'">
            <div class="balance-main" *ngIf="b.balance?.total_credits">
              <span class="balance-amount">\${{ b.balance?.total_credits?.toFixed(2) }}</span>
              <span class="balance-label">总额度</span>
            </div>
            <div class="balance-main" *ngIf="!b.balance?.total_credits && b.balance?.limit">
              <span class="balance-amount">\${{ b.balance?.limit?.toFixed(2) }}</span>
              <span class="balance-label">额度上限</span>
            </div>
            <div class="balance-main" *ngIf="!b.balance?.total_credits && !b.balance?.limit">
              <span class="balance-amount">无上限</span>
              <span class="balance-label">额度</span>
            </div>
            <div class="balance-detail-row">
              <div class="detail-item">
                <span class="detail-label">已用</span>
                <span class="detail-value">\${{ usageAmount(b.balance)?.toFixed(2) }}</span>
              </div>
              <div class="detail-item" *ngIf="b.balance?.limit_remaining">
                <span class="detail-label">剩余</span>
                <span class="detail-value">\${{ b.balance?.limit_remaining?.toFixed(2) }}</span>
              </div>
              <div class="detail-item" *ngIf="b.balance?.hard_limit_usd">
                <span class="detail-label">订阅上限</span>
                <span class="detail-value">\${{ b.balance?.hard_limit_usd?.toFixed(2) }}</span>
              </div>
            </div>
            <div class="progress-bar" *ngIf="usagePct(b.balance) > -1">
              <div class="progress-fill" [class.warn]="usagePct(b.balance) > 80" [style.width.%]="usagePct(b.balance)"></div>
              <div class="progress-label">已用 {{ usagePct(b.balance) | number:'1.0-0' }}%</div>
            </div>
            <div class="balance-meta" *ngIf="b.balance?.expires_at">
              <span class="meta-label">有效期至</span>
              <span class="meta-value" [class.warn]="keyExpiryClass(b.balance) === 'warn'" [class.bad]="keyExpiryClass(b.balance) === 'err'">
                {{ keyExpiry(b.balance) }}
              </span>
            </div>
          </ng-container>

          <!-- OpenAI -->
          <ng-container *ngIf="b.balance?.kind === 'openai'">
            <div class="balance-main" *ngIf="b.balance?.hard_limit_usd">
              <span class="balance-amount">\${{ b.balance?.hard_limit_usd?.toFixed(2) }}</span>
              <span class="balance-label">订阅上限</span>
            </div>
            <div class="balance-detail-row">
              <div class="detail-item">
                <span class="detail-label">已用</span>
                <span class="detail-value">\${{ b.balance?.total_usage_usd?.toFixed(2) }}</span>
              </div>
            </div>
          </ng-container>

          <!-- Platform Note（无公开余额接口） -->
          <ng-container *ngIf="b.balance?.kind === 'platform_note'">
            <div class="balance-main">
              <span class="balance-plan">{{ b.balance?.plan || '免费套餐' }}</span>
            </div>
            <div class="quota-list" *ngIf="b.balance?.quota">
              <div class="quota-item" *ngFor="let q of parseQuota(b.balance?.quota)">
                <span class="quota-model">{{ q.model }}</span>
                <span class="quota-amount">{{ q.amount }}</span>
              </div>
            </div>
            <div class="balance-meta" *ngIf="b.balance?.reset">
              <span class="meta-label">重置周期</span>
              <span class="meta-value">{{ b.balance?.reset }}</span>
            </div>
            <div class="balance-note" *ngIf="b.balance?.note">{{ b.balance?.note }}</div>
          </ng-container>

          <!-- 控制台链接 -->
          <div class="balance-footer" *ngIf="b.balance?.url">
            <a [href]="b.balance?.url" target="_blank" rel="noopener" class="console-link">
              前往控制台查看详情 ↗
            </a>
          </div>
        </div>

        <ng-template #noBal>
          <div class="balance-empty">
            <span class="muted">{{ b.error === 'no_key' ? '未配置 Key' : '无法获取额度' }}</span>
          </div>
        </ng-template>
      </div>
    </div>

    <div class="empty" *ngIf="!loading() && !balances().length">还没有 Provider，或全部无 Key</div>
  `,
  styles: [`
    .balance-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 14px; margin-top: 12px; }
    .balance-card { background: var(--card-color); border: 1px solid var(--border-color); border-radius: 12px; padding: 16px; display: flex; flex-direction: column; gap: 12px; }
    .balance-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 8px; }
    .balance-provider { display: flex; flex-direction: column; gap: 2px; }
    .balance-provider .mono { font-size: 15px; font-weight: 600; }
    .balance-status { display: flex; gap: 6px; flex-shrink: 0; flex-wrap: wrap; justify-content: flex-end; }

    .balance-body { display: flex; flex-direction: column; gap: 10px; }
    .balance-main { display: flex; align-items: baseline; gap: 8px; }
    .balance-amount { font-size: 28px; font-weight: 700; color: var(--text-color); }
    .balance-plan { font-size: 18px; font-weight: 600; color: var(--text-color); }
    .balance-label { font-size: 12px; color: var(--text-secondary); }

    .balance-detail-row { display: flex; gap: 16px; flex-wrap: wrap; }
    .detail-item { display: flex; flex-direction: column; gap: 2px; }
    .detail-label { font-size: 11px; color: var(--text-secondary); }
    .detail-value { font-size: 14px; font-weight: 600; color: var(--text-color); }

    .progress-bar { position: relative; height: 20px; background: rgba(0,0,0,0.05); border-radius: 10px; overflow: hidden; }
    .progress-fill { height: 100%; background: linear-gradient(90deg, #52c41a, #73d13d); border-radius: 10px; transition: width 0.3s; min-width: 2px; }
    .progress-fill.warn { background: linear-gradient(90deg, #faad14, #ffc53d); }
    .progress-label { position: absolute; top: 50%; left: 50%; transform: translate(-50%, -50%); font-size: 11px; color: var(--text-color); font-weight: 600; white-space: nowrap; }

    .balance-meta { display: flex; justify-content: space-between; align-items: center; padding-top: 8px; border-top: 1px solid rgba(0,0,0,0.06); }
    .meta-label { font-size: 11px; color: var(--text-secondary); }
    .meta-value { font-size: 12px; color: var(--text-color); font-weight: 500; }
    .meta-value.warn { color: #faad14; }
    .meta-value.bad { color: #ea6668; }

    .quota-list { display: flex; flex-direction: column; gap: 6px; }
    .quota-item { display: flex; justify-content: space-between; align-items: center; padding: 6px 10px; background: rgba(0,0,0,0.02); border-radius: 6px; }
    .quota-model { font-size: 12px; font-family: monospace; color: var(--text-color); }
    .quota-amount { font-size: 12px; font-weight: 600; color: #52c41a; white-space: nowrap; }

    .balance-note { font-size: 11px; color: var(--text-secondary); line-height: 1.5; padding: 8px 10px; background: rgba(94,134,255,0.05); border-radius: 6px; border-left: 3px solid #5e86ff; }

    .balance-footer { margin-top: auto; padding-top: 10px; border-top: 1px solid rgba(0,0,0,0.06); }
    .console-link { font-size: 12px; color: #5e86ff; text-decoration: none; }
    .console-link:hover { text-decoration: underline; }

    .balance-empty { padding: 20px 0; text-align: center; }
  `],
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
