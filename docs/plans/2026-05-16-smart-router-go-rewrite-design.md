---
title: Smart Router Go Rewrite Design
date: 2026-05-16
author: Claude Code (User + AI Collaboration)
status: Approved
---

# Smart Router Go Rewrite

## Overview

Clone the existing TypeScript/Cloudflare Workers Smart Router project and revamp it in Go for self-hosted deployment. Remove the Cloudflare Workers dependency (wrangler, Durable Objects, D1) and replace with a standalone Go HTTP server using SQLite + BadgerDB for persistence.

## Goals

- Single binary, zero external service dependencies
- Accept network traffic from other machines on the same LAN (bind 0.0.0.0)
- Preserve all existing functionality: multi-provider routing, circuit breaker, format translation, streaming SSE, usage stats
- Lower memory footprint and better performance than workerd
- Easier local development and debugging

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    HTTP Server (net/http)                │
│                   http://0.0.0.0:8790                    │
└──────────────┬──────────────────────┬───────────────────┘
               │                      │
     ┌─────────▼──────┐      ┌─────────▼────────┐
     │  Router Logic  │      │   Health Tracker  │
     │  (provider LB) │      │  (circuit breaker) │
     └─────────┬──────┘      └────────┬─────────┘
               │                      │
     ┌─────────▼────────┐      ┌───────▼─────────┐
     │  Format Trans.   │      │  BadgerDB (KV)   │
     │  (OpenAI↔Ant)    │      │  health_state    │
     └─────────┬────────┘      └─────────────────┘
               │
     ┌─────────▼────────┐
     │  SQLite (stats)   │
     └───────────────────┘
```

**Key design decisions:**
- Standard library `net/http` with `gorilla/mux` for routing
- Go's goroutines for concurrent connections
- BadgerDB (pure Go) for health state persistence (replaces Cloudflare Durable Objects)
- SQLite (`modernc.org/sqlite`) for stats and plan storage (replaces Cloudflare D1)
- Standard `net/http` client for upstream calls with streaming support
- AES-GCM-256 for API key encryption (same as current)

## Technology Stack

| Component | Technology | Replaces |
|---|---|---|
| HTTP Server | net/http + gorilla/mux | Cloudflare Workers fetch handler |
| Async Runtime | Go goroutines | workerd isolate |
| HTTP Client | net/http (stdlib) | native fetch |
| Health State | BadgerDB | Durable Object |
| Stats DB | SQLite (modernc.org/sqlite) | D1 |
| Config | YAML seed file | plans.json |
| Encryption | AES-GCM (crypto/aes) | Web Crypto API |
| SSE Parsing | bufio.Scanner + custom | native ReadableStream |
| Testing | go test + httptest | vitest |

## Data Models

### SQLite Schema (stats + plans)

```sql
CREATE TABLE request_stats (
    id INTEGER PRIMARY KEY,
    plan TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    key_mask TEXT,
    request_tokens INTEGER NOT NULL DEFAULT 0,
    response_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    latency_ms INTEGER NOT NULL,
    is_streaming INTEGER NOT NULL DEFAULT 0,
    target_provider TEXT,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now') * 1000)
);

CREATE INDEX idx_stats_plan ON request_stats(plan);
CREATE INDEX idx_stats_provider ON request_stats(provider);
CREATE INDEX idx_stats_created ON request_stats(created_at);

CREATE TABLE plans (
    slug TEXT PRIMARY KEY,
    config TEXT NOT NULL
);
```

### BadgerDB Key-Value (health state)

```
health:<provider_name>  →  ProviderHealth JSON
alert:<plan_name>       →  last_alert_timestamp (int64)
```

### Types (Go)

```go
type ProviderConfig struct {
    Name              string  `json:"name"`
    BaseURL           string  `json:"base_url"`
    Model             string  `json:"model"`
    Format            string  `json:"format"` // "openai" | "anthropic"
    Timeout           int     `json:"timeout"`
    APIKey            string  `json:"api_key,omitempty"`
    MaskedKey         string  `json:"masked_key,omitempty"`
    WeeklyTokenLimit  *uint64 `json:"weekly_token_limit,omitempty"`
    WeeklyReqLimit    *uint64 `json:"weekly_request_limit,omitempty"`
    ContextLength     *int    `json:"context_length,omitempty"`
    MaxOutputTokens   *int    `json:"max_output_tokens,omitempty"`
}

type ProviderHealth struct {
    Status              string `json:"status"`
    ConsecutiveFailures int    `json:"consecutiveFailures"`
    LastFailureAt       int64  `json:"lastFailureAt"`
    CooldownUntil       int64  `json:"cooldownUntil"`
    LastFailureReason   string `json:"lastFailureReason"`
    LastSuccessAt       int64  `json:"lastSuccessAt"`
    TotalRequests       int64  `json:"totalRequests"`
    SuccessCount        int64  `json:"successCount"`
    LastActivityAt      int64  `json:"lastActivityAt"`
}

type StatRecord struct {
    Plan           string `json:"plan"`
    Provider       string `json:"provider"`
    Model          string `json:"model"`
    KeyMask        string `json:"key_mask,omitempty"`
    RequestTokens  int    `json:"request_tokens"`
    ResponseTokens int    `json:"response_tokens"`
    TotalTokens    int    `json:"total_tokens"`
    Status         string `json:"status"`
    LatencyMs      int64  `json:"latency_ms"`
    IsStreaming    bool   `json:"is_streaming"`
    TargetProvider string `json:"target_provider,omitempty"`
}
```

## Routing & Request Flow

```
1. Client POST /v1/chat/completions
   Headers: X-Plan: jason | X-Admin-Key: (optional)
   Body: OpenAI/Anthropic compatible JSON

