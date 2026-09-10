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
          <ng-container *ngFor="let group of navGroups">
            <div class="nav-group" [class.open]="!collapsed[group.name]">
              <div class="nav-group-head" (click)="toggleGroup(group.name)">
                <span>{{ group.name }}</span><span class="arrow">▶</span>
              </div>
              <ng-container *ngIf="!collapsed[group.name]">
                <a *ngFor="let item of group.items" [routerLink]="item.route"
                   routerLinkActive="active">{{ item.label }}</a>
              </ng-container>
            </div>
          </ng-container>
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

  navGroups = [
    {
      name: '网关',
      items: [
        { label: 'Providers', route: '/providers' },
        { label: '路由表', route: '/routes' },
        { label: 'Token Keys', route: '/keys' },
      ],
    },
    {
      name: '模型与额度',
      items: [
        { label: '模型', route: '/models' },
        { label: '额度', route: '/balances' },
      ],
    },
    {
      name: '能力池',
      items: [
        { label: '技能库', route: '/skills' },
        { label: 'MCP', route: '/mcps' },
        { label: '工具', route: '/tools' },
        { label: '记忆', route: '/memory' },
        { label: '沙箱', route: '/sandbox' },
        { label: '快路径', route: '/fastpath' },
      ],
    },
    {
      name: '观测',
      items: [
        { label: '用量', route: '/usage' },
        { label: '监控', route: '/observability' },
      ],
    },
    {
      name: '调试',
      items: [{ label: '测试', route: '/playground' }],
    },
  ];

  collapsed: Record<string, boolean> = {};

  toggleGroup(name: string): void {
    this.collapsed[name] = !this.collapsed[name];
  }

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
