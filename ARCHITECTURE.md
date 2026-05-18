# Smart Router Architecture

```mermaid
flowchart TB
    subgraph Clients["Clients"]
        C1[Hermes Agent]
        C2[Claude Code]
        C3[OpenAI SDK]
        C4[Anthropic SDK]
    end

    subgraph API["API Layer (Gorilla Mux)"]
        direction TB
        E1[/v1/chat/completions<br/>OpenAI format/]
        E2[/v1/messages<br/>Anthropic format/]
        E3[/v1/models/]
        E4[/v1/plans/]
        E5[/v1/health/]
        E6[/v1/stats/]
    end

    subgraph Router["Router Core"]
        direction TB
        R1["getPlanCached()<br/>30s TTL in-memory cache"]
        R2["Model matching<br/>requestedModel == provider.Model"]
        R3["Load Balancing Strategy"]
        R4["Health Check<br/>skip if unhealthy + cooldown"]
        R5["Virtual Provider<br/>smart://plan recursive routing"]
        R6["Request Translation<br/>OpenAI ↔ Anthropic"]
        R7["Provider HTTP Call<br/>Call() / CallStream()"]
    end

    subgraph Strategies["Load Balancing Strategies"]
        S1["round_robin<br/>rotate by offset"]
        S2["weighted_round_robin<br/>distribute by weight"]
        S3["lru<br/>least-recently used first"]
    end

    subgraph Data["Data & Observability"]
        D1[(SQLite<br/>plans + stats)]
        D2[(BadgerDB<br/>provider health)]
        D3["Telegram Bot<br/>/plan /health /usage"]
        D4["Discord Alerts<br/>outage notifications"]
    end

    subgraph Upstream["Upstream Providers"]
        P1["Kimi (k2.6)"]
        P2["GLM (glm-5.1)"]
        P3["DeepSeek (V4)"]
        P4["MiniMax (M2.7)"]
        P5["Volcengine (ARK)"]
        P6["Perplexity (PPLX)"]
        P7["Qwen (3.6)"]
    end

    Clients -->|HTTP + X-Plan header| API
    API -->|Route(planSlug, body, stream, clientFormat, headers)| Router

    Router --> R1
    R1 --> R2
    R2 --> R3
    R3 --> R4
    R4 --> R5
    R5 --> R6
    R6 --> R7

    R3 --> Strategies
    R1 -.->|Invalidate on<br/>plan update/delete| D1
    R4 -.->|Record success/failure| D2
    R7 -.->|Record stat| D1

    Router -->|HTTP POST| Upstream
    D1 --> D3
    D2 --> D3
    D1 --> D4
    D2 --> D4
```

## Request Flow

```
1. Client sends POST /v1/chat/completions
   → X-Plan: chat2api (or auto- prefix, or plan/model syntax)

2. API handler parses body
   → auto- prefix: model="auto-chat2api" → plan="chat2api", model="auto"
   → plan/model: model="chat2api/DeepSeek-V4-Pro" → plan="chat2api", model="DeepSeek-V4-Pro"

3. Router.Route(planSlug, body, isStreaming, "openai", headers)
   → Load plan from cache (30s TTL) or SQLite
   → Split providers: model-matching first, rest second
   → Apply strategy (round_robin / weighted / lru) to remaining providers

4. For each provider in order:
   → Virtual? smart://target → recurse into target plan (max depth 3)
   → Healthy? Skip if unhealthy and still in cooldown
   → Translate request body to provider format (OpenAI ↔ Anthropic)
   → Call provider HTTP endpoint
   → Success (2xx): record success, update LRU, return response
   → Failure: record failure, try next provider

5. API handler returns response
   → Streaming: proxy SSE with optional translation
   → Non-streaming: translate response body, record usage stats
```

## Key Features

| Feature | Description |
|---------|-------------|
| **Plan-based routing** | Each request targets a named plan (default, chat2api, jason, etc.) |
| **Model matching** | Providers with matching model are tried first |
| **Virtual providers** | `smart://plan` redirects to another plan internally |
| **Load balancing** | `round_robin`, `weighted_round_robin`, `lru` strategies per plan |
| **Health tracking** | Automatic cooldown after consecutive failures (BadgerDB) |
| **Format translation** | Full OpenAI ↔ Anthropic request/response/streaming translation |
| **Passthrough mode** | When clientFormat == provider.Format, proxy headers/body as-is |
| **Plan cache** | 30s TTL in-memory cache eliminates per-request SQLite IO |
| **Usage tracking** | Per-plan, per-provider token/request stats in SQLite |
| **Alerts** | Telegram bot commands + Discord outage notifications |
