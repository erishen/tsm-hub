import { bootstrapApplication } from '@angular/platform-browser';
import { provideHttpClient, withInterceptorsFromDi } from '@angular/common/http';
import { provideRouter, Routes } from '@angular/router';
import { AppComponent } from './app/ts/app.component';

// 全部管理台页面懒加载：首屏只加载 dashboard + 外壳，其余按需分块。
const routes: Routes = [
  { path: '', loadComponent: () => import('./app/ts/dashboard.component').then((m) => m.DashboardComponent) },
  { path: 'providers', loadComponent: () => import('./app/ts/providers.component').then((m) => m.ProvidersComponent) },
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
  { path: 'playground', loadComponent: () => import('./app/ts/playground.component').then((m) => m.PlaygroundComponent) },
  { path: '**', redirectTo: '' },
];

bootstrapApplication(AppComponent, {
  providers: [provideHttpClient(withInterceptorsFromDi()), provideRouter(routes)],
}).catch((err) => console.error(err));
