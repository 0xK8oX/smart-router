# Smart Router API Documentation

Base URL: `https://<your-worker>.workers.dev` (local: `http://localhost:8790`)

---

## Chat Completion

### OpenAI Format

```
POST /v1/chat/completions
Content-Type: application/json
X-Plan: <plan-slug>        # optional, defaults to "default"
```

**Request body:**
```json
{
  "model": "auto",
  "messages": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": false,
  "tools": [],
  "tool_choice": "auto"
}
```

**Model behavior:**
- `"model": "auto"` — uses the provider's configured model from the plan
- `"model": "auto-jason"` — routes to the "jason" plan, rewrites to `"auto"`
- `"model": "auto-compact"` — routes to the "compact" plan, rewrites to `"auto"`
- `"model": "glm-5.1"` — overrides with the specified model

**Response (non-streaming):**
```json
{
  "id": "msg_...",
  "object": "chat.completion",
  "model": "glm-5.1",
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "Hi there!"
      }
    }
  ]
}
```

**Response (streaming):**
```
data: {"choices":[{"delta":{"content":"Hi"}}]}
data: {"choices":[{"delta":{"content":" there"}}]}
data: [DONE]
```

### Anthropic Format

```
POST /v1/messages
Content-Type: application/json
X-Plan: <plan-slug>
```

**Request body:**
```json
{
  "model": "auto",
  "messages": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": false,
  "max_tokens": 4096
}
```

**Response:** Anthropic Messages API format (translated from the backend provider).

---

## Plan Management

API keys are stored **per-provider inside the plan** and encrypted with AES-256-GCM. The master encryption key (`KEY_ENCRYPTION_KEY`) stays in Wrangler secrets only.

### List All Plans

```
GET /v1/plans
```

**Response:**
```json
{
  "kato": {
    "providers": [
      {"name": "kato-glm", "base_url": "...", "model": "glm-5.1", "format": "anthropic", "timeout": 60, "masked_key": "4910****dydR"},
      {"name": "kato-kimi", "base_url": "...", "model": "kimi-for-coding", "format": "anthropic", "timeout": 60, "masked_key": "sk-k****Cwwo"}
    ]
  }
}
```

### Admin: List All Plans (plain keys)

```
GET /v1/plans
X-Admin-Key: <admin-secret>
```

**Response:**
```json
{
  "kato": {
    "providers": [
      {"name": "kato-glm", "base_url": "...", "model": "glm-5.1", "format": "anthropic", "timeout": 60, "api_key": "sk-..."},
      {"name": "kato-kimi", "base_url": "...", "model": "kimi-for-coding", "format": "anthropic", "timeout": 60, "api_key": "sk-..."}
    ]
  }
}
```

### Get Single Plan

```
GET /v1/plans/:slug
```

**Example:** `GET /v1/plans/kato`

**Response (200):**
```json
{
  "providers": [
    {"name": "kato-glm", "base_url": "...", "model": "glm-5.1", "format": "anthropic", "timeout": 60, "masked_key": "4910****dydR"},
    {"name": "kato-kimi", "base_url": "...", "model": "kimi-for-coding", "format": "anthropic", "timeout": 60, "masked_key": "sk-k****Cwwo"}
  ]
}
```

**Response (404):**
```json
{"error": "Plan not found"}
```

### Admin: Get Single Plan (plain keys)

```
GET /v1/plans/:slug
X-Admin-Key: <admin-secret>
```

Returns the same shape but with `api_key` (plain text) instead of `masked_key`.

### Replace a Plan

```
PUT /v1/plans/:slug
Content-Type: application/json
```

**Request body:**
```json
{
  "providers": [
    {
      "name": "kato-glm",
      "base_url": "https://open.bigmodel.cn/api/anthropic",
      "model": "glm-5.1",
      "format": "anthropic",
      "timeout": 60,
      "api_key": "sk-..."
    },
    {
      "name": "kato-kimi",
      "base_url": "https://api.kimi.com/coding/",
      "model": "kimi-for-coding",
      "format": "anthropic",
      "timeout": 60,
      "api_key": "sk-..."
    }
  ]
}
```

**Key handling:**
- Send **plain keys** — the server auto-encrypts them before storing
- Send **already-encrypted keys** — the server detects this and preserves them
- Omit `api_key` — the key is removed from the provider

**Response (200):**
```json
{"ok": true, "slug": "kato"}
```

### Delete a Plan

```
DELETE /v1/plans/:slug
```

**Response (200):**
```json
{"ok": true, "slug": "kato"}
```

---

## Health Status

```
GET /v1/health?plan=<slug>
```

**Example:** `GET /v1/health?plan=kato`

**Response:**
```json
{
  "providers": {
    "kato-glm": {
      "status": "healthy",
      "consecutiveFailures": 0,
      "lastFailureAt": 0,
      "cooldownUntil": 0,
      "lastFailureReason": "",
      "lastSuccessAt": 1777363500000,
      "totalRequests": 1256,
      "successCount": 1256,
      "lastActivityAt": 1777363500000
    },
    "kato-kimi": {
      "status": "unhealthy",
      "consecutiveFailures": 3,
      "lastFailureAt": 1777090000000,
      "cooldownUntil": 1777090600000,
      "lastFailureReason": "rate_limit",
      "lastSuccessAt": 1777089000000,
      "totalRequests": 3486,
      "successCount": 3483,
      "lastActivityAt": 1777090000000
    }
  }
}
```

