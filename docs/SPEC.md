# Smart Router Specification

> Generated: 2026-06-15
> Version: current `main` (`164e06b` + subsequent runtime changes)

---

## 1. Overview

Smart Router is a Go-based LLM API proxy/router that accepts OpenAI and Anthropic API requests and forwards them to one of several configured upstream providers. It supports plan-based routing, provider failover, health tracking, request/response translation, and usage statistics.

### Primary use cases

- Expose a single Anthropic-compatible endpoint to clients (Claude Code, Hermes Agent, custom SDKs) while routing across multiple provider backends.
- Fail over automatically when a provider returns errors or is rate-limited.
- Track per-plan, per-provider, per-key usage.
- Separate model families into plans (e.g., `jason` for 262K-context providers, `jason-1m` for 1M-context providers).

---

## 2. High-level Architecture

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│  Claude Code    │     │  OpenAI SDK     │     │  Anthropic SDK  │
│  /v1/messages   │     │ /v1/chat/comp   │     │ /v1/messages    │
└────────┬────────┘     └────────┬────────┘     └────────┬────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 ▼
                    ┌─────────────────────────┐
                    │   HTTP API (Gorilla Mux)│
                    │  - Auth / rate limiting │
                    │  - Body parsing         │
                    │  - Plan resolution      │
                    └───────────┬─────────────┘
                                ▼
                    ┌─────────────────────────┐
                    │        Router           │
                    │  - load plan            │
                    │  - provider selection   │
                    │  - health check         │
                    │  - format translation   │
                    │  - upstream call        │
                    └───────────┬─────────────┘
                                ▼
                    ┌─────────────────────────┐
                    │   Upstream Providers    │
                    │  Kimi / GLM / MiniMax   │
                    │  Volcengine / OpenRouter│
                    └─────────────────────────┘
