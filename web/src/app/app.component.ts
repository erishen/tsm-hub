import { Component } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { ApiService } from './api.service';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [CommonModule, FormsModule, RouterLink, RouterLinkActive, RouterOutlet],
  template: `
    <div class="login-wrap" *ngIf="!api.loggedIn">
      <div class="card">
        <h2>LLM Token Router</h2>
        <p class="muted">请输入管理口令（data/config.json 的 settings.admin_token）</p>
        <div class="banner error" *ngIf="error">{{ error }}</div>
        <label for="admin-token">管理口令</label>
        <input id="admin-token" type="password" [(ngModel)]="token" (keyup.enter)="login()"
               placeholder="admin_token" autocomplete="current-password" />
        <div style="margin-top:12px">
          <button class="primary" (click)="login()" [disabled]="!token || loading">
            {{ loading ? '登录中…' : '登录' }}
          </button>
        </div>
      </div>
    </div>

    <div class="app-shell" *ngIf="api.loggedIn">
      <aside class="sidebar">
        <div class="brand">
          LLM Token Router
          <small>自制 Key · 智能路由</small>
        </div>
        <nav class="nav">
          <a routerLink="/" routerLinkActive="active" [routerLinkActiveOptions]="{exact:true}">概览</a>
          <a routerLink="/providers" routerLinkActive="active">Providers</a>
          <a routerLink="/routes" routerLinkActive="active">路由表</a>
          <a routerLink="/keys" routerLinkActive="active">Token Keys</a>
          <a routerLink="/balances" routerLinkActive="active">额度</a>
          <a routerLink="/models" routerLinkActive="active">模型</a>
          <a routerLink="/usage" routerLinkActive="active">用量</a>
          <a routerLink="/observability" routerLinkActive="active">监控</a>
          <a routerLink="/skills" routerLinkActive="active">技能库</a>
          <a routerLink="/mcps" routerLinkActive="active">MCP</a>
          <a routerLink="/tools" routerLinkActive="active">工具</a>
          <a routerLink="/memory" routerLinkActive="active">记忆</a>
          <a routerLink="/sandbox" routerLinkActive="active">沙箱</a>
          <a routerLink="/fastpath" routerLinkActive="active">快路径</a>
          <a routerLink="/playground" routerLinkActive="active">测试</a>
        </nav>
        <div style="margin-top:24px">
          <button class="small" (click)="logout()">退出</button>
        </div>
      </aside>
      <main class="main">
        <router-outlet />
      </main>
    </div>
  `,
})
export class AppComponent {
  token = '';
  error = '';
  loading = false;

  constructor(public api: ApiService) {}

  login(): void {
    if (!this.token) return;
    this.loading = true;
    this.error = '';
    this.api.login(this.token).subscribe({
      next: (res) => {
        this.api.saveSession(res.session_token);
        this.token = '';
        this.loading = false;
      },
      error: (err: Error) => {
        this.error = err.message;
        this.loading = false;
      },
    });
  }

  logout(): void {
    this.api.logout();
  }
}
