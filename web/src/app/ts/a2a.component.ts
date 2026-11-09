import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from './api.service';
import { A2AInvokeResp, A2AOverview, A2ATask, A2ATasksResp } from './models';

@Component({
  selector: 'app-a2a',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/a2a.component.html',
  styleUrls: ['../css/a2a.component.css'],
})
export class A2aComponent implements OnInit {
  readonly overview = signal<A2AOverview | null>(null);
  readonly skills = signal<any[]>([]);
  readonly tasks = signal<A2ATask[]>([]);
  readonly loading = signal(true);
  readonly error = signal('');

  // ── 快速调用 ──
  readonly invokeTool = signal('tsm.overview');
  readonly invokeArgs = signal('{}');
  readonly invoking = signal(false);
  readonly invokeResult = signal<A2AInvokeResp | null>(null);

  // 展开的任务轨迹（按 task id）
  readonly openTasks = signal<Record<string, boolean>>({});

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
    this.loadSkills();
  }

  load(): void {
    this.loading.set(true);
    this.error.set('');
    // overview 与 tasks 并行；agent card（公开端点）提供能力详情
    Promise.all([
      this.toPromise(this.api.a2aOverview()),
      this.toPromise(this.api.a2aTasks(50)),
      this.toPromise(this.api.a2aAgentCard()),
    ]).then(([ov, tr, card]) => {
      this.overview.set(ov ?? null);
      this.tasks.set(tr?.tasks ?? []);
      if (card?.skills) this.skills.set(card.skills);
      this.loading.set(false);
    }).catch((e: Error) => {
      this.error.set('加载失败：' + e.message);
      this.loading.set(false);
    });
  }

  loadSkills(): void {
    this.api.a2aAgentCard().subscribe({
      next: (card) => { if (card?.skills) this.skills.set(card.skills); },
      error: () => {}, // 能力清单获取失败不阻塞整页（overview 里仍有数量）
    });
  }

  toggleTask(id: string): void {
    const cur = this.openTasks();
    this.openTasks.set({ ...cur, [id]: !cur[id] });
  }

  invoke(): void {
    let args: Record<string, any> = {};
    const raw = this.invokeArgs().trim();
    if (raw && raw !== '{}') {
      try {
        args = JSON.parse(raw);
      } catch {
        this.invokeResult.set({ ok: false, tool: this.invokeTool(), error: 'args 不是合法 JSON' });
        return;
      }
    }
    this.invoking.set(true);
    this.invokeResult.set(null);
    this.api.a2aInvoke(this.invokeTool(), args).subscribe({
      next: (r) => { this.invokeResult.set(r); this.invoking.set(false); this.load(); },
      error: (e: Error) => {
        this.invokeResult.set({ ok: false, tool: this.invokeTool(), error: e.message });
        this.invoking.set(false);
      },
    });
  }

  /** 时间戳（unix 秒）→ 本地可读时间 */
  fmtTs(ts: number): string {
    if (!ts) return '';
    return new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false });
  }

  /** 秒 → 可读时长（d h m s） */
  fmtDuration(secs?: number): string {
    if (secs === undefined || secs === null) return '';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    const s = secs % 60;
    const parts: string[] = [];
    if (d) parts.push(d + 'd');
    if (h) parts.push(h + 'h');
    if (m) parts.push(m + 'm');
    if (!d && !h && !m) parts.push(s + 's');
    return parts.join(' ');
  }

  /** 选中一个能力到"快速调用"（来自能力卡片点击） */
  selectTool(id: string): void {
    this.invokeTool.set(id);
    this.fillExample(id);
  }

  /** 下拉手动切换能力：参数还是默认/空时才预填示例，不动用户已写的内容 */
  onToolChange(id: string): void {
    this.invokeTool.set(id);
    const cur = this.invokeArgs().trim();
    if (!cur || cur === '{}') {
      this.fillExample(id);
    }
  }

  /** 用该能力 Agent Card 里的 examples[0] 预填参数（美化成 2 空格缩进） */
  private fillExample(toolId: string): void {
    const cap = this.skills().find((s: any) => s.id === toolId);
    const ex = cap?.examples?.[0];
    if (!ex) {
      this.invokeArgs.set('{}');
      return;
    }
    try {
      this.invokeArgs.set(JSON.stringify(JSON.parse(ex), null, 2));
    } catch {
      this.invokeArgs.set(ex);
    }
  }

  /** 状态 → css class（驱动 badge 颜色） */
  stateClass(state: string): string {
    switch (state) {
      case 'completed': return 'ok';
      case 'failed':
      case 'rejected': return 'err';
      case 'input-required':
      case 'working':
      case 'submitted': return 'warn';
      case 'canceled': return 'muted';
      default: return '';
    }
  }

  /** 取任务第一个 artifact 的正文（用于轨迹区展示） */
  firstArtifact(t: A2ATask): string {
    const arts = t.artifacts || [];
    if (!arts.length) return '';
    const parts = arts[0]?.parts || [];
    return parts.map((p: any) => p.text || '').join('\n');
  }

  /** result 文本尝试 JSON 美化（a2a 的 artifact 正文多为 JSON 字符串） */
  prettyJson(text: string): string {
    if (!text) return '';
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  }

  private toPromise<T>(obs: import('rxjs').Observable<T>): Promise<T> {
    return new Promise((resolve, reject) => {
      obs.subscribe({ next: (v) => resolve(v), error: (e) => reject(e) });
    });
  }
}