```

### Key design decisions

- **No preflight token-size check.** The router sends requests to providers and handles upstream errors.
- **Token-limit 400s are passed through.** When an upstream provider returns `400` with a token-limit message, the response is returned to the client unchanged. The provider is not marked unhealthy.
- **Plan resolution from `model` field.** The plan slug is derived from the request body `"model"` field (`auto-<plan>` or `<plan>/<model>` syntax). The `X-Plan` header has been removed.
- **Format-aware passthrough.** When `clientFormat == provider.Format`, headers and body pass through with only `model` and auth changed.
- **30s in-memory plan cache** in the router; invalidated on plan updates.

---

## 3. Project Layout

| Path | Responsibility |
|------|----------------|
| `main.go` | Service bootstrap, graceful shutdown, plan seeding |
| `internal/api/handlers.go` | HTTP handlers, plan/key management, stat recording |
| `internal/api/auth.go` | API key validation, rate limiting, plan access control |
| `internal/router/router.go` | Plan loading, provider iteration, health checks, failover |
| `internal/providers/client.go` | HTTP client for upstream providers, header forwarding, auth |
| `internal/translation/` | OpenAI ↔ Anthropic request/response/streaming translation |
| `internal/db/db.go` | SQLite persistence: plans, stats, keys, groups, audit |
| `internal/health/health.go` | BadgerDB-backed failure tracking and cooldown logic |
| `internal/alerts/telegram.go` | Telegram bot commands |
| `config/plans.yaml` | Static seed config for `free`/`default` plans |
| `ecosystem.config.js` | PM2 process configuration |
| `scripts/safe-restart.sh` | Atomic build + restart |

---

## 4. Configuration

### Environment variables

| Variable | Purpose | Example |
|----------|---------|---------|
| `SMART_ROUTER_HOST` | Bind host | `0.0.0.0` |
| `SMART_ROUTER_PORT` | Bind port | `8790` |
| `SMART_ROUTER_ADMIN_KEY` | Admin API key | `admin-secret-123` |
| `SMART_ROUTER_DB_PATH` | SQLite DB path | `./data/smart-router.db` |
| `SMART_ROUTER_HEALTH_PATH` | BadgerDB path | `./data/health` |
| `KEY_ENCRYPTION_KEY` | Base64-encoded 32-byte AES key for provider API keys | `...` |
| `CONFIG_PATH` | Plan seed YAML path | `./config/plans.yaml` |
| `DISCORD_WEBHOOK_URL` | Discord outage alerts | optional |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token | optional |
| `TELEGRAM_CHAT_ID` | Telegram chat for alerts | optional |

### Provider config schema (`types.ProviderConfig`)

```json
{
  "name": "jason-kimi",
  "base_url": "https://api.kimi.com/coding",
  "model": "kimi-for-coding",
  "format": "anthropic",
  "timeout": 60,
  "api_key": "sk-...",
  "context_length": 262144,
  "max_output_tokens": 98304,
  "max_concurrency": 30,
  "weekly_token_limit": 0,
  "weekly_request_limit": 0,
  "weight": 1
}
```

| Field | Meaning |
|-------|---------|
| `name` | Provider identifier |
| `base_url` | Upstream API base URL (must NOT end with `/v1`) |
| `model` | Model name sent to upstream |
| `format` | `"openai"` or `"anthropic"` |
| `timeout` | Request timeout in seconds |
| `context_length` | Metadata only; not enforced by router |
| `max_output_tokens` | Metadata only |
| `max_concurrency` | In-flight limit for adaptive scoring |
| `weekly_*_limit` | Optional per-provider weekly caps |

### Plan config schema (`types.PlanConfig`)

```json
{
  "strategy": "adaptive",
  "providers": [ ... ]
}
```

Strategies:
- `adaptive` — score providers by EWMA latency and in-flight ratio; lowest score first
- `round_robin` — rotate by offset
- `weighted_round_robin` — distribute by `weight`
- `lru` — least-recently-used first
- default/`""` — model-matching providers first, then remaining in declared order

---

## 5. API Endpoints

### Chat / Messages

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions |
| `POST` | `/v1/messages` | Anthropic Messages API |
| `POST` | `/v1/messages/count_tokens` | Returns `CountRequestTokens(body)` |

Authentication: `Authorization: Bearer <api-key>`.

Plan resolution from `model`:
- `"auto"` → plan `default`
- `"auto-jason"` → plan `jason`, model rewritten to `"auto"`
- `"auto-kato"` → plan `kato`, model rewritten to `"auto"`
- `"jason-1m/MiniMax-M3"` → plan `jason-1m`, model `MiniMax-M3`

### Models

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/models` | Static model list + live probe of upstream `/v1/models` |
| `GET` | `/v1/models/{id}` | Single model info |

### Plans (admin)

All require `X-Admin-Key`.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/plans` | List plans (masked keys) |
| `GET` | `/v1/plans` | With admin key: plain keys |
| `GET` | `/v1/plans/{slug}` | Single plan |
| `PUT` | `/v1/plans/{slug}` | Update existing plan |
| `DELETE` | `/v1/plans/{slug}` | Delete plan |

Note: `PUT` only updates existing plans; new plans must be seeded or inserted directly.

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/health` | All provider health |
| `GET` | `/v1/health?plan={slug}` | Health for providers in a plan |
| `GET` | `/v1/health/activity` | Recent activity summary |
| `POST` | `/v1/health/reset` | Reset provider health (`X-Admin-Key`) |

