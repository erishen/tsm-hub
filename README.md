# tsm-hub

> **AI Capability Hub & LLM Gateway** — unify models, tools, skills, MCPs, sandbox, memory with discovery and promotion across 13+ providers.
> Expose only your own `sk-tr-…` keys to clients; internally route OpenAI-compatible requests to multiple configured upstream providers,
> with a built-in capability pool (generic tools + Agent Skills + MCP servers + Docker sandbox + session memory) that every downstream client gets for free.

- **Backend**: Go 1.22, **zero third-party runtime deps** (stdlib + modernc.org/sqlite pure-Go), single binary
- **Frontend**: Angular 19 (standalone + signals), build output `embed`ded into the Go binary
- **Storage**: JSON config (atomic writes) + JSONL usage logs + SQLite (memory/audit logs), no external DB required
- **Protocol**: OpenAI-compatible client API (`/v1/chat/completions`, `/v1/models`, …), with SSE streaming passthrough; **upstream protocol adapter layer** supports 13 protocols (OpenAI/Anthropic/Azure/Gemini/Bedrock/SageMaker/Cohere/Mistral/HuggingFace/Replicate/Together/Fireworks/Groq) + 12 China platforms (all OpenAI-compatible)
- **Capability pool**: built-in generic tools + Agent Skills library + MCP servers + Docker sandbox + session memory
- **Smart routing**: priority failover, weighted distribution, smart cost-aware routing, auto scene routing, deterministic fastpath
- **Security & compliance**: SHA-256 key hashing, audit logs (auto-redacted secrets), automatic usage cleanup, upstream key `env:` references, native TLS/HTTPS, CORS middleware, startup security self-check (config permissions / plaintext key warnings / TLS warnings), [Security Guide](./docs/SECURITY.md), [Privacy Policy](./docs/PRIVACY.md), [DPA Template](./docs/DPA.md)
- **Admin console**: 18 pages (Dashboard/Providers/Upstream Platforms/Routes/Keys/Models/Balances/Usage/Observability/Skills/MCP/Tools/Memory/Sandbox/Fastpath/Recommendations/Playground/Audit Logs), dark mode, global search, PWA

---

## What it solves

| Problem | Solution |
|---------|----------|
| Upstream API keys scattered across clients, one leak means full rotation | Issue only self-made keys to clients; upstream keys live only on the server; revoking one self-made key doesn't affect others |
| Changing models/providers requires updating all client code | Clients only use aliases like `smart` / `cheap`; mappings live in the server-side route table |
| One upstream goes down, entire chain breaks | Priority-based failover + health check auto-removal, cooldown + half-open probing |
| No visibility into who used how many tokens / how much cost | Per-key independent accounting, supports RPM / total tokens / total cost / daily quota |

---

## Quick start

```bash
cd work/golang/tsm-hub

# 0) Pull skills submodule (resolve-skills, used by admin "Skills" page)
git submodule update --init --recursive

# 1) Build (includes mock upstream for local dev)
make build mock

# 2) Run end-to-end smoke (auto-starts mock upstream + gateway + assertions)
make smoke

# 3) For real use: copy config template, fill in your upstream keys
mkdir -p data && cp config.example.json data/config.json
$EDITOR data/config.json          # Must change: settings.admin_token, each provider's api_key

# 4) Start
./bin/tsm-hub -data ./data -addr :9070
```

Open <http://localhost:9070> for the admin console, log in with `admin_token`.

Call it just like OpenAI (only change the key, base_url, and model alias):

```bash
curl http://localhost:9070/v1/chat/completions \
  -H "Authorization: Bearer sk-tr-xxxxxxxx..." \
  -H "Content-Type: application/json" \
  -d '{"model":"smart","messages":[{"role":"user","content":"tell me a joke"}]}'
```

Python SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:9070/v1", api_key="sk-tr-...")
print(client.chat.completions.create(model="smart", messages=[{"role": "user", "content": "hi"}]))
```

---

## Request pipeline

```
Client (sk-tr-…)
   │
   ▼