2. Router extracts plan & model
   model="auto-jason" -> plan="jason", rewrite model="auto"
   model="auto" -> plan="default"

3. Get healthy providers from HealthTracker
   Filter: cooldownUntil < now, status != "unhealthy"
   Sort: priority order (config defined)

4. Try providers in order:
   Translate request -> provider format
   Call upstream with timeout
   On success: translate response -> client format
   Record stats (SQLite)
   On failure: report to HealthTracker (BadgerDB), try next

5. If all fail: return 503 with details
```

### Circuit Breaker Rules

| Failure type | Threshold | Cooldown |
|---|---|---|
| auth | 1 | 1 hour |
| quota | 1 | 5 hours |
| rate_limit | 3 | 5 min |
| server_error | 2 | 2 min |
| connection | 2 | 1 min |
| timeout | 2 | 2 min |
| unknown | 3 | 1 min |

## Translation Layer

### Request Translation

OpenAI and Anthropic formats are nearly identical for chat completions. Key differences:
- Anthropic requires `anthropic-version` header
- Anthropic uses `max_tokens` as required field
- System message placement differs

### Streaming SSE Conversion

OpenAI SSE: `data: {"choices":[{"delta":{"content":"hello"}}]}`
Anthropic SSE: `data: {"type":"content_block_delta","delta":{"text":"hello"}}`

Approach: Use `bufio.Scanner` with custom split function to parse SSE lines. Convert in-flight without buffering.

```go
func translateStream(reader io.Reader, from, to string) io.Reader {
    // Parse SSE, transform events, re-emit
    // Use io.Pipe for streaming without buffering
}
```

## API Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /v1/chat/completions | Plan key | Main chat endpoint |
| GET | /v1/plans | - | List all plans (masked keys) |
| GET | /v1/plans/:slug | Admin | Single plan config |
| PUT | /v1/plans/:slug | Admin | Update plan config |
| DELETE | /v1/plans/:slug | Admin | Delete plan |
| GET | /v1/health | - | Combined health |
| GET | /v1/health?plan=:slug | - | Health for plan |
| GET | /v1/health/activity | - | Activity feed |
| GET | /v1/stats | - | Raw stats |
| GET | /v1/stats/aggregated | - | Aggregated stats |
| POST | /v1/stats/clear | Admin | Clear stats |

## Configuration

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| SMART_ROUTER_PORT | 8790 | HTTP server port |
| SMART_ROUTER_HOST | 0.0.0.0 | Bind address |
| MASTER_KEY | - | AES-GCM master key (base64) |
| ADMIN_KEY | - | Admin API key |
| DATABASE_PATH | ./data/smart-router.db | SQLite path |
| BADGER_PATH | ./data/health | BadgerDB path |
| CONFIG_PATH | ./config/plans.yaml | Plans seed file |
| LOG_LEVEL | info | Log level |

### Plans YAML

```yaml
plans:
  jason:
    providers:
      - name: jason-kimi-2
        base_url: https://api.kimi.com/coding/
        model: k2p6
        format: anthropic
        timeout: 60
```

## Project Structure

```
smart-router-go/
├── go.mod
├── go.sum
├── main.go
├── .env
├── config/
│   └── plans.yaml
├── internal/
│   ├── router/
│   │   └── router.go       # Core routing + failover
│   ├── health/
│   │   └── health.go       # Circuit breaker + BadgerDB
│   ├── translation/
│   │   ├── translate.go    # Translation interface
│   │   ├── openai.go       # OpenAI format
│   │   ├── anthropic.go    # Anthropic format
│   │   └── streaming.go    # SSE stream conversion
│   ├── providers/
│   │   └── client.go       # HTTP client
│   ├── db/
│   │   └── db.go           # SQLite schema + stats
│   ├── crypto/
│   │   └── crypto.go       # AES-GCM encryption
│   ├── config/
│   │   └── config.go       # Config loading
│   ├── stats/
│   │   └── stats.go        # Stats recording
│   ├── alerts/
│   │   └── alerts.go       # Outage notifications
│   ├── modelregistry/
│   │   └── registry.go     # Model metadata
│   └── types/
│       └── types.go        # Shared types
├── cmd/
│   └── decrypt_keys/
│       └── main.go         # Key decryption utility
└── scripts/
    └── decrypt_keys.go
```

## go.mod Dependencies

```
require (
    github.com/gorilla/mux v1.8.1
    github.com/dgraph-io/badger/v4 v4.2.0
    modernc.org/sqlite v1.29.0
    gopkg.in/yaml.v3 v3.0.1
    github.com/joho/godotenv v1.5.1
)
```

## Migration Path

1. Bootstrap Go project with net/http + SQLite + BadgerDB
2. Implement core routing + translation (no streaming)
3. Add streaming SSE support
4. Add circuit breaker + health tracking
5. Add stats + aggregation queries
6. Add alerts + admin API
7. Port existing plan configs, test against upstream providers
8. Replace old workerd process with Go binary

## Notes

- YAGNI: No need for multi-instance clustering initially
- Key insight: Streaming SSE translation uses io.Pipe for zero-copy streaming
- Performance target: Handle 1000+ concurrent connections on a single core
- Backwards compatible: Same API surface, existing Hermes config works unchanged
