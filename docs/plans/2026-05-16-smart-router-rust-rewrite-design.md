---
title: Smart Router Rust Rewrite Design
date: 2026-05-16
author: Claude Code (User + AI Collaboration)
status: Draft
---

# Smart Router Rust Rewrite

## Overview

Clone the existing TypeScript/Cloudflare Workers Smart Router project and revamp it in Rust for self-hosted deployment. Remove the Cloudflare Workers dependency (wrangler, Durable Objects, D1) and replace with a standalone Axum HTTP server using SQLite + Sled for persistence.

## Goals

- Single binary, zero external service dependencies
- Accept network traffic from other machines on the same LAN
- Preserve all existing functionality: multi-provider routing, circuit breaker, format translation, streaming SSE, usage stats
- Lower memory footprint and better performance than workerd
- Easier local development and debugging

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    HTTP Server (Axum)                    │
│                   http://0.0.0.0:8790                    │
└──────────────┬──────────────────────┬───────────────────┘
               │                      │
     ┌─────────▼──────┐      ┌─────────▼────────┐
     │  Router Logic  │      │   Health Tracker  │
     │  (provider LB) │      │  (circuit breaker) │
     └─────────┬──────┘      └────────┬─────────┘
               │                      │
     ┌─────────▼────────┐      ┌───────▼─────────┐
     │  Format Trans.   │      │  Sled (KV store) │
     │  (OpenAI↔Ant)    │      │  health_state    │
     └─────────┬────────┘      └─────────────────┘
               │
     ┌─────────▼────────┐
     │  SQLite (stats)   │
     └───────────────────┘
```

**Key design decisions:**
- Axum for HTTP API with built-in routing and middleware
- Tokio async runtime for I/O-bound concurrent connections
- Sled for health state persistence (replaces Cloudflare Durable Objects)
- SQLite for stats and plan storage (replaces Cloudflare D1)
- `reqwest` for upstream HTTP calls with streaming support
- AES-GCM-256 for API key encryption (same as current)

## Technology Stack

| Component | Technology | Replaces |
|---|---|---|
| HTTP Server | Axum + Tokio | Cloudflare Workers fetch handler |
| Async Runtime | Tokio | workerd isolate |
| HTTP Client | reqwest + hyper | native fetch |
| Health State | Sled | Durable Object |
| Stats DB | SQLite (rusqlite / sqlx) | D1 |
| Config | YAML / JSON seed file | plans.json |
| Encryption | AES-GCM (aes-gcm crate) | Web Crypto API |
| SSE Parsing | custom / eventsource-stream | native ReadableStream |
| Testing | cargo test + wiremock | vitest |

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
    status TEXT NOT NULL, -- 'success', 'failure', 'empty', 'timeout'
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
    config TEXT NOT NULL -- JSON blob of ProviderConfig[]
);
```

### Sled Key-Value (health state)

```
health:<provider_name>  →  ProviderHealth { status, consecutive_failures, last_failure_at, cooldown_until, ... }
alert:<plan_name>       →  last_alert_timestamp
```

### Types (Rust)

```rust
pub struct ProviderConfig {
    pub name: String,
    pub base_url: String,
    pub model: String,
    pub format: ClientFormat, // OpenAI | Anthropic
    pub timeout: u64,
    pub api_key: Option<String>,     // plaintext (in memory only)
    pub masked_key: Option<String>,   // masked for API responses
    pub weekly_token_limit: Option<u64>,
    pub weekly_request_limit: Option<u64>,
    pub context_length: Option<u32>,
    pub max_output_tokens: Option<u32>,
}

pub struct ProviderHealth {
    pub status: HealthStatus,           // healthy | degraded | unhealthy
    pub consecutive_failures: u32,
    pub last_failure_at: u64,           // Unix ms
    pub cooldown_until: u64,            // Unix ms
    pub last_failure_reason: String,    // auth | quota | rate_limit | ...
    pub last_success_at: u64,           // Unix ms
    pub total_requests: u64,
    pub success_count: u64,
    pub last_activity_at: u64,          // Unix ms
}

pub struct StatRecord {
    pub plan: String,
    pub provider: String,
    pub model: String,
    pub key_mask: Option<String>,
    pub request_tokens: u32,
    pub response_tokens: u32,
    pub total_tokens: u32,
    pub status: StatStatus,             // success | failure | empty | timeout
    pub latency_ms: u64,
    pub is_streaming: bool,
    pub target_provider: Option<String>, // for virtual providers
}
```

