import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import { ApiService } from './api.service';

@Component({
  selector: 'app-sandbox',
  standalone: true,
  imports: [CommonModule],
  templateUrl: '../html/sandbox.component.html',
  styleUrls: ['../css/sandbox.component.css'],
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
