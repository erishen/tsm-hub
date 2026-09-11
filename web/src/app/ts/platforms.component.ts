import { Component, OnInit, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { ApiService } from './api.service';
import { UpstreamPlatform } from './models';

@Component({
  selector: 'app-platforms',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: '../html/platforms.component.html',
  styleUrls: ['../css/platforms.component.css'],
})
export class PlatformsComponent implements OnInit {
  loading = signal(true);
  error = signal<string | null>(null);
  platforms = signal<UpstreamPlatform[]>([]);
  filter = signal<string>('all');
  search = signal('');

  readonly protocolLabels: Record<string, string> = {
    openai: 'OpenAI',
    anthropic: 'Anthropic',
    azure: 'Azure',
    gemini: 'Gemini',
    bedrock: 'AWS Bedrock',
    sagemaker: 'AWS SageMaker',
    cohere: 'Cohere',
    mistral: 'Mistral',
    huggingface: 'HuggingFace',
    replicate: 'Replicate',
    together: 'Together',
    fireworks: 'Fireworks',
    groq: 'Groq',
  };

  constructor(private api: ApiService, private router: Router) {}

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.loading.set(true);
    this.error.set(null);
    this.api.listUpstreamPlatforms().subscribe({
      next: (r) => {
        this.platforms.set(r.platforms || []);
        this.loading.set(false);
      },
      error: (e: Error) => {
        this.error.set(e.message);
        this.loading.set(false);
      },
    });
  }

  filteredPlatforms(): UpstreamPlatform[] {
    let list = this.platforms();
    if (this.filter() !== 'all') {
      list = list.filter((p) => p.protocol === this.filter());
    }
    const q = this.search().toLowerCase().trim();
    if (q) {
      list = list.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          p.description.toLowerCase().includes(q) ||
          p.models.some((m) => m.toLowerCase().includes(q)) ||
          p.features.some((f) => f.toLowerCase().includes(q))
      );
    }
    return list;
  }

  protocols(): string[] {
    const set = new Set(this.platforms().map((p) => p.protocol));
    return Array.from(set);
  }

  goConfigure(p: UpstreamPlatform): void {
    this.router.navigate(['/providers'], {
      queryParams: { new: 'true', protocol: p.protocol, base_url: p.base_url },
    });
  }

  trackById(_: number, p: UpstreamPlatform): string {
    return p.id;
  }
}