## Routing & Request Flow

```
1. Client POST /v1/chat/completions
   ├─ Headers: X-Plan: jason | X-Admin-Key: (optional)
   └─ Body: OpenAI/Anthropic compatible JSON

2. Router extracts plan & model
   ├─ model="auto-jason" → plan="jason", rewrite model="auto"
   └─ model="auto" → plan="default"

3. Get healthy providers from HealthTracker
   ├─ Filter: cooldown_until < now, status != "unhealthy"
   └─ Sort: priority order (config defined)

4. Try providers in order:
   ├─ Translate request → provider format (OpenAI/Anthropic)
   ├─ Call upstream with timeout (reqwest)
   ├─ On success: translate response → client format
   ├─ Record stats (SQLite)
   └─ On failure: report to HealthTracker (Sled), try next

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

### Virtual Providers

Virtual providers reference another plan's provider chain via `smart://<plan>` URLs. Max recursion depth: 3.

## Translation Layer

### Request Translation (OpenAI ↔ Anthropic)

| OpenAI | Anthropic |
|---|---|
| `messages[]` | `messages[]` (same structure) |
| `model` | `model` |
| `stream` | `stream` |
| `max_tokens` | `max_tokens` |
| `temperature` | `temperature` |
| `top_p` | `top_p` |

Differences handled:
- Anthropic requires `anthropic-version` header
- Anthropic uses `max_tokens` as required field (OpenAI optional)
- Anthropic `system` message is a top-level string, not in `messages[]`

### Streaming SSE Conversion

**OpenAI SSE format:**
```
data: {"choices":[{"delta":{"content":"hello"}}]}
```

**Anthropic SSE format:**
```
data: {"type":"content_block_delta","delta":{"text":"hello"}}
```

**Approach:** Use `bytes::Bytes` streams with `tokio::sync::mpsc` for backpressure. SSE parsing via custom parser (simpler than full eventsource-stream crate). Convert in-flight without buffering entire response.

```rust
pub async fn translate_stream(
    body_stream: impl Stream<Item = Result<Bytes, reqwest::Error>>,
    from: ClientFormat,
    to: ClientFormat,
) -> impl Stream<Item = Result<Bytes, TranslationError>> {
    // Parse SSE chunks, transform events, re-serialize
}
```

## API Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | /v1/chat/completions | Plan key | Main chat endpoint |
| GET | /v1/plans | — | List all plans (masked keys) |
| GET | /v1/plans/:slug | Admin | Single plan config (plain keys) |
| PUT | /v1/plans/:slug | Admin | Update plan config |
| DELETE | /v1/plans/:slug | Admin | Delete plan |
| GET | /v1/health | — | Combined health across all plans |
| GET | /v1/health?plan=:slug | — | Health for specific plan |
| GET | /v1/health/activity | — | Activity feed per provider |
| GET | /v1/stats | — | Raw stats (paginated) |
| GET | /v1/stats/aggregated | — | Aggregated stats (group_by) |
| POST | /v1/stats/clear | Admin | Clear stats |

## Encryption

Same AES-GCM-256 as current implementation. Master key from environment variable `MASTER_KEY` (base64-encoded 32 bytes).

```rust
use aes_gcm::{Aes256Gcm, Key, Nonce};
use aes_gcm::aead::{Aead, KeyInit};

pub fn encrypt(plaintext: &str, master_key: &[u8; 32]) -> String {
    // Generate random 12-byte IV
    // AES-GCM encrypt
    // Return base64(iv + ciphertext + tag)
}

pub fn decrypt(ciphertext_b64: &str, master_key: &[u8; 32]) -> Result<String, CryptoError> {
    // Decode base64
    // Split iv (12 bytes) | ciphertext | tag (16 bytes)
    // AES-GCM decrypt
}
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `SMART_ROUTER_PORT` | `8790` | HTTP server port |
| `SMART_ROUTER_HOST` | `0.0.0.0` | Bind address (0.0.0.0 for LAN access) |
| `MASTER_KEY` | — | AES-GCM master key (base64) |
| `ADMIN_KEY` | — | Admin API key |
| `DATABASE_PATH` | `./data/smart-router.db` | SQLite database path |
| `SLED_PATH` | `./data/health` | Sled database path |
| `CONFIG_PATH` | `./config/plans.yaml` | Plans seed file |
| `LOG_LEVEL` | `info` | tracing log level |

### Plans YAML (seed format)

```yaml
plans:
  jason:
    providers:
      - name: jason-kimi-2
        base_url: https://api.kimi.com/coding/
        model: k2p6
        format: anthropic
        timeout: 60
      - name: jason-volcengine
        base_url: https://ark.cn-beijing.volces.com/api/coding
        model: kimi-k2.6
        format: anthropic
        timeout: 60
