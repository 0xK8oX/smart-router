/**
 * Smart Router - HealthTracker Durable Object
 *
 * Circuit breaker with per-key health state (shared across plans).
 * State is persisted via DO storage to survive hibernation.
 */

import type { ProviderHealth } from "./types";

const STORAGE_KEY = "health_state_v2";

const CIRCUIT_RULES: Record<string, { threshold: number; cooldownMs: number }> = {
  auth: { threshold: 1, cooldownMs: 60 * 60 * 1000 },            // 1 hour
  quota: { threshold: 1, cooldownMs: 5 * 60 * 60 * 1000 },      // 5 hours
  rate_limit: { threshold: 3, cooldownMs: 5 * 60 * 1000 },       // 5 min
  server_error: { threshold: 2, cooldownMs: 2 * 60 * 1000 },     // 2 min
  connection: { threshold: 2, cooldownMs: 60 * 1000 },           // 1 min
  timeout: { threshold: 2, cooldownMs: 2 * 60 * 1000 },          // 2 min
  unknown: { threshold: 3, cooldownMs: 60 * 1000 },              // 1 min
};

function classifyFailure(status: number, message: string): string {
  const msg = message.toLowerCase();
  if (status === 401 || msg.includes("authentication") || msg.includes("unauthorized") || msg.includes("invalid api key")) {
    return "auth";
  }
  if (status === 402 || msg.includes("quota") || msg.includes("credit") || msg.includes("billing")) {
    return "quota";
  }
  if (status === 429 || msg.includes("rate limit") || msg.includes("too many requests")) {
    return "rate_limit";
  }
  if (status >= 500 && status < 600) {
    return "server_error";
  }
  if (msg.includes("connection") || msg.includes("refused") || msg.includes("econnrefused")) {
    return "connection";
  }
  if (msg.includes("timeout") || msg.includes("etimedout")) {
    return "timeout";
  }
  return "unknown";
}

function makeHealthy(): ProviderHealth {
  return {
    status: "healthy",
    consecutiveFailures: 0,
    lastFailureAt: 0,
    cooldownUntil: 0,
    lastFailureReason: "",
    lastSuccessAt: 0,
    totalRequests: 0,
    successCount: 0,
    lastActivityAt: 0,
  };
}

export class HealthTracker implements DurableObject {
  private state: DurableObjectState;
  private health: Map<string, ProviderHealth> = new Map();
  private loaded: boolean = false;

  constructor(state: DurableObjectState) {
    this.state = state;
  }

  private async loadState(): Promise<void> {
    if (this.loaded) return;
    const stored = await this.state.storage.get<Record<string, ProviderHealth>>(STORAGE_KEY);
    if (stored) {
      for (const [keyId, h] of Object.entries(stored)) {
        this.health.set(keyId, this.normalizeHealth(h));
      }
    }
    this.loaded = true;
  }

  private async saveState(): Promise<void> {
    const obj: Record<string, ProviderHealth> = {};
    for (const [keyId, h] of this.health.entries()) {
      obj[keyId] = h;
    }
    await this.state.storage.put(STORAGE_KEY, obj);
  }

  async fetch(request: Request): Promise<Response> {
    await this.loadState();
    const url = new URL(request.url);
    const path = url.pathname;

    if (path === "/health" && request.method === "POST") {
      const body = (await request.json()) as {
        action: "recordFailure" | "recordSuccess" | "getHealthyProviders";
        plan: string;
        provider?: string;
        keyId?: string;
        status?: number;
        message?: string;
        providerList?: Array<{ name: string; keyId?: string }>;
      };

      switch (body.action) {
        case "recordFailure":
          if (!body.keyId) {
            return new Response("Missing keyId", { status: 400 });
          }
          this.recordFailure(body.plan, body.provider ?? body.keyId, body.keyId, body.status ?? 0, body.message ?? "");
          await this.saveState();
          return new Response(JSON.stringify({ ok: true }), {
            headers: { "Content-Type": "application/json" },
          });

        case "recordSuccess":
          if (!body.keyId) {
            return new Response("Missing keyId", { status: 400 });
          }
          this.recordSuccess(body.plan, body.provider ?? body.keyId, body.keyId);
          await this.saveState();
          return new Response(JSON.stringify({ ok: true }), {
            headers: { "Content-Type": "application/json" },
          });

        case "getHealthyProviders":
          if (!body.providerList) {
            return new Response("Missing providerList", { status: 400 });
          }
          const healthy = this.getHealthyProviders(body.plan, body.providerList);
          return new Response(JSON.stringify({ providers: healthy }), {
            headers: { "Content-Type": "application/json" },
          });

        default:
          return new Response("Unknown action", { status: 400 });
      }
    }

    if (path === "/health/state" && request.method === "GET") {
      // Normalize all entries so newly-added fields get defaults for old stored state
      const result: Record<string, ProviderHealth> = {};
      for (const [keyId, h] of this.health.entries()) {
        result[keyId] = this.normalizeHealth(h);
      }
      return new Response(JSON.stringify(result), {
        headers: { "Content-Type": "application/json" },
      });
    }

    if (path === "/health/activity" && request.method === "GET") {
      const sorted = Array.from(this.health.entries())
        .sort((a, b) => b[1].lastActivityAt - a[1].lastActivityAt)
        .map(([keyId, h]) => ({
          keyId,
          status: h.status,
          lastSuccessAt: h.lastSuccessAt,
          lastFailureAt: h.lastFailureAt,
          lastActivityAt: h.lastActivityAt,
          totalRequests: h.totalRequests,
          successCount: h.successCount,
          consecutiveFailures: h.consecutiveFailures,
          cooldownUntil: h.cooldownUntil,
        }));
      return new Response(JSON.stringify({ providers: sorted }), {
        headers: { "Content-Type": "application/json" },
      });
    }

    return new Response("Not found", { status: 404 });
  }

