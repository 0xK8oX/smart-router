# Smart Router

Cloudflare Worker that acts as a smart proxy / circuit breaker for LLM providers.

## Features

- **Multi-format**: Accepts both OpenAI (`/v1/chat/completions`) and Anthropic (`/v1/messages`) requests
- **Smart routing**: Routes to healthy providers based on configurable plans
- **Circuit breaker**: Temporarily disables providers on repeated failures or quota exhaustion
- **Streaming**: Real-time SSE translation between formats
- **Tool calling**: Full bidirectional tool schema + tool_call translation
- **Encrypted key storage**: API keys encrypted with AES-256-GCM in D1
- **Usage stats**: Non-blocking request/response token tracking per plan, provider, and model

## Architecture

```
Client (OpenAI or Anthropic format)
  → Cloudflare Worker
    → HealthTracker Durable Object (circuit breaker state)
    → Try provider #1
      → Translate request to provider format
      → Call upstream
      → Translate response back
    → If fails: report to DO, try provider #2...
    → Record stats to D1 (non-blocking via ctx.waitUntil)
```

## Setup

### 1. Install dependencies

```bash
cd smart-router
npm install
```

### 2. Configure Wrangler

Copy `wrangler.toml` and set your D1 database ID:

```toml
[[d1_databases]]
binding = "DB"
database_name = "smart-router-db"
database_id = "your-actual-database-id"
```

Create the D1 database:

```bash
npx wrangler d1 create smart-router-db
```

### 3. Set secrets

All sensitive values are kept in Wrangler secrets (production) or `.dev.vars` (local dev). Never commit them.

#### Required secrets

| Secret | Purpose |
|--------|---------|
| `KEY_ENCRYPTION_KEY` | 32-byte base64 master key for AES-256-GCM encryption of API keys in D1 |
| `ADMIN_KEY` | Optional. Required to view decrypted keys via `GET /v1/plans` |
| `TELEGRAM_BOT_TOKEN` | Optional. Telegram bot token for outage alerts |
| `TELEGRAM_CHAT_ID` | Optional. Telegram chat ID for outage alerts |

Generate the encryption key once:

```bash
node -e "console.log(Buffer.from(crypto.randomBytes(32)).toString('base64'))"
```

**Production:**
```bash
npx wrangler secret put KEY_ENCRYPTION_KEY
npx wrangler secret put ADMIN_KEY
npx wrangler secret put TELEGRAM_BOT_TOKEN
npx wrangler secret put TELEGRAM_CHAT_ID
```

**Local dev:** create `.dev.vars` in the project root:

```bash
KEY_ENCRYPTION_KEY=your-32-byte-base64-key
ADMIN_KEY=your-admin-key
TELEGRAM_BOT_TOKEN=your-bot-token
TELEGRAM_CHAT_ID=your-chat-id
```

> **Important:** Wrangler `dev` only reads `.dev.vars` — not `.env`. If `KEY_ENCRYPTION_KEY` is missing, API key decryption fails and every provider returns "Missing API key".

### 4. Deploy

```bash
npx wrangler login   # one-time auth
npx wrangler deploy
```

### 5. Manage plans via API

On first deploy, `plans.json` is seeded into D1. After that, manage plans via the API:

```bash
# List plans
curl http://localhost:8790/v1/plans

# Update a plan (keys are auto-encrypted)
curl -X PUT http://localhost:8790/v1/plans/default \
  -H "Content-Type: application/json" \
  -d '{
    "providers": [
      {"name": "kimi", "base_url": "https://api.kimi.com/coding", "model": "k2p6", "format": "anthropic", "timeout": 60, "api_key": "sk-..."}
    ]
  }'
```

### 6. Update Hermes config

Point your model at the Worker:

```yaml
model:
  provider: custom
  base_url: ${SMART_ROUTER_URL}
  api_key: dummy
  model: auto
```

Route to a specific plan via model name:

```yaml
model:
  provider: custom
  base_url: ${SMART_ROUTER_URL}
  api_key: dummy
  model: auto-jason    # routes to "jason" plan
```

Or for auxiliary tasks:

```yaml
compression:
  provider: custom
  base_url: ${SMART_ROUTER_URL}
  api_key: dummy
  model: auto-compact   # routes to "compact" plan
```

## API

See [API.md](API.md) for full endpoint documentation.

## Local Development

### Wrangler dev (direct)

```bash
npx wrangler dev --port 8790
```

### PM2 (auto-restart on crash)

```bash
pm2 start ecosystem.config.cjs
pm2 logs smart-router
```

Make sure `.dev.vars` exists with `KEY_ENCRYPTION_KEY` before starting. Wrangler only reads `.dev.vars`, not `.env`.

### Test request

```bash
curl -X POST http://localhost:8790/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Plan: default" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'
```
