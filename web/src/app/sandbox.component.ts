import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import { ApiService } from './api.service';

@Component({
  selector: 'app-sandbox',
  standalone: true,
  imports: [CommonModule],
  template: `
    <div class="page-head">
      <div>
        <h1>沙箱</h1>
        <div class="sub">execute_code · Docker 一次性容器隔离执行（--rm / --cap-drop ALL / --network none / 只读根文件系统）</div>
      </div>
      <button class="small" (click)="loadSandbox()">刷新状态</button>
    </div>

    <div class="banner error" *ngIf="error()">{{ error() }}</div>

    <div class="card" *ngIf="sandbox(); else loading">
      <h2>状态</h2>
      <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:12px">
        <span class="badge" [class.ok]="sandbox()!.enabled">{{ sandbox()!.enabled ? '已启用' : '未启用' }}</span>
        <span class="badge" [class.ok]="sandbox()!.docker_ok" [class.err]="!sandbox()!.docker_ok">{{ sandbox()!.docker_ok ? 'Docker 可用' : 'Docker 不可用' }}</span>
        <span class="muted small" style="margin-left:auto">超时 {{ sandbox()!.timeout_sec }}s · 内存 {{ sandbox()!.memory_mb }}MB · CPU {{ sandbox()!.cpus }} · 输出上限 {{ sandbox()!.max_output_kb }}KB</span>
      </div>

      <div class="form-section">
        <h3>支持语言</h3>
        <div class="mcp-chips">
          <span class="chip" *ngFor="let l of sandbox()!.languages">{{ l }}</span>
        </div>
      </div>

      <div class="form-section">
        <h3>安全特性</h3>
        <ul class="muted small" style="line-height:1.8;padding-left:18px;margin:0">
          <li>一次性容器，执行完自动销毁（--rm）</li>
          <li>最小权限：--cap-drop ALL + no-new-privileges</li>
          <li>禁用网络：--network none</li>
          <li>只读根文件系统，仅 /tmp 可写</li>
          <li>内存 / CPU / 进程数 / 文件描述符限制，超时自动 kill 并清理</li>
        </ul>
      </div>

      <div class="form-section">
        <h3>一键测试</h3>
        <div class="muted small" style="margin-bottom:8px">
          在「工具池」页找到 <span class="mono">execute_code</span> 点击「测试」，填写 language 与 code 即可真实跑一次沙箱。
        </div>
        <button class="small primary" (click)="goTools()">前往工具池测试</button>
      </div>
    </div>
    <ng-template #loading><div class="card"><div class="empty">加载沙箱状态中…</div></div></ng-template>
  `,
  styles: [`
    .mcp-chips { display:flex; flex-wrap:wrap; gap:4px; }
    .chip { border:1px solid var(--border,#e4e3dd); border-radius:999px; padding:2px 10px; font-size:12px; }
    .form-section { margin-top:14px; }
    .form-section h3 { margin:0 0 6px; font-size:13px; color:#555; }
  `],
})
export class SandboxComponent implements OnInit {
  sandbox = signal<{ enabled: boolean; docker_ok: boolean; timeout_sec: number; memory_mb: number; cpus: number; max_output_kb: number; languages: string[] } | null>(null);
  error = signal('');
  readonly loading = signal(true);

  constructor(private api: ApiService, private router: Router) {}

  ngOnInit(): void {
    this.loadSandbox();
  }

  loadSandbox(): void {
    this.error.set('');
    this.loading.set(true);
    this.api.sandboxStatus().subscribe({
      next: (r) => { this.sandbox.set(r); this.loading.set(false); },
      error: (e: Error) => { this.error.set('加载失败：' + e.message); this.loading.set(false); },
    });
  }

  goTools(): void {
    this.router.navigate(['/tools']);
  }
}