  private normalizeHealth(stored: ProviderHealth): ProviderHealth {
    const defaults = makeHealthy();
    const merged: ProviderHealth = { ...defaults };
    for (const key of Object.keys(defaults) as Array<keyof ProviderHealth>) {
      const val = stored[key];
      if (val !== null && val !== undefined) {
        merged[key] = val as never;
      }
    }
    return merged;
  }

  private getProviderHealth(keyId: string): ProviderHealth {
    if (!this.health.has(keyId)) {
      this.health.set(keyId, makeHealthy());
    }
    return this.health.get(keyId)!;
  }

  recordFailure(plan: string, provider: string, keyId: string, status: number, message: string): void {
    const h = this.getProviderHealth(keyId);
    const reason = classifyFailure(status, message);
    const rule = CIRCUIT_RULES[reason] ?? CIRCUIT_RULES.unknown;

    h.consecutiveFailures++;
    h.lastFailureAt = Date.now();
    h.lastFailureReason = reason;
    h.totalRequests++;
    h.lastActivityAt = Date.now();

    if (h.consecutiveFailures >= rule.threshold) {
      h.status = "unhealthy";
      h.cooldownUntil = Date.now() + rule.cooldownMs;
      console.log(`[CIRCUIT_BREAKER] TRIPPED: plan=${plan} provider=${provider} keyId=${keyId} reason=${reason} failures=${h.consecutiveFailures} cooldownMs=${rule.cooldownMs} status=${status}`);
    } else if (h.consecutiveFailures >= Math.max(1, Math.floor(rule.threshold / 2))) {
      h.status = "degraded";
      console.log(`[CIRCUIT_BREAKER] DEGRADED: plan=${plan} provider=${provider} keyId=${keyId} reason=${reason} failures=${h.consecutiveFailures} status=${status}`);
    } else {
      console.log(`[CIRCUIT_BREAKER] RECORD_FAILURE: plan=${plan} provider=${provider} keyId=${keyId} reason=${reason} failures=${h.consecutiveFailures} status=${status}`);
    }
  }

  recordSuccess(plan: string, provider: string, keyId: string): void {
    const h = this.getProviderHealth(keyId);
    const wasUnhealthy = h.status === "unhealthy" || h.status === "degraded";
    h.status = "healthy";
    h.consecutiveFailures = 0;
    h.cooldownUntil = 0;
    h.lastFailureReason = "";
    h.totalRequests++;
    h.successCount++;
    h.lastSuccessAt = Date.now();
    h.lastActivityAt = Date.now();
    if (wasUnhealthy) {
      console.log(`[CIRCUIT_BREAKER] RECOVERED: plan=${plan} provider=${provider} keyId=${keyId}`);
    }
  }

  getHealthyProviders(plan: string, providerList: Array<{ name: string; keyId?: string }>): Array<{ name: string; keyId?: string }> {
    const now = Date.now();
    const result: Array<{ name: string; keyId?: string }> = [];

    for (const p of providerList) {
      const keyId = p.keyId ?? p.name;
      const h = this.getProviderHealth(keyId);

      // If cooldown expired, auto-reset to healthy
      if (h.status === "unhealthy" && now >= h.cooldownUntil) {
        console.log(`[CIRCUIT_BREAKER] COOLDOWN_EXPIRED: plan=${plan} provider=${p.name} keyId=${keyId} reset to healthy`);
        h.status = "healthy";
        h.consecutiveFailures = 0;
        h.cooldownUntil = 0;
      }

      if (h.status !== "unhealthy") {
        result.push(p);
      }
    }

    return result;
  }
}