### Stats

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/stats` | Recent request records |
| `GET` | `/v1/stats/aggregated` | Aggregated usage |

Query params: `plan`, `provider`, `model`, `limit`, `since`, etc.

### API Keys (admin)

Require `X-Admin-Key`.

| Method | Path | Description |
|--------|------|-------------|
| `GET/POST` | `/v1/keys` | List / create keys |
| `GET/PUT/DELETE` | `/v1/keys/{key}` | Manage single key |
| `GET` | `/v1/keys/{key}/usage` | Monthly/weekly usage |

---

## 6. Routing Behavior

1. Resolve plan slug from `model`.
2. Load plan (cache first, then SQLite).
3. Split providers:
   - First pass: providers whose configured `model` matches the requested model.
   - Second pass: remaining providers, ordered by strategy.
4. For each provider:
   - Skip virtual-provider cycles or max depth exceeded.
   - Skip if `status == unhealthy` and `cooldownUntil > now`.
   - Override request `model` with provider's configured model.
   - Translate request if formats differ.
   - Call upstream (`Call` or `CallStream`).
   - On 2xx: record success, update LRU, return response.
   - On token-limit 400: return upstream response to client (no failure recorded).
   - On other errors: record failure stat, try next provider.
5. If all providers fail: return 503 with combined provider errors.

### Auth header conventions

| Provider base URL | Header |
|-------------------|--------|
| `api.anthropic.com` | `x-api-key: <key>` |
| `api.kimi.com` (coding) | `Authorization: Bearer <key>` + custom `User-Agent` |
| Others | `Authorization: Bearer <key>` |

---

## 7. Health Tracking

Failure categories and circuit-breaker rules:

| Reason | Threshold | Cooldown |
|--------|-----------|----------|
| `auth` | 1 | 1 hour |
| `quota` | 1 | 5 hours |
| `rate_limit` | 3 | 5 minutes |
| `server_error` | 2 | 2 minutes |
| `connection` | 2 | 1 minute |
| `timeout` | 2 | 2 minutes |
| `invalid_request` | 50 | 5 minutes |
| `unknown` | 3 | 1 minute |

Classification (`health.ClassifyFailure`):
- `401` / "authentication" / "unauthorized" → `auth`
- `402` / "quota" / "credit" / "billing" → `quota`
- `429` / "rate limit" → `rate_limit`
- `400` / `422` → `invalid_request`
- `500`–`599` → `server_error`
- "connection" / "refused" → `connection`
- "timeout" / "context deadline exceeded" → `timeout`
- `"context canceled"` → ignored (empty reason)

Token-limit errors (`"exceeded model token limit"`, `"token limit"`, `"context window"`) are **not** recorded as failures and are passed through to the client.

---

## 8. Stats

Every request writes a row to `request_stats` with:

- `plan`, `provider`, `model`
- `key_mask`, `client_key`, `source`, `user_agent`
- `request_tokens`, `response_tokens`, `total_tokens`
- `status`, `status_code`, `latency_ms`, `is_streaming`

`source` is derived from `User-Agent` (e.g., `claude-code`, `hermes`, `curl`).

---

## 9. Telegram Bot Commands

| Command | Description |
|---------|-------------|
| `/plan <slug>` | Show plan strategy, providers, health, weekly usage |
| `/plans` | List all plans with provider counts |
| `/health [provider]` | Provider health summary |
| `/stats [plan] [limit]` | Recent stats |
| `/usage [plan]` | Weekly usage summary |
| `/status` | System status |
| `/top` | Top consumers |
| `/failures` | Recent failures |
| `/providers` | Provider list |
| `/sources` | Per-client breakdown |
| `/reset [provider]` | Reset provider health |
| `/keys` | List API keys |
| `/keyusage <key>` | Key usage |

---

## 10. Operations

### Safe restart

```bash
./scripts/safe-restart.sh              # build + restart
./scripts/safe-restart.sh --reset-all  # also reset unhealthy providers
```

### Reset provider health

```bash
curl -X POST http://localhost:8790/v1/health/reset \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $SMART_ROUTER_ADMIN_KEY" \
  -d '{"provider":"jason-kimi"}'
