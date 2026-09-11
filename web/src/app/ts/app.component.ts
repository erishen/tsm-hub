import { Component, HostListener, ViewChild, ElementRef } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { ApiService } from './api.service';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [CommonModule, FormsModule, RouterLink, RouterLinkActive, RouterOutlet],
  templateUrl: '../html/app.component.html',
})
export class AppComponent {
  token = '';
  error = '';
  loading = false;
  isDark = false;

  @ViewChild('searchInput') searchInput!: ElementRef<HTMLInputElement>;

  /** 全局键盘快捷键：Ctrl/Cmd+K 或 / 聚焦搜索框；Esc 关闭搜索下拉。 */
  @HostListener('document:keydown', ['$event'])
  handleKeyboard(ev: KeyboardEvent): void {
    const target = ev.target as HTMLElement;
    const isInput = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable;

    // Ctrl/Cmd + K：聚焦搜索框
    if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'k') {
      ev.preventDefault();
      this.searchInput?.nativeElement.focus();
      this.searchInput?.nativeElement.select();
      return;
    }

    // /：聚焦搜索框（仅当不在输入框时）
    if (ev.key === '/' && !isInput && this.api.loggedIn) {
      ev.preventDefault();
      this.searchInput?.nativeElement.focus();
      return;
    }

    // Esc：关闭搜索下拉
    if (ev.key === 'Escape' && this.searchOpen) {
      this.closeSearch();
    }
  }

  constructor(public api: ApiService) {
    // 初始化主题：从 localStorage 读取，默认跟随系统
    const saved = localStorage.getItem('theme');
    if (saved === 'dark' || (!saved && window.matchMedia('(prefers-color-scheme: dark)').matches)) {
      this.isDark = true;
      document.documentElement.setAttribute('data-theme', 'dark');
    }
  }

  /** 切换深色/浅色主题，保存到 localStorage。 */
  toggleTheme(): void {
    this.isDark = !this.isDark;
    if (this.isDark) {
      document.documentElement.setAttribute('data-theme', 'dark');
      localStorage.setItem('theme', 'dark');
    } else {
      document.documentElement.removeAttribute('data-theme');
      localStorage.setItem('theme', 'light');
    }
  }

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
        { label: '应用推荐', route: '/recommendations' },
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
        { label: '审计日志', route: '/audit-logs' },
      ],
    },
    {
      name: '调试',
      items: [{ label: '测试', route: '/playground' }],
    },
  ];

  collapsed: Record<string, boolean> = {};

  // ---- 全局搜索 ----
  searchQuery = '';
  searchOpen = false;
  searching = false;
  searchResults: { label: string; items: { name: string; desc?: string; route: string }[] }[] = [];
  private searchTimer: ReturnType<typeof setTimeout> | null = null;

  onSearch(): void {
    this.searchOpen = true;
    if (this.searchTimer) clearTimeout(this.searchTimer);
    const q = this.searchQuery.trim().toLowerCase();
    if (!q) { this.searchResults = []; this.searching = false; return; }
    this.searching = true;
    this.searchTimer = setTimeout(() => this.doSearch(q), 250);
  }

  /** 并行拉取模型/工具/技能/Provider/路由/Key，本地过滤匹配项，按类型分组展示。 */
  private doSearch(q: string): void {
    Promise.all([
      this.api.modelsCatalog().toPromise().catch(() => null),
      this.api.listTools().toPromise().catch(() => null),
      this.api.listSkills().toPromise().catch(() => null),
      this.api.listProviders().toPromise().catch(() => null),
      this.api.listRoutes().toPromise().catch(() => null),
      this.api.listKeys().toPromise().catch(() => null),
    ]).then(([models, tools, skills, providers, routes, keys]) => {
      const groups: { label: string; items: { name: string; desc?: string; route: string }[] }[] = [];
      const match = (s: string) => s.toLowerCase().includes(q);
      if (models?.models) {
        const items = models.models.filter((m: any) => match(m.id) || match(m.name || '')).slice(0, 8)
          .map((m: any) => ({ name: m.id, desc: m.provider || '', route: '/models' }));
        if (items.length) groups.push({ label: '模型', items });
      }
      if (tools?.tools) {
        const items = tools.tools.filter((t: any) => match(t.name) || match(t.description || '')).slice(0, 8)
          .map((t: any) => ({ name: t.name, desc: (t.description || '').slice(0, 30), route: '/tools' }));
        if (items.length) groups.push({ label: '工具', items });
      }
      if (skills?.skills) {
        const items = skills.skills.filter((s: any) => match(s.name) || match(s.description || '')).slice(0, 8)
          .map((s: any) => ({ name: s.name, desc: (s.description || '').slice(0, 30), route: '/skills' }));
        if (items.length) groups.push({ label: '技能', items });
      }
      if (providers?.providers) {
        const items = providers.providers.filter((p: any) => match(p.id) || match(p.name || '')).slice(0, 8)
          .map((p: any) => ({ name: p.id, desc: p.base_url || '', route: '/providers' }));
        if (items.length) groups.push({ label: 'Provider', items });
      }
      if (routes?.routes) {
        const items = routes.routes.filter((r: any) => match(r.id) || match(r.model || '')).slice(0, 8)
          .map((r: any) => ({ name: r.id || r.model, desc: r.model || '', route: '/routes' }));
        if (items.length) groups.push({ label: '路由', items });
      }
      if (keys?.keys) {
        const items = keys.keys.filter((k: any) => match(k.name) || match(k.id || '')).slice(0, 8)
          .map((k: any) => ({ name: k.name, desc: k.prefix || '', route: '/keys' }));
        if (items.length) groups.push({ label: 'Key', items });
      }
      this.searchResults = groups;
      this.searching = false;
    });
  }

  closeSearch(): void {
    this.searchOpen = false;
    this.searchQuery = '';
    this.searchResults = [];
  }

  toggleGroup(name: string): void {
    this.collapsed[name] = !this.collapsed[name];
  }

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
