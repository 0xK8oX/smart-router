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

A Go-based LLM API router that proxies requests across OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages formats, with plan-based provider failover, native multi-endpoint routing, health tracking, and stats collection.

### Architecture

**Plan-based routing:**
- Plans are stored in SQLite (`internal/db/db.go`). Each plan has an ordered list of providers.
- `internal/router/router.go` loads the plan, iterates providers in order, checks health, resolves the effective endpoint, translates request format if needed, and calls the provider.
- Provider config includes: `name`, `base_url`, `model`, `format` ("openai", "anthropic", or "responses"), `api_key`, `timeout`, and optionally `endpoints` (see Multi-endpoint providers below).

**Format-aware passthrough:**
- When the effective provider format matches `clientFormat`, the router acts as a transparent proxy: only the `model` field and auth headers are changed. All other headers and body fields pass through unchanged.
- When formats differ, full translation happens (`internal/translation/`).
- `routeWithDepth` returns the **effective** provider (resolved format/URL), not the declared one — handlers branch on the format actually used upstream.

**Format-affinity routing:**
- `providerScore` gives a bonus to providers whose effective format matches `clientFormat`, so the router prefers passthrough over translation when both are healthy. Provider ordering is shared by `ResolveProvider` (precheck) and `routeWithDepth` (actual call) via `buildOrderedProviders`, so the two never disagree on order.

**Translation layer (`internal/translation/`):**
- `translate.go` — `TranslateRequest` and `TranslateResponse` between OpenAI and Anthropic formats.
- `anthropic_to_openai.go` — Anthropic Messages API ↔ OpenAI Chat Completions.
- `openai_to_anthropic_stream.go` — SSE streaming translation OpenAI → Anthropic.
- `streaming.go` — SSE streaming translation Anthropic → OpenAI.
- `responses.go` / `responses_stream.go` — Responses API ↔ Chat Completions conversion (used at the `/v1/responses` edge; `Route` itself never sees a `responses ↔ openai` pair).

**Multi-endpoint providers (`endpoints` field):**
- A provider can declare `endpoints: { anthropic: <url>, openai: <url>, responses: <url> }` to expose multiple APIs from one entry. The router resolves `endpoints[clientFormat]` (`resolveEndpoint`) and hits it natively (passthrough, no translation).
- Example (MiniMax, which offers all three): one `jason-minimax` provider with `endpoints: {anthropic: .../anthropic, openai: .../v1, responses: .../v1}`.
- Providers without `endpoints` keep using `base_url`/`format`.
- When a client requests a format no provider in the plan can serve natively (e.g. `responses` with no `responses` endpoint declared), the handler falls back to edge translation (Responses ↔ Chat Completions). The router skips non-matching providers without marking them unhealthy.

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
- Headers are forwarded from the original request only when `clientFormat == eff.Format` (passthrough mode).
- Auth header format varies by provider: `x-api-key` for native Anthropic, `Authorization: Bearer` for others. Kimi coding gets a custom `User-Agent`.
- `buildEndpoint` appends `/v1/messages`, `/v1/responses`, or `/v1/chat/completions` based on format. **Special case:** if the URL already ends in a known API path (`/chat/completions`, `/messages`, `/responses`, `/completions`), it is used verbatim — this lets providers with non-standard paths (e.g. bigmodel's `/api/paas/v4/chat/completions`) declare their exact URL.

### Endpoints

The server (`internal/api/handlers.go`) exposes:
- `POST /v1/chat/completions` — OpenAI Chat Completions format in/out
- `POST /v1/messages` — Anthropic Messages format in/out
- `POST /v1/responses` — OpenAI Responses API (for Codex). If the selected provider declares a `responses` endpoint, the raw body is proxied natively; otherwise it converts Responses ↔ Chat Completions at the edge.
- `GET /v1/models`, `GET /v1/models/{id}` — static model list for Claude Code compatibility
- `GET/PUT/DELETE /v1/plans/{slug}` — plan management (responses always mask API keys, even for admin callers)
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
| `internal/router/router.go` | Plan loading, provider ordering (`buildOrderedProviders`), health checks, endpoint resolution (`resolveEndpoint`), format-affinity scoring |
| `internal/api/handlers.go` | HTTP handlers (`/v1/chat/completions`, `/v1/messages`, `/v1/responses`), token extraction, key masking, stat recording, streaming loops |
| `internal/providers/client.go` | HTTP client for upstream providers, `buildEndpoint` URL construction, header forwarding, auth |
| `internal/translation/translate.go` | Request/response translation Anthropic ↔ OpenAI, Kimi fallback |
| `internal/translation/responses.go` | Responses API ↔ Chat Completions conversion |
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

- **Tests fail after signature changes:** `Route`, `ResolveProvider`, `Call`, `CallStream`, `TranslateRequest`, `providerScore` all take extra params now (notably `providerScore` takes `clientFormat` for the format-affinity bonus). Update test calls.
- **Token counts are 0:** Check that `types.MaskAPIKey` is set in `KeyMask` and that the upstream response actually contains usage. Use `extractUsage` for non-streaming, `extractUsageFromStream` for streaming. Both handle `"responses"` (reads `input_tokens`/`output_tokens`).
- **Streaming timeout:** `CallStream` must use `context.Background()`, not `context.WithTimeout`.
- **Double `/v1` in URL:** Ensure `base_url` / `endpoints` in config does NOT end with `/v1` (unless the URL is a full API path — see `buildEndpoint` above). The client appends `/v1/messages`, `/v1/responses`, or `/v1/chat/completions`. For non-standard paths (bigmodel), give the full URL ending in `/chat/completions` so it's used verbatim.
- **Anthropic `x-api-key` vs `Authorization`:** Native Anthropic (`api.anthropic.com`) uses `x-api-key`. Most proxies use `Authorization: Bearer`.
- **Empty/wrong response after multi-endpoint:** `Route` returns the *effective* provider. Handlers must branch on the returned `provider.Format` (effective), not the declared format — otherwise a passthrough response gets mistranslated.
