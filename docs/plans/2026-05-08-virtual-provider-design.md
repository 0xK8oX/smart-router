# Virtual Provider (Internal Plan Redirect) Design

**Date:** 2026-05-08
**Status:** Draft

## Overview

Allow plans to reference other plans as a "virtual provider" — a provider that routes internally to another plan instead of making an external API call. This avoids unnecessary round trips when chaining plan fallbacks.

## Data Structure

A virtual provider looks like a normal provider entry:

```json
{
  "name": "auto-chat2api",
  "base_url": "smart://chat2api",
  "format": "anthropic",
  "timeout": 60
}
```

Convention: `smart://` prefix means "internal plan redirect".

## Routing Logic

1. In `router.ts`, when iterating providers, detect `smart://` URIs:

```typescript
function isVirtualProvider(baseUrl: string): boolean {
  return baseUrl.startsWith("smart://");
}

function extractPlanFromUrl(baseUrl: string): string {
  return baseUrl.slice("smart://".length);
}
```

2. If a provider is virtual:
   - Mark it as "trying"
   - Recursively call `routeRequest()` with target plan
   - If recursive call succeeds → return that response
   - If it fails → record combined errors, treat virtual provider as failed

## Depth Limit

To prevent infinite loops (e.g., `compact → auto-chat2api → auto-compact`), add a recursion depth limit:

```typescript
const MAX_VIRTUAL_DEPTH = 3;
```

Pass `depth` in recursive calls. If `depth > MAX_VIRTUAL_DEPTH`, return `503` with `"Max routing depth exceeded"`.

## Stats Recording

Virtual providers record stats for both levels:

```typescript
// Stats record structure
{
  plan: "compact",
  provider: "auto-chat2api",         // virtual provider
  target_provider: "DeepSeek-V4-Pro", // actual provider in target plan
  ...
}
```

This provides full visibility into the routing chain.

## Health Tracking

Virtual providers don't track independent health — they inherit from the target plan's providers. Query the target plan's provider health via `HealthTracker DO`.

If all providers in the target plan are in cooldown, the virtual provider is effectively unhealthy.

## Error Handling

- **Success:** Return the response from the recursive call
- **All providers fail:** Record virtual provider as failed with combined errors
- **Max depth exceeded:** Return `503` with `"Max routing depth exceeded"`
- **Quota/Context limits:** No special handling needed — applied per-provider in target plan

## Edge Cases

- **No real API key on virtual provider:** N/A — virtual providers have no key
- **`context_length`/`max_output_tokens` on virtual provider:** Ignored, meaningless without real API
- **Quota limits on virtual provider:** Skipped — quota tracked per real API key
