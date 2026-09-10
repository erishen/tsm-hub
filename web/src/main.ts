import { bootstrapApplication } from '@angular/platform-browser';
import { provideHttpClient, withInterceptorsFromDi } from '@angular/common/http';
import { provideRouter, Routes } from '@angular/router';
import { AppComponent } from './app/app.component';

// 全部管理台页面懒加载：首屏只加载 dashboard + 外壳，其余按需分块。
const routes: Routes = [
  { path: '', loadComponent: () => import('./app/dashboard.component').then((m) => m.DashboardComponent) },
  { path: 'providers', loadComponent: () => import('./app/providers.component').then((m) => m.ProvidersComponent) },
  { path: 'routes', loadComponent: () => import('./app/routes.component').then((m) => m.RoutesComponent) },
  { path: 'keys', loadComponent: () => import('./app/keys.component').then((m) => m.KeysComponent) },
  { path: 'balances', loadComponent: () => import('./app/balances.component').then((m) => m.BalancesComponent) },
  { path: 'models', loadComponent: () => import('./app/models.component').then((m) => m.ModelsComponent) },
  { path: 'usage', loadComponent: () => import('./app/usage.component').then((m) => m.UsageComponent) },
  { path: 'observability', loadComponent: () => import('./app/observability.component').then((m) => m.ObservabilityComponent) },
  { path: 'skills', loadComponent: () => import('./app/skills.component').then((m) => m.SkillsComponent) },
  { path: 'mcps', loadComponent: () => import('./app/mcps.component').then((m) => m.McpsComponent) },
  { path: 'fastpath', loadComponent: () => import('./app/fastpath.component').then((m) => m.FastpathComponent) },
  { path: 'playground', loadComponent: () => import('./app/playground.component').then((m) => m.PlaygroundComponent) },
  { path: '**', redirectTo: '' },
];

bootstrapApplication(AppComponent, {
  providers: [provideHttpClient(withInterceptorsFromDi()), provideRouter(routes)],
}).catch((err) => console.error(err));