┌──────────────────────── tsm-hub ────────────────────────┐
│ 1. Auth    sha256(Key) → memory index → enabled/expiry check │
│ 2. Rate limit  RPM sliding window + token/cost/daily quota   │
│ 3. Route    alias → candidate list: priority sort + weight    │
│            filter out cooling providers, keep one half-open slot│
│ 4. Adapt    protocol adapter: OpenAI→Anthropic/Azure/Gemini/…  │
│            request/response/stream format conversion + SigV4 sign│
│ 5. Forward  swap Authorization, rewrite model, inject stream_options │
│ 6. Respond  non-streaming full response; streaming SSE frame-by-frame │
│            response header X-LLM-Router-Provider: <provider_id> │
│ 7. Account  usage → memory aggregation + data/usage/YYYY-MM-DD.jsonl │
└────────────────────────────────────────────────────────────┘
   │                    │                    │
   ▼                    ▼                    ▼
OpenAI            DeepSeek / Tongyi         Anthropic / Gemini
(failover to next candidate on failure; consecutive failures → removal + cooldown + half-open probe)
```

---

## Directory structure

```
tsm-hub/
├── cmd/
│   ├── server/          # Gateway main program
│   └── mockupstream/    # Fake upstream for local dev (no real models)
├── internal/
│   ├── config/          # Startup params (flags + env vars)
│   ├── store/           # Config models, atomic persistence, memory index
│   ├── auth/            # Self-made key issuance/validation, admin sessions
│   ├── router/          # Route selection + health state machine
│   ├── proxy/           # OpenAI-compatible forwarding, SSE passthrough, usage collection, agent tool loop, MCP, memory, sandbox, fastpath
│   ├── quota/           # Rate limiting, usage aggregation, JSONL persistence
│   ├── api/             # HTTP routing (/v1/*, /api/admin/*)
│   ├── audit/           # Audit logs (SQLite storage, auto-redaction, auto-cleanup)
│   ├── skills/          # Agent Skills library loading and injection
│   └── web/             # embed frontend build output
├── web/                 # Angular admin console source
├── scripts/smoke.sh     # End-to-end smoke test
├── config.example.json  # Config template
└── data/                # Runtime data (config.json + usage/*.jsonl + memory.db + audit.db)
```

---

## Configuration

`data/config.json` is auto-generated on first start, see `config.example.json` for structure:

| Field | Description |
|-------|-------------|
| `settings.listen` | Listen address, can be overridden by `-addr` |
| `settings.admin_token` | Admin console password, can be overridden by `-admin-token` / `$LLM_ROUTER_ADMIN_TOKEN` |
| `settings.fail_threshold` / `cooldown_sec` | Remove after N consecutive failures, half-open probe after M seconds cooldown |
| `settings.pricing` | Price per 1K tokens (USD) for cost estimation; matched by "actual upstream model name → alias → default" order |
| `providers[]` | Upstream: `base_url` (with or without `/v1`), `api_key`, `models`, `weight`, `priority` |
| `routes[]` | External model name → candidate list; `strategy`: `failover` (priority-based degradation) / `weighted` (weighted distribution) |
| `keys[]` | Self-made keys, **only sha256 hash stored** |
| `settings.skills_dir` | Agent Skills library directory (supports relative paths); empty = skills disabled |
| `settings.agent.*` | Gateway agent (generic tools + server-side execution loop), see below |
| `settings.mcps` | External MCP servers (stdio or Streamable HTTP), their tools register as `mcp_<server>_<tool>` in the gateway tool pool |
| `settings.audit_retention_days` | Audit log retention days (default 90), auto-cleanup expired entries |
| `settings.usage_retention_days` | Usage log retention days (default 0 = no auto-cleanup), delete expired files on startup |
| `settings.tls.*` | Native TLS/HTTPS: `enabled` / `cert_file` / `key_file`; when enabled the gateway listens directly on HTTPS (no reverse proxy needed). Startup validates cert files and warns if TLS is disabled in production. |
| `settings.cors.*` | CORS middleware: `enabled` / `allowed_origins` / `allowed_methods` / `allowed_headers` / `allow_credentials` / `max_age`; disabled by default (secure default — prevents unauthorized cross-origin calls). Preflight OPTIONS requests are handled automatically. |
| `settings.sandbox.*` | Docker sandbox config (execute_code tool), see below |

**Gateway agent (built-in generic tools)**: When the client request **does not include `tools`**, the gateway automatically appends the built-in tool pool
and executes the `tool_calls` loop server-side, ultimately returning the answer (streaming requests also run the loop internally then replay via SSE):

| Tool | Description |
|------|-------------|
| `get_time` | Server current local time |
| `calc` | Math expression evaluation (arithmetic + parentheses + sqrt/pow/min/max/round/floor/ceil/sin/cos/tan/log/exp) |
| `fetch_url` | Fetch webpage/API text (http/https only, **internal addresses rejected by default** to prevent SSRF) |
| `echo` | Echo text |
| `skill-run` | Load full description of a specified skill from the gateway skill library |
| `remember` / `recall` | Key-isolated key-value memory (SQLite-persisted) |
| `read_file` | Read files within whitelisted root directory (only available if `agent.read_root` is configured) |
| `csv_analyze` | Analyze CSV structure within whitelisted root directory (only available if `agent.read_root` is configured) |
| `execute_code` | Docker sandbox code execution (only available if `settings.sandbox.enabled=true` and docker is installed) |
| `query_exchange_rate` | Real-time exchange rate query (open.er-api.com free API) |
| `system_info` | Server OS/arch/CPU/memory/disk/uptime |

`settings.agent` config:

| Field | Description |
|-------|-------------|
| `disabled` | `true` to disable agent (enabled by default) |
| `max_rounds` | Max tool loop rounds (default 4) |
| `allow_private_url` | `fetch_url` allow internal/loopback addresses (rejected by default) |
| `read_root` | Root directory allowed for `read_file` (empty = tool not available) |
| `memory_file` | `remember` persistence file (empty = memory only) |

Request-level switch: `X-Llm-Router-Agent: off` header can disable agent for this request; when client provides
`tools`, the gateway always respects the client (pure passthrough, no injection, no execution).

**MCP servers (stdio + Streamable HTTP)**: After configuring `settings.mcps`, the gateway connects to the server on startup/first request,
completes MCP handshake and fetches the tool list. Tools are named `mcp_<server>_<tool>` and added to the agent tool pool,
executed server-side by the gateway (lazy connection + auto-reconnect on failure). Common MCPs (filesystem / sequential-thinking /
memory / serena) are pre-installed, run `make install-mcps` once for out-of-the-box use:

```json
"mcps": {
  "fs":     {"command": "./mcp/node_modules/.bin/mcp-server-filesystem", "args": ["/path/to/workspace"]},
  "think":  {"command": "./mcp/node_modules/.bin/mcp-server-sequential-thinking"},
  "memory": {"command": "./mcp/node_modules/.bin/mcp-server-memory"},
  "serena": {"command": "<uv-tool-dir>/serena-agent/bin/serena", "args": ["start-mcp-server"]},
  "remote": {"transport": "http", "url": "http://127.0.0.1:8787/mcp"}
}
```

`make install-mcps` will: ① `npm install` into `mcp/` (filesystem / sequential-thinking / memory /
github / brave-search / playwright); ② `uv tool install` serena (code semantic engine, Python/uv);
③ optionally install playwright browser kernel. serena's `uv-tool-dir` can be found via `uv tool dir`. Transport:
`command` (stdio, local subprocess) or `transport: "http"` + `url` (remote Streamable HTTP endpoint,
response supports application/json and SSE). Tool capability boundaries are determined by the MCP server you configure (e.g. filesystem
can read/write configured directories).

**Docker sandbox (execute_code)**: When `settings.sandbox.enabled=true`, the gateway registers
the `execute_code` tool, executing code in a disposable Docker container (supports python /
javascript / shell / java / go / rust / c / cpp). Sandbox security boundaries: no network
(`--network none`), read-only root filesystem (only /tmp writable), drop all capabilities,
no privilege escalation, memory/CPU/process/file descriptor limits, timeout auto-kill and container cleanup, auto-destroy after execution.

```json
"sandbox": {"enabled": true, "timeout_seconds": 30, "memory_mb": 512, "cpus": 1, "max_output_kb": 100}
```

Tool params: `language` (including aliases like py/js/sh/c++) + `code` (full source) + optional
`timeout`. For tasks requiring network access, use tools like `fetch_url`; no network access inside the sandbox.

**Deterministic fastpath**: Pure-code-solvable problems like arithmetic, current time, date calculation, unit conversion, number statistics,
base conversion, word count, etc. **return directly, zero model calls, zero upstream cost**,
response header `X-Llm-Router-Fastpath: <method>` (provider is `fastpath`):

| Matcher | Trigger example |
|---------|-----------------|
| arithmetic | calculate 2+3 / 12×34 / 23 plus 45 (supports Chinese operators, power, parentheses) |
| statistics | average of … / sum / max-min / sort (number list) |
| unit_convert | 100 Celsius to Fahrenheit / 5 km to miles |
| date_math | what date is tomorrow / date after N days / days between two dates |
| base_convert | 255 in hex / decimal to binary |
| text_stats | how many characters in this text |
| time | what time is it / current time |

**codegen + promotion**: When built-in matchers don't hit, the gateway asks an LLM to generate a JS detector
(`detect(text) -> string|null`), validated and executed in a goja sandbox (no host API, no I/O,
no network, 3s hard timeout, no eval/Function). On hit, it's persisted as a plugin
(`<data>/fastpath_plugins/`), and subsequent similar problems **hit directly with zero model calls**. Admin console
"Fastpath" page can view built-in matchers, test any query, one-click generate detectors, promote plugins to formal
detectors (moved to `<data>/fastpath_promoted/`, hot-reloaded by mtime, no restart needed).

```json
"fastpath": {"enabled": true, "codegen": true, "plugins_dir": ""}
```

**Upstream keys not written to config file**: `api_key` supports reference form like `env:OPENAI_API_KEY`,
put the real key in an environment variable before startup (admin console echoes the reference itself, no secret).

Fallback when route table doesn't match: directly find all enabled providers declaring support for that model, distribute by weight.

**smart cost-aware routing**: When `strategy: "smart"`, candidates are selected by composite scoring of "free bonus + price tier + health +
cooldown", all parameters adjustable (0 = default):

| Field | Default | Description |
|-------|---------|-------------|
| `smart.free_bonus` | 100 | Base bonus for free models |
| `smart.half_open_penalty` | 30 | Penalty for half-open (post-cooldown probing) candidates |
| `smart.throttle_sec` | 60 | Cooldown seconds after upstream 429, provider not selected during this period |
| `smart.unavailable_sec` | 1800 | Mark duration for upstream 404 "model not found" |
| `smart.price_tiers` | `[{0,60},{0.5,40},{2,20},{10,5}]` | Price tier bonus: prompt price ≤ max tier gets score (match high→low) |

**Scene routing (auto brainless call)**: When client `model` is not provided or is `"auto"`, the gateway automatically
classifies by request content and routes to the corresponding scene (`chat` / `reason` / `code` / `fast`), response header
`X-Llm-Router-Scene` returns the hit signal. Admin "Routes" page can configure candidates for each scene separately.
Falls back to `smart` wildcard route when no `auto` scene route exists.

**Key skill injection**: `keys[].inject_skills` controls skill injection into system for that key's requests:
`""` (default, no injection) / `"list"` (skill list, for model to decide when to call) / `"all"` (all
skill full text) / other string (single skill name). Skill source directory specified by `settings.skills_dir`.

**Observability**: Each request writes one log entry (`data/usage/YYYY-MM-DD.jsonl`), in addition to usage it also
records attribution fields:

| Field | Description |
|-------|-------------|
| `scene` | auto routing hit scene (chat/reason/code/fast; empty for non-auto) |
| `fastpath` | fastpath hit marker (`arithmetic` / `unit_convert` / `plugin:xxx`, etc.) |
| `attempt` | Which candidate actually tried (>1 means failover occurred) |

Admin API `GET /api/admin/observability/overview` returns aggregated view: `today/week/month`
overview (request count/error rate/avg latency/cost/failover count), by-provider attribution, by-scene distribution,
daily trend. Admin "Observability" page displays directly. Historical logs (missing old fields) are auto-compatible, no migration needed.

**Audit logs**: All management operations (create/update/delete Providers, Routes, Keys, MCP, Settings, etc.) are automatically recorded,
stored in `data/audit.db` (SQLite), including timestamp, action type, object type, object ID, detail (auto-redacted
`api_key`/`token`/`password`/`secret`), operator, client IP, User-Agent. Default retention 90 days,
configurable via `settings.audit_retention_days`. Admin "Audit Logs" page supports filtering by object type, action type,
object ID, and pagination.

**External discovery & promotion mechanism**: The gateway automatically records `tools`, `skills`, `mcps` declared in client requests,
displayed as external candidates in admin "Tools", "Skills", "MCP" pages. Admins can view usage statistics and selectively "adopt"
as gateway built-in capabilities (tools promoted to built-in tools or fastpath plugins, skills integrated into skill library, MCP integrated into gateway tool pool).
This way best practices from external clients can be **promoted** into gateway generic capabilities, subsequent clients don't need to re-declare.

---

## Client-facing programmable interfaces

Read-only capability discovery interfaces for **client programs**: accessible with any self-made key (`Authorization: Bearer sk-tr-…`),
allowing clients to dynamically discover what models, tools, MCPs, skills the gateway has, no local scanning needed:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/v1/models` | Available models and route aliases (OpenAI-compatible) |
| GET | `/v1/tools` | Gateway tool pool catalog: built-in + conditional + MCP (OpenAI function schema) |
| GET | `/v1/mcps` | Mounted MCP servers: name / transport / connection status / tools (read-only, no sensitive config like env/command) |
| GET | `/v1/skills` | Skill library list; `/v1/skills/<name>` for single skill full text |