**Fields:**
- `status` — `healthy` | `degraded` | `unhealthy`
- `consecutiveFailures` — failures since last success
- `lastFailureAt` — Unix ms timestamp of last failure
- `cooldownUntil` — Unix ms timestamp when provider exits cooldown
- `lastFailureReason` — `quota`, `rate_limit`, `server_error`, `connection`, `timeout`, `unknown`
- `lastSuccessAt` — Unix ms timestamp of last successful request
- `totalRequests` — total requests attempted (success + failure)
- `successCount` — total successful requests
- `lastActivityAt` — Unix ms timestamp of last request (success or failure)

---

## Activity Feed

Providers sorted by most recent activity. Use this to see which provider is actively serving traffic right now.

```
GET /v1/health/activity?plan=<slug>
```

**Example:** `GET /v1/health/activity?plan=jason`

**Response:**
```json
{
  "plan": "jason",
  "providers": [
    {
      "name": "jason-volcengine",
      "status": "healthy",
      "lastSuccessAt": 1777363500000,
      "lastFailureAt": 1777345507643,
      "lastActivityAt": 1777363500000,
      "totalRequests": 1258,
      "successCount": 1256,
      "consecutiveFailures": 0,
      "cooldownUntil": 0
    },
    {
      "name": "jason-minimax",
      "status": "degraded",
      "lastSuccessAt": 1777363400000,
      "lastFailureAt": 1777363450000,
      "lastActivityAt": 1777363450000,
      "totalRequests": 917,
      "successCount": 915,
      "consecutiveFailures": 1,
      "cooldownUntil": 0
    }
  ]
}
```

---

## Stats

### Query Raw Stats

```
GET /v1/stats?plan=default&provider=kimi&from=2025-01-01&limit=100
```

**Query params:**
- `plan` — filter by plan slug
- `provider` — filter by provider name
- `model` — filter by model name
- `from` — Unix ms timestamp or ISO date
- `to` — Unix ms timestamp or ISO date
- `limit` — max rows (default 100, max 1000)

**Response:**
```json
[
  {
    "id": 1,
    "plan": "default",
    "provider": "kimi",
    "model": "k2p6",
    "key_mask": "sk-k****Cwwo",
    "request_tokens": 120,
    "response_tokens": 450,
    "total_tokens": 570,
    "status": "success",
    "latency_ms": 3200,
    "is_streaming": 0,
    "created_at": 1777090000000
  }
]
```

### Aggregated Stats

```
GET /v1/stats/aggregated?group_by=provider&from=2025-01-01
```

**Query params:**
- `group_by` — `plan`, `provider`, `model`, or `key_mask` (default: `provider`)
- `from` — Unix ms or ISO date
- `to` — Unix ms or ISO date
- `limit` — max groups (default 100, max 1000)

**Response:**
```json
{
  "group_by": "provider",
  "results": [
    {
      "dimension": "kimi",
      "requests": 42,
      "request_tokens": 12000,
      "response_tokens": 34000,
      "total_tokens": 46000,
      "avg_latency_ms": 3200
    }
  ]
}
```

---

## Error Responses

### Plan Not Found
```json
{"error": "Plan \"xyz\" not found or empty"}
```

### All Providers Failed
```json
{
  "error": "All providers failed",
  "details": [
    {"provider": "kato-glm", "status": 401, "message": "Invalid API key"},
    {"provider": "kato-kimi", "status": 429, "message": "Rate limited"}
  ]
}
```

### All Providers in Cooldown
```json
{"error": "All providers in cooldown"}
```

---

## Managing a Plan (Step-by-Step)

### 1. View the plan (normal)

```bash
curl http://localhost:8790/v1/plans/kato
```

### 2. View the plan with plain keys (admin)

```bash
curl -H "X-Admin-Key: $ADMIN_KEY" http://localhost:8790/v1/plans/kato
```

### 3. Edit and replace the plan

Copy the response, edit keys or providers, then PUT it back:

```bash
curl -X PUT http://localhost:8790/v1/plans/kato \
  -H "Content-Type: application/json" \
  -d '{
    "providers": [
      {"name": "kato-glm", "base_url": "https://open.bigmodel.cn/api/anthropic", "model": "glm-5.1", "format": "anthropic", "timeout": 60, "api_key": "sk-..."},
      {"name": "kato-kimi", "base_url": "https://api.kimi.com/coding/", "model": "kimi-for-coding", "format": "anthropic", "timeout": 60, "api_key": "sk-..."}
    ]
  }'
```

Keys are encrypted server-side before storage. Even if the D1 database leaks, keys remain unusable without the master `KEY_ENCRYPTION_KEY` (stored in Wrangler secrets).

### 4. Test it

```bash
curl -X POST http://localhost:8790/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Plan: kato" \
  -d '{"model":"auto","messages":[{"role":"user","content":"Hello"}]}'
```

---

## Hermes Agent Integration

In `config.yaml`, point the model base_url to the router:

```yaml
model:
  provider: custom
  base_url: ${SMART_ROUTER_URL}
  api_key: dummy
  model: "auto"
```

Route to a specific plan via model name:

```yaml
model:
  provider: custom
  base_url: ${SMART_ROUTER_URL}
  api_key: dummy
  model: "auto-jason"    # routes to "jason" plan
```

Or for auxiliary tasks:

```yaml
compression:
  provider: custom
  base_url: ${SMART_ROUTER_URL}
  api_key: dummy
  model: "auto-compact"   # routes to "compact" plan
```

The router handles:
- Format translation (OpenAI ↔ Anthropic)
- Provider fallback (tries next provider on failure)
- Circuit breaker (skips unhealthy providers)
- Encrypted API key storage (AES-256-GCM in D1)
- Usage tracking (non-blocking stats per plan/provider/model)
- Model override (`auto` = plan default, `auto-<plan>` = route to plan, specific name = pass through)
