import { bootstrapApplication } from '@angular/platform-browser';
import { provideHttpClient, withInterceptorsFromDi } from '@angular/common/http';
import { provideRouter, Routes } from '@angular/router';
import { AppComponent } from './app/ts/app.component';

// 全部管理台页面懒加载：首屏只加载 dashboard + 外壳，其余按需分块。
const routes: Routes = [
  { path: '', loadComponent: () => import('./app/ts/dashboard.component').then((m) => m.DashboardComponent) },
  { path: 'providers', loadComponent: () => import('./app/ts/providers.component').then((m) => m.ProvidersComponent) },
  { path: 'platforms', loadComponent: () => import('./app/ts/platforms.component').then((m) => m.PlatformsComponent) },
  { path: 'routes', loadComponent: () => import('./app/ts/routes.component').then((m) => m.RoutesComponent) },
  { path: 'keys', loadComponent: () => import('./app/ts/keys.component').then((m) => m.KeysComponent) },
  { path: 'balances', loadComponent: () => import('./app/ts/balances.component').then((m) => m.BalancesComponent) },
  { path: 'models', loadComponent: () => import('./app/ts/models.component').then((m) => m.ModelsComponent) },
  { path: 'recommendations', loadComponent: () => import('./app/ts/recommendations.component').then((m) => m.RecommendationsComponent) },
  { path: 'usage', loadComponent: () => import('./app/ts/usage.component').then((m) => m.UsageComponent) },
  { path: 'observability', loadComponent: () => import('./app/ts/observability.component').then((m) => m.ObservabilityComponent) },
  { path: 'audit-logs', loadComponent: () => import('./app/ts/audit-logs.component').then((m) => m.AuditLogsComponent) },
  { path: 'skills', loadComponent: () => import('./app/ts/skills.component').then((m) => m.SkillsComponent) },
  { path: 'mcps', loadComponent: () => import('./app/ts/mcps.component').then((m) => m.McpsComponent) },
  { path: 'tools', loadComponent: () => import('./app/ts/tools.component').then((m) => m.ToolsComponent) },
  { path: 'memory', loadComponent: () => import('./app/ts/memory.component').then((m) => m.MemoryComponent) },
  { path: 'sandbox', loadComponent: () => import('./app/ts/sandbox.component').then((m) => m.SandboxComponent) },
  { path: 'fastpath', loadComponent: () => import('./app/ts/fastpath.component').then((m) => m.FastpathComponent) },
  // 注意：页面路由不能用 'a2a' —— 那是 A2A 协议官方端点（POST /a2a，JSON-RPC），
  // 浏览器 GET /a2a 会打到协议 handler 回 405。能力盘页走 /a2a-panel。
  { path: 'a2a-panel', loadComponent: () => import('./app/ts/a2a.component').then((m) => m.A2aComponent) },
  { path: 'playground', loadComponent: () => import('./app/ts/playground.component').then((m) => m.PlaygroundComponent) },
  { path: '**', redirectTo: '' },
];

bootstrapApplication(AppComponent, {
  providers: [provideHttpClient(withInterceptorsFromDi()), provideRouter(routes)],
}).catch((err) => console.error(err));
