# llm-router

A self-hosted LLM gateway: issue your own `sk-tr-…` API keys, and let the gateway
route OpenAI-compatible requests to whichever upstream model you configured.

- **Backend**: Go 1.22, **standard library only**, single binary
- **Frontend**: Angular 19 (standalone), built assets embedded into the Go binary
- **Storage**: JSON config (atomic write) + JSONL usage log — no database required
- **Protocol**: OpenAI compatible (`/v1/chat/completions`, `/v1/models`, …) with SSE streaming

## Quick start

```bash
make build mock      # build gateway + a mock upstream for local testing
make smoke           # end-to-end smoke: failover, streaming, auth, usage accounting

mkdir -p data && cp config.example.json data/config.json
$EDITOR data/config.json        # set settings.admin_token and provider api_key
./bin/llm-router -data ./data -addr :9070
```

Then use it like OpenAI:

```bash
curl http://localhost:9070/v1/chat/completions \
  -H "Authorization: Bearer sk-tr-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"smart","messages":[{"role":"user","content":"hello"}]}'
```

Admin console: <http://localhost:9070> (log in with `admin_token`).

## Features

| Feature | Notes |
|---------|-------|
| Self-issued keys | `sk-tr-<48 hex>`, only sha256 stored; shown once at creation |
| Smart routing | alias → candidates, priority failover or weighted split, latency-aware |
| Health check | consecutive-failure eviction, cooldown, half-open probing |
| Quotas | RPM sliding window, total tokens, total USD, daily token cap |
| Usage accounting | per day / model / key aggregation, JSONL append log |
| Streaming | SSE forwarded frame by frame; `stream_options.include_usage` injected |

## Development

```bash
make test          # unit + e2e tests (no network needed)
make vet fmt       # static checks
make web-install   # install Angular deps
make web-dev       # dev server on :4200, /api proxied to :9070
make web-build     # build console into internal/web/dist/browser (embedded)
```

See `ARCHITECTURE.md` for design notes and `TODO.md` for the roadmap.

> On macOS 15 the Go binary must be built with `CGO_ENABLED=1 -ldflags=-linkmode=external`
> and re-signed ad-hoc (`codesign -s - -f`), otherwise dyld kills it (`LC_UUID` / SIGKILL 137).
> The Makefile already does both.