```

## Testing Strategy

### Unit Tests (cargo test)

- `router::tests` — provider selection, fallback ordering, circuit breaker rules
- `translation::tests` — request/response format conversion, SSE parsing
- `health::tests` — cooldown calculation, failure classification, Sled persistence
- `crypto::tests` — AES-GCM encrypt/decrypt roundtrip

### Integration Tests (cargo test --features integration)

- Start full Axum server on random port
- Mock upstream providers via `wiremock` or `mockall`
- Test end-to-end: request → route → mock provider → response

### Load Tests (optional)

- `cargo bench` with `criterion` — measure routing throughput
- `wrk` or `oha` — concurrent request handling

## Project Structure

```
smart-router-rust/
├── Cargo.toml
├── Cargo.lock
├── .env
├── config/
│   ├── plans.yaml              # Seed plans
│   └── default.yaml            # Default config
├── src/
│   ├── main.rs                 # Entry point, Axum server
│   ├── router.rs               # Core routing + failover
│   ├── health.rs               # Circuit breaker + Sled
│   ├── translation/
│   │   ├── mod.rs              # Translation interface
│   │   ├── openai.rs           # OpenAI format
│   │   ├── anthropic.rs        # Anthropic format
│   │   └── streaming.rs        # SSE stream conversion
│   ├── providers/
│   │   ├── mod.rs              # Provider interface
│   │   └── client.rs           # HTTP client, timeout, retries
│   ├── db.rs                   # SQLite schema + stats queries
│   ├── crypto.rs               # AES-GCM encryption
│   ├── config.rs               # Config loading
│   ├── stats.rs                # Stats recording
│   ├── alerts.rs               # Outage notifications
│   ├── model_registry.rs       # Model metadata
│   └── types.rs                # Shared types
├── tests/
│   ├── integration_test.rs
│   └── fixtures/
└── scripts/
    └── decrypt_keys.rs         # Key decryption utility
```

## Cargo.toml Dependencies

```toml
[dependencies]
# Async runtime
tokio = { version = "1.35", features = ["full"] }

# HTTP server
axum = { version = "0.7", features = ["macros"] }
tower = "0.4"
tower-http = { version = "0.5", features = ["cors", "trace"] }

# HTTP client
reqwest = { version = "0.11", features = ["json", "stream"] }

# Serialization
serde = { version = "1.0", features = ["derive"] }
serde_json = "1.0"

# Database
rusqlite = { version = "0.30", features = ["bundled", "chrono"] }
sled = "0.34"

# Config
config = "0.14"

# Encryption
aes-gcm = "0.10"
rand = "0.8"
base64 = "0.22"

# Logging/tracing
tracing = "0.1"
tracing-subscriber = { version = "0.3", features = ["env-filter"] }

# CLI
clap = { version = "4.4", features = ["derive"] }

# Utils
anyhow = "1.0"
thiserror = "1.0"
bytes = "1.5"
futures = "0.3"
tokio-stream = "0.1"

[dev-dependencies]
tokio-test = "0.4"
wiremock = "0.6"
criterion = "0.5"
```

## Migration Path

1. **Phase 1:** Bootstrap Rust project with Axum + SQLite + Sled
2. **Phase 2:** Implement core routing + translation (no streaming)
3. **Phase 3:** Add streaming SSE support
4. **Phase 4:** Add circuit breaker + health tracking
5. **Phase 5:** Add stats + aggregation queries
6. **Phase 6:** Add alerts + admin API
7. **Phase 7:** Port existing plan configs, test against upstream providers
8. **Phase 8:** Replace old workerd process with Rust binary

## Notes

- **YAGNI:** No need for multi-instance clustering initially. SQLite + Sled are single-process. If clustering is needed later, swap SQLite for PostgreSQL and Sled for Redis.
- **Key insight:** Streaming SSE translation is the trickiest part. Use `tokio::sync::mpsc` with bounded channels to prevent memory bloat on slow consumers.
- **Performance target:** Handle 1000+ concurrent connections on a single core (Rust's async is much more efficient than workerd isolates).
- **Backwards compatibility:** Keep the same API surface. Existing Hermes config (`http://localhost:8790`) works unchanged.