```bash
curl http://localhost:9070/v1/tools -H "Authorization: Bearer <your-key>"
curl http://localhost:9070/v1/mcps  -H "Authorization: Bearer <your-key>"
curl http://localhost:9070/v1/skills -H "Authorization: Bearer <your-key>"
```

---

## Admin API

All require `X-Admin-Token: <admin_token>` (or first `POST /api/admin/login` to get `X-Session-Token`).

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/admin/login` | Exchange admin token for session token (5 wrong attempts from same IP locks for 10 minutes) |
| GET | `/api/admin/overview` | Overview: provider/route/key counts, today and cumulative usage |
| GET/POST | `/api/admin/providers` | Provider list / create or update |
| POST | `/api/admin/providers/probe` | Probe upstream model list and balance |
| GET | `/api/admin/providers/balances` | Batch query each provider's balance/quota (with cache) |
| DELETE | `/api/admin/providers/{id}` | Delete (also cleans related candidates in route table) |
| GET/POST | `/api/admin/routes` | Route table list / create or update |
| DELETE | `/api/admin/routes/{model}` | Delete route |
| GET/POST | `/api/admin/keys` | Key list / issue (**response returns plaintext once**) |
| POST | `/api/admin/keys/{id}/toggle` | Enable/disable |
| PATCH | `/api/admin/keys/{id}` | Update key name/model whitelist/quota |
| GET | `/api/admin/keys/{id}/plaintext` | Re-view newly created key plaintext (valid within 2 minutes of creation, shown only once) |
| DELETE | `/api/admin/keys/{id}` | Delete |
| GET | `/api/admin/models/catalog` | Model catalog: all providers' models aggregated, categorized by type, marked free/paid |
| POST | `/api/admin/models/refresh` | Refresh model catalog (re-probe all providers) |
| GET | `/api/admin/models/recommendations` | App recommendations: recommend suitable app scenarios based on current model pool |
| GET | `/api/admin/usage?days=7&limit=50` | Daily / by-model / by-key aggregation + recent logs |
| POST | `/api/admin/usage/clear` | Clear all usage logs |
| GET | `/api/admin/observability/overview` | Observability: overview / by-provider attribution / by-scene distribution / daily trend |
| GET | `/api/admin/health` | Provider real-time health (latency EWMA, failure count, cooldown expiry) |
| GET/POST | `/api/admin/settings` | View / modify global settings (timeout, threshold, price table, audit/usage retention days) |
| GET | `/api/admin/skills` | Skill library list |
| GET | `/api/admin/skills/{name}` | Single skill full text (Markdown) |
| GET/POST | `/api/admin/mcps` | MCP server list / create or update |
| DELETE | `/api/admin/mcps/{name}` | Delete MCP server |
| GET | `/api/admin/tools` | Gateway tool pool catalog (built-in + MCP + conditional tools) |
| POST | `/api/admin/tools/invoke` | One-click test tool execution |
| GET | `/api/admin/fastpath` | Fastpath status: built-in matchers + plugin list |
| POST | `/api/admin/fastpath/{name}/promote` | Promote plugin to formal detector (optional fastpath plugin or Tools) |
| DELETE | `/api/admin/fastpath/{name}` | Delete fastpath plugin |
| POST | `/api/admin/fastpath/generate` | Manually trigger codegen: generate detector for query and validate |
| GET | `/api/admin/external-tools` | External client-used tool candidates (auto-discovered, can be selectively adopted) |
| POST | `/api/admin/external-tools/{name}/adopt` | Adopt external tool as gateway built-in tool |
| DELETE | `/api/admin/external-tools/{name}` | Delete/ignore external tool candidate |
| GET | `/api/admin/external-skills/candidates` | External skill candidate list |
| GET | `/api/admin/external-mcps/candidates` | External MCP candidate list (auto-discovered MCP declared by clients) |
| POST | `/api/admin/external-mcps/{server}/adopt` | Adopt external MCP server |
| DELETE | `/api/admin/external-mcps/{server}` | Ignore/delete external MCP candidate |
| GET | `/api/admin/sandbox/status` | Docker sandbox status (availability, image, config) |
| GET | `/api/admin/memory` | Session memory list (remember/recall data, SQLite-persisted) |
| DELETE | `/api/admin/memory` | Clear all session memory |
| GET | `/api/admin/audit-logs` | Audit log query (supports object/action/object_id filtering + pagination) |

Additional probe endpoints (no auth required):

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe: `status`/`uptime_s`/`requests` + `degraded`/`providers[]` upstream health |
| GET | `/metrics` | Prometheus text format: HTTP status counts, quota rejection counts, upstream health/request/error/latency EWMA |

CLI equivalent for issuing a key:

```bash
curl -X POST http://localhost:9070/api/admin/keys \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"prod","models":["smart"],"quota":{"rpm":60,"max_cost_usd":20}}'
# → {"ok":true,"id":"…","key":"sk-tr-…","warning":"Please save immediately…"}
```

---

## Development

```bash
make build         # Build server
make mock          # Build mock upstream
make test          # Unit tests + e2e (built-in mock, no internet needed)
make vet fmt       # Static check and formatting
make smoke         # End-to-end smoke script
make web-install   # Install frontend deps (pnpm preferred, falls back to npm)
make web-dev       # Angular dev server :4200 (/api proxied to :9070)
make web-build     # Build frontend to internal/web/dist/browser (for embed)
make web-check     # TS syntax self-check without node_modules
```

### Local dev (start frontend + backend together)

```bash
make dev           # First kill residual processes on :9070/:4200, then rebuild and start backend + Angular dev server in background
make dev-logs      # tail -f .dev/*.log
make dev-status    # See who's listening on those ports
make dev-stop      # Stop only, don't kill build, ports and .dev/*.pid cleaned
```

`make dev` will: clean ports → `go build` → start backend (`-data ./data`, logs `.dev/router.log`) → start `pnpm start`
(logs `.dev/web.log`) → poll `/healthz` and `:4200` until ready then print URL; frontend deps auto-install once if missing.
Ports can be changed with `make dev ROUTER_PORT=9080 WEB_PORT=4300`.

> ⚠️ `dev-stop` is **port-based** `kill -9`: if other services are running on :9070/:4200, they'll be killed too,
> use `make dev-status` to confirm before executing.

> On macOS 15, Go binaries need `CGO_ENABLED=1 -ldflags=-linkmode=external`,
> otherwise dyld reports `missing LC_UUID`. Makefile handles this uniformly.

---

## Container deployment

```bash
make web-install && make web-build   # Build admin console first (output will be baked into image)
docker compose up -d --build          # or docker build -t tsm-hub . && docker run ...
```

Only Go is compiled in the image (no need to install Node in-image), frontend output is copied from host `internal/web/dist/browser`;
skipping frontend build still runs, just admin console is a placeholder page. Data persisted via `./data` volume.

## Testing

```bash
make test    # All packages
make cover   # Coverage
```

| Package | Coverage |
|---------|----------|
| `internal/api` | E2E: auth, failover, SSE, rate limiting, key lifecycle, /v1/models, config persistence, provider response header, 4xx no circuit break, upstream pricing, login rate limit, 413, panic fallback, healthz health, env key, /metrics (18 cases) |
| `internal/proxy` | Request body rewrite, `stream_options` injection, SSE usage parsing, URL concatenation (forwarding main chain covered by `internal/api` e2e tests) |
| `internal/router` | Priority sorting, weight preference, latency penalty, health removal and half-open pass |
| `internal/store` | Atomic write, update rollback, route cleanup on provider deletion, Key CRUD |
| `internal/quota` | Aggregation and replay, RPM window, four quota rejections, TopKeys, recent logs |
| `internal/auth` | Key format, Bearer parsing, constant-time comparison, session expiry |

`make smoke` has an additional script-level e2e suite (real process startup + curl assertions).

## Notes

- **Self-made key plaintext appears only once**: after issuance API returns, server only keeps hash; if lost, must re-issue.
- **Upstream keys recommended to use `env:VAR` references**, avoiding secrets on disk; plaintext also supported (simpler deployment).
- **Startup security self-check**: if admin password is still default placeholder, no providers configured, `config.json` is group/other-readable (not 600), plaintext upstream API keys are detected (recommend `env:` references), or TLS is not enabled, WARN logs are printed on startup.
- **Quota alerts**: When key usage reaches 80% of any limit, a warn log is printed (same type only alerted once,
  resets after falling back below line), suitable for connecting to log monitoring for cost control.
- **`data/` directory contains upstream API keys and usage logs**, already in `.gitignore`, don't commit to public repos.
- **Cost is estimated**: calculated from `settings.pricing` price table, streaming scenarios depend on upstream returning usage
  (`stream_options.include_usage` auto-injected); when upstream doesn't return, tokens recorded as 0.
- Upstream 5xx automatically fails over to next candidate; **streaming responses that have already written bytes to client won't retry**
  (otherwise causes content duplication), only logs the error.
- **4xx not counted as provider failure**: only network errors / 5xx / stream disconnection before first frame trigger circuit break,
  avoiding "context too long / content moderation rejection" client-side issues removing healthy upstreams.
- When editing provider in admin console, echoed `api_key` is redacted; backend detects ellipsis and preserves original key,
  won't overwrite real key with redacted string (frontend-backend dual protection).
- Every response carries `X-Request-ID`, logs with same ID can be correlated; panic auto-falls back to 500 with that ID.
- Request body exceeding `settings.max_body_bytes` directly returns 413 (no misleading error after truncation).
- `/healthz` additionally carries `degraded` and `providers[]` fields, can distinguish "gateway down" vs "some upstream removed".

---

## Related documentation

| Document | Description |
|----------|-------------|
| [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | System architecture, request pipeline, component design |
| [docs/SECURITY.md](./docs/SECURITY.md) | Security deployment guide: TLS, CORS, key management, hardening checklist |
| [docs/PRIVACY.md](./docs/PRIVACY.md) | Privacy policy: data collection, usage, storage, data subject rights |
| [docs/DPA.md](./docs/DPA.md) | Data Processing Agreement template (for B2B deployments) |
| [docs/PRIVACY_AUDIT.md](./docs/PRIVACY_AUDIT.md) | Privacy compliance audit report with findings and remediation |
| [docs/TODO.md](./docs/TODO.md) | Roadmap and planned features |