```

To reset all unhealthy:

```bash
curl -X POST http://localhost:8790/v1/health/reset ... -d '{}'
```

### Graceful shutdown timing

- Go `srv.Shutdown` timeout: **120s**
- PM2 `kill_timeout`: **135s**

---

## 11. Current Runtime Plans (2026-06-15)

### `jason`

Strategy: `adaptive`

| Provider | Model | Context | Format |
|----------|-------|---------|--------|
| `jason-kimi-2` | `kimi-for-coding` | 262,144 | anthropic |
| `jason-kimi` | `kimi-for-coding` | 262,144 | anthropic |
| `jason-kimi-debbie` | `kimi-for-coding` | 262,144 | anthropic |

All providers are 262K-context Kimi.

### `volcengine`

Strategy: ordered failover (`""`)

| Provider | Model | Context | Format |
|----------|-------|---------|--------|
| `jason-volcengine` | `glm-latest` | 262,144* | anthropic |
| `sam-volcengine-k2` | `glm-latest` | 262,144 | anthropic |

Both providers point at Volcano Engine ARK Coding (`https://ark.cn-beijing.volces.com/api/coding`) and use separate API keys. `glm-latest` is a virtual model name that ARK resolves to the latest Z.ai GLM model (currently `glm-5.2`) at request time. The original `jason-1m` and `kato` plans still include their respective volcengine providers with the legacy `ark-code-latest` model.

No load balancing — providers are tried in the order above and `sam-volcengine-k2` is only used when `jason-volcengine` is out of service (auth/quota/5xx/timeout/connection errors push it into cooldown). Client-side 4xx (e.g. the text-only-model image error) does not trigger failover.

\* Volcengine ARK has been observed successfully handling ~490K-token requests, so the effective context is larger than the configured metadata.

### `jason-1m`

Strategy: `adaptive`

| Provider | Model | Context | Format |
|----------|-------|---------|--------|
| `jason-minimax` | `MiniMax-M3` | 1,000,000 | anthropic |
| `jason-volcengine` | `ark-code-latest` | 262,144* | anthropic |

\* Volcengine `ark-code-latest` has been observed successfully handling ~490K-token requests, so the effective context is larger than the configured metadata.

### `kato`

Strategy: `adaptive`

| Provider | Model | Context | Format |
|----------|-------|---------|--------|
| `zaipu` | `glm-5.2` | 1,000,000 | anthropic |
| `kevin-kimi` | `kimi-for-coding` | 262,144 | anthropic |
| `sam-volcengine-k2` | `ark-code-latest` | 262,144 | anthropic |
| `jason-minimax` | `MiniMax-M3` | 1,000,000 | anthropic |

### `default` / `free` / `chat2api` / `compact` / etc.

Static/runtime plans for general OpenAI-compatible routing and virtual-provider delegation. See live `GET /v1/plans` for current config.

---

## 12. Client Configuration Example

For Claude Code to use the `kato` plan:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://192.168.31.235:8790",
    "ANTHROPIC_AUTH_TOKEN": "sr-...",
    "ANTHROPIC_MODEL": "auto-kato"
  }
}
```

For large contexts, use `auto-jason-1m` or `auto-kato`.

---

## 13. Known Behaviors & Caveats

- **No preflight token check.** Oversized requests hit providers and return upstream 400s for token limits.
- **Token-limit 400s are passed through.** Provider stays healthy; client sees the real upstream error.
- **Plan cache TTL is 30s.** Runtime DB edits are visible within 30s unless cache is invalidated by a plan update.
- **Double `/v1` bug.** `base_url` must not end with `/v1`; the client appends `/v1/messages` or `/v1/chat/completions`.
- **Native Anthropic auth.** `api.anthropic.com` uses `x-api-key`; most proxies use `Authorization: Bearer`.
- **Streaming timeout.** `CallStream` uses `context.Background()`; no hard timeout.
- **Stats token counts come from upstream responses**, not from the local tokenizer.

---

## 14. Changelog Snippets

- Removed preflight token-size check; added live `/v1/models` provider probe.
- Token-limit upstream 400s are passed through to client without marking provider unhealthy.
- Split `jason` into `jason` (262K Kimi) and `jason-1m` (MiniMax + Volcengine).
- Updated `kato` GLM provider from `glm-5.1` to `glm-5.2` (1M context).
- Telegram `/plan` output includes plan strategy.
