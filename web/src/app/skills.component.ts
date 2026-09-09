import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ApiService } from './api.service';
import { SkillDetail, SkillSummary } from './models';

@Component({
  selector: 'app-skills',
  standalone: true,
  imports: [CommonModule],
  template: `
    <div class="page-head">
      <div>
        <h1>技能库</h1>
        <div class="sub" *ngIf="dir()">挂载自 <span class="mono">{{ dir() }}</span>（Agent Skills 标准）</div>
        <div class="sub" *ngIf="!dir()">未配置 skills_dir，可在 data/config.json 的 settings 里指向技能库目录</div>
      </div>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="card" *ngIf="detail()">
      <div style="display:flex;align-items:flex-start;gap:10px">
        <div style="flex:1">
          <h2 style="margin:0">
            <span class="mono">{{ detail()!.name }}</span>
            <span class="badge" style="vertical-align:middle">SKILL.md</span>
          </h2>
          <div class="muted" style="margin-top:6px">{{ detail()!.description }}</div>
          <div class="muted small" style="margin-top:4px">
            {{ detail()!.size }} B · 更新于 {{ detail()!.updated }}
            <span *ngIf="detail()!.has_scripts"> · scripts: {{ (detail()!.scripts || []).join(', ') }}</span>
          </div>
        </div>
        <div style="flex:0 0 auto;display:flex;gap:8px">
          <button (click)="back()">← 返回列表</button>
          <button class="primary" (click)="copyRaw()">{{ copied() ? '已复制 ✓' : '复制 SKILL.md' }}</button>
        </div>
      </div>
      <pre class="skill-md">{{ detail()!.raw }}</pre>
    </div>

    <div class="card" *ngIf="!detail()">
      <h2>技能（{{ skills().length }}）</h2>
      <div class="skill-grid" *ngIf="skills().length; else none">
        <div class="skill-card" *ngFor="let s of skills()" (click)="open(s)">
          <div class="skill-name">
            <span class="mono">{{ s.name }}</span>
            <span class="badge ok" *ngIf="s.has_scripts" title="含脚本">scripts</span>
            <span class="badge" *ngIf="!s.has_scripts">纯指令</span>
          </div>
          <div class="muted skill-desc">{{ s.description || '（无描述）' }}</div>
          <div class="skill-foot muted small" *ngIf="s.has_scripts">
            {{ (s.scripts || []).join(', ') }}
          </div>
        </div>
      </div>
      <ng-template #none><div class="empty">技能库为空或未配置 skills_dir</div></ng-template>
    </div>
  `,
  styles: [`
    .skill-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
    .skill-card {
      border: 1px solid var(--border); border-radius: 12px; padding: 12px 14px;
      cursor: pointer; transition: border-color .15s, box-shadow .15s;
    }
    .skill-card:hover { border-color: var(--primary); box-shadow: 0 4px 14px -6px rgba(15,23,42,.15); }
    .skill-name { display: flex; align-items: center; gap: 8px; font-size: 15px; font-weight: 600; }
    .skill-desc { margin-top: 6px; font-size: 13px; line-height: 1.5; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
    .skill-foot { margin-top: 8px; font-size: 11px; color: var(--muted); }
    .skill-md {
      margin-top: 14px; background: #0d1117; color: #e6edf3; border-radius: 10px;
      padding: 14px 16px; font-size: 12.5px; line-height: 1.6; overflow-x: auto;
      white-space: pre-wrap; word-break: break-word; max-height: 70vh; overflow-y: auto;
    }
  `],
})
export class SkillsComponent implements OnInit {
  readonly skills = signal<SkillSummary[]>([]);
  readonly dir = signal('');
  readonly detail = signal<SkillDetail | null>(null);
  readonly error = signal('');
  readonly copied = signal(false);

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.api.listSkills().subscribe({
      next: (r) => { this.skills.set(r.skills ?? []); this.dir.set(r.dir || ''); },
      error: (e: Error) => this.error.set(e.message),
    });
  }

  open(s: SkillSummary): void {
    this.api.getSkill(s.name).subscribe({
      next: (d) => this.detail.set(d),
      error: (e: Error) => this.error.set(e.message),
    });
  }

  back(): void {
    this.detail.set(null);
  }

  copyRaw(): void {
    const raw = this.detail()?.raw ?? '';
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(raw).then(
        () => { this.copied.set(true); setTimeout(() => this.copied.set(false), 2000); },
        () => {},
      );
    }
  }
}
