# CLAUDE.md

Behavioral guidelines + project-specific instructions for smart-router.

## Behavioral Guidelines

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it — don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

## Project: smart-router

A Go-based LLM API router that proxies requests between OpenAI and Anthropic formats, with plan-based provider failover, health tracking, and stats collection.

### Architecture

**Plan-based routing:**
- Plans are stored in SQLite (`internal/db/db.go`). Each plan has an ordered list of providers.
- `internal/router/router.go` loads the plan, iterates providers in order, checks health, translates request format if needed, and calls the provider.
- Provider config includes: `name`, `base_url`, `model`, `format` ("openai" or "anthropic"), `api_key`, `timeout`.

**Format-aware passthrough:**
- When `clientFormat == provider.Format`, the router acts as a transparent proxy: only the `model` field and auth headers are changed. All other headers and body fields pass through unchanged.
- When formats differ, full translation happens (`internal/translation/`).

**Translation layer (`internal/translation/`):**
- `translate.go` — `TranslateRequest` and `TranslateResponse` between OpenAI and Anthropic formats.
- `anthropic_to_openai.go` — Anthropic Messages API ↔ OpenAI Chat Completions.
- `openai_to_anthropic_stream.go` — SSE streaming translation OpenAI → Anthropic.
- `streaming.go` — SSE streaming translation Anthropic → OpenAI.

**Key quirk:** Kimi's Anthropic-format API returns `input_tokens: 0` alongside `prompt_tokens: N`. The translation layer (`translateAnthropicToOpenAI`) falls back to `prompt_tokens`/`completion_tokens` when the Anthropic-style fields are zero.

**Health tracking (`internal/health/`):**
- BadgerDB-backed. Tracks consecutive failures, cooldown periods, last activity.
- A provider is skipped if `status == "unhealthy"` and `cooldownUntil > now`.

**Stats (`internal/db/stats.go` embedded in db.go):**
- SQLite table `request_stats` records every request with: plan, provider, model, key_mask, request/response/total tokens, status, latency, streaming flag.
- Success stats with token counts are recorded by the HTTP handlers after reading the response body.
- Failure stats are recorded by the router.
- `key_mask` is computed from `types.MaskAPIKey(provider.APIKey)`.

**Provider client (`internal/providers/client.go`):**
- `Call` — non-streaming, with timeout context.
- `CallStream` — streaming, no hard timeout (uses `context.Background()`).
- Headers are forwarded from the original request only when `clientFormat == provider.Format` (passthrough mode).
- Auth header format varies by provider: `x-api-key` for native Anthropic, `Authorization: Bearer` for others. Kimi coding gets a custom `User-Agent`.

### Endpoints

The server (`internal/api/handlers.go`) exposes:
- `POST /v1/chat/completions` — OpenAI format in/out
- `POST /v1/messages` — Anthropic format in/out
- `GET /v1/models`, `GET /v1/models/{id}` — static model list for Claude Code compatibility
- `GET/PUT/DELETE /v1/plans/{slug}` — plan management
- `GET /v1/health`, `GET /v1/health/activity` — health checks
- `GET /v1/stats`, `GET /v1/stats/aggregated` — stats queries

Plus a Telegram bot (`internal/alerts/telegram.go`) for `/health`, `/usage`, `/plan`, `/stats`, `/top`, `/status`.

### Testing

Run all tests:
```bash
cd /Volumes/Proj/workspace/smart-router && go test ./...
```

After any handler, router, translation, or provider client changes: rebuild and restart via PM2:
```bash
cd /Volumes/Proj/workspace/smart-router && go build -o smart-router . && pm2 restart smart-router
```

**Critical: never chain `pm2 stop` with fallible commands.** A failed intermediate step (e.g., `go run ./cmd/reset/main.go` missing) leaves the service dead. Use `pm2 restart` or `pm2 reload` instead. For full safety, use `./scripts/safe-restart.sh` which builds first, then restarts atomically.

### Key Files

| File | Responsibility |
|------|----------------|
| `internal/router/router.go` | Plan loading, provider iteration, health checks, format decision |
| `internal/api/handlers.go` | HTTP handlers, token extraction, stat recording, streaming loops |
| `internal/providers/client.go` | HTTP client for upstream providers, header forwarding, auth |
| `internal/translation/translate.go` | Response translation Anthropic ↔ OpenAI, Kimi fallback |
| `internal/db/db.go` | SQLite: plans, stats, usage queries |
| `internal/health/health.go` | BadgerDB: failure tracking, cooldown logic |
| `internal/alerts/telegram.go` | Telegram bot commands |

### Operations

**Safe restart (preserves in-flight requests):**
```bash
./scripts/safe-restart.sh              # build first, then restart
./scripts/safe-restart.sh --reset-all  # also reset unhealthy providers
```
The script builds a new binary atomically. If compilation fails, the running service is untouched.

**Graceful shutdown timing:**
- Go `srv.Shutdown` timeout: **120s** (`main.go`)
- PM2 `kill_timeout`: **135s** (`ecosystem.config.js`)
- Streaming requests can exceed 60s; PM2 must outlast Go's shutdown so `srv.Shutdown` completes before SIGKILL.

**Reset provider health (online, no restart needed):**
```bash
# Specific provider
curl -X POST http://localhost:8790/v1/health/reset \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $SMART_ROUTER_ADMIN_KEY" \
  -d '{"provider":"jason-kimi"}'

# All unhealthy providers
curl -X POST http://localhost:8790/v1/health/reset \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $SMART_ROUTER_ADMIN_KEY" \
  -d '{}'
```

Or via Telegram: `/reset <provider>`

### Common Pitfalls

- **Tests fail after signature changes:** `Route`, `Call`, `CallStream`, `TranslateRequest` all take extra params now. Update test calls.
- **Token counts are 0:** Check that `types.MaskAPIKey` is set in `KeyMask` and that the upstream response actually contains usage. Use `extractUsage` for non-streaming, `extractUsageFromStream` for streaming.
- **Streaming timeout:** `CallStream` must use `context.Background()`, not `context.WithTimeout`.
- **Double `/v1` in URL:** Ensure `base_url` in config does NOT end with `/v1`. The client appends `/v1/messages` or `/v1/chat/completions`.
- **Anthropic `x-api-key` vs `Authorization`:** Native Anthropic (`api.anthropic.com`) uses `x-api-key`. Most proxies use `Authorization: Bearer`.
