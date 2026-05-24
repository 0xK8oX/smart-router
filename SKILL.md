# Smart Router Usage Guide

**Base URL:** `https://smart-router.clawplete.workers.dev`

---

## Authentication

### Admin Key
Use `X-Admin-Key` header for plan and key management.

```bash
ADMIN_KEY="sr-admin-1644de824d4ea5f34eb30e91c54fd978"
```

### Client API Key
Use `Authorization: Bearer <key>` for chat endpoints.

---

## 1. Create an API Key (Admin)

```bash
curl -X POST https://smart-router.clawplete.workers.dev/v1/keys \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $ADMIN_KEY" \
  -d '{
    "name": "my-app",
    "plans": ["default"],
    "models": [],
    "rate_limit_rpm": 60,
    "rate_limit_rpd": 1000,
    "monthly_token_limit": 0,
    "monthly_request_limit": 0
  }'
```

Response:
```json
{"ok": true, "key": "sr-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}
```

**Key fields:**
- `plans` — allowed plan slugs (empty = all plans)
- `models` — allowed model names (empty = all models)
- `rate_limit_rpm` — requests per minute limit (0 = unlimited)
- `rate_limit_rpd` — requests per day limit (0 = unlimited)
- `monthly_token_limit` — monthly token quota (0 = unlimited)
- `monthly_request_limit` — monthly request quota (0 = unlimited)

---

## 2. Create a Plan (Admin)

```bash
curl -X PUT https://smart-router.clawplete.workers.dev/v1/plans/default \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $ADMIN_KEY" \
  -d '{
    "providers": [
      {
        "name": "openai",
        "base_url": "https://api.openai.com/v1",
        "model": "gpt-4o",
        "format": "openai",
        "timeout": 60,
        "api_key": "sk-your-openai-key",
        "weekly_token_limit": 100000,
        "weekly_request_limit": 1000
      },
      {
        "name": "anthropic",
        "base_url": "https://api.anthropic.com",
        "model": "claude-3-5-sonnet-20241022",
        "format": "anthropic",
        "timeout": 60,
        "api_key": "sk-your-anthropic-key"
      }
    ]
  }'
```

Response:
```json
{"ok": true, "slug": "default"}
```

**Provider fields:**
- `name` — provider identifier
- `base_url` — upstream API base URL (no trailing `/v1`)
- `model` — model name to use with this provider
- `format` — `"openai"` or `"anthropic"`
- `api_key` — provider API key (encrypted at rest)
- `timeout` — request timeout in seconds
- `endpoint` — optional explicit URL, bypasses auto-built `base_url + /v1/messages|chat/completions`
- `context_length` — optional max input token override
- `max_output_tokens` — optional max output token override
- `weekly_token_limit` / `weekly_request_limit` — provider-level quotas

---

## 3. Add Provider to Existing Plan (Admin)

Fetch existing plan, modify providers array, then PUT back:

```bash
curl https://smart-router.clawplete.workers.dev/v1/plans/default \
  -H "X-Admin-Key: $ADMIN_KEY"
```

Update:
```bash
curl -X PUT https://smart-router.clawplete.workers.dev/v1/plans/default \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $ADMIN_KEY" \
  -d '{
    "providers": [
      { ...existing providers... },
      {
        "name": "kimi",
        "base_url": "https://api.moonshot.cn",
        "model": "moonshot-v1-8k",
        "format": "openai",
        "timeout": 60,
        "api_key": "sk-your-kimi-key"
      }
    ]
  }'
```

---

## 4. OpenAI Format Request

```bash
API_KEY="sr-your-client-key"

curl https://smart-router.clawplete.workers.dev/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "auto",
    "messages": [
      {"role": "user", "content": "Hello, what can you do?"}
    ],
    "stream": false
  }'
```

**Model selection:**
- `"model": "auto"` — route to first healthy provider in plan
- `"model": "openai"` — route to provider named "openai" specifically
- `"model": "myplan"` — if "myplan" is a plan name, use that plan instead of default

---

## 5. Anthropic Format Request

```bash
curl https://smart-router.clawplete.workers.dev/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "Explain quantum computing"}
    ],
    "stream": false
  }'
```

---

## 6. Streaming Request

```bash
curl https://smart-router.clawplete.workers.dev/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "Count to 10"}],
    "stream": true
  }'
```

---

## 7. List API Keys (Admin)

```bash
curl https://smart-router.clawplete.workers.dev/v1/keys \
  -H "X-Admin-Key: $ADMIN_KEY"
```

---

## 8. Update Key Restrictions (Admin)

```bash
curl -X PUT https://smart-router.clawplete.workers.dev/v1/keys/sr-xxxxxxxx \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $ADMIN_KEY" \
  -d '{
    "plans": ["default", "pro"],
    "models": ["gpt-4o", "claude-3-5-sonnet"],
    "disabled": false
  }'
```

---

## 9. Delete a Key (Admin)

```bash
curl -X DELETE https://smart-router.clawplete.workers.dev/v1/keys/sr-xxxxxxxx \
  -H "X-Admin-Key: $ADMIN_KEY"
```

---

## Health & Status

```bash
# Check system status
curl https://smart-router.clawplete.workers.dev/v1/status

# Check provider health for a plan
curl https://smart-router.clawplete.workers.dev/v1/health?plan=default
```

---

## Plan Redirection (Virtual Providers)

Create a provider that redirects to another plan:

```json
{
  "name": "fallback",
  "base_url": "smart://backup-plan",
  "model": "auto",
  "format": "openai"
}
```

When this provider is reached, the router recursively routes to the `backup-plan` plan.

---

## Key Quotas & Rate Limits

| Limit Type | Behavior |
|---|---|
| RPM | Counts requests in the last 60 seconds |
| RPD | Counts requests in the last 24 hours |
| Monthly tokens | Counts total tokens in current calendar month |
| Monthly requests | Counts requests in current calendar month |

When a limit is exceeded, the API returns `429 Too Many Requests`.
