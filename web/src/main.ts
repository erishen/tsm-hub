import { bootstrapApplication } from '@angular/platform-browser';
import { provideHttpClient, withInterceptorsFromDi } from '@angular/common/http';
import { provideRouter, Routes } from '@angular/router';
import { AppComponent } from './app/app.component';
import { DashboardComponent } from './app/dashboard.component';
import { ProvidersComponent } from './app/providers.component';
import { RoutesComponent } from './app/routes.component';
import { KeysComponent } from './app/keys.component';
import { UsageComponent } from './app/usage.component';
import { PlaygroundComponent } from './app/playground.component';

const routes: Routes = [
  { path: '', component: DashboardComponent },
  { path: 'providers', component: ProvidersComponent },
  { path: 'routes', component: RoutesComponent },
  { path: 'keys', component: KeysComponent },
  { path: 'usage', component: UsageComponent },
  { path: 'playground', component: PlaygroundComponent },
  { path: '**', redirectTo: '' },
];

bootstrapApplication(AppComponent, {
  providers: [provideHttpClient(withInterceptorsFromDi()), provideRouter(routes)],
}).catch((err) => console.error(err));
