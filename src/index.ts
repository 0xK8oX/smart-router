/**
 * Smart Router - Cloudflare Worker Entry Point
 *
 * Endpoints:
 *   POST /v1/chat/completions  (OpenAI format)
 *   POST /v1/messages          (Anthropic format)
 *   GET  /v1/health            (Provider health status)
 *
 * Plan Management:
 *   GET    /v1/plans           (List all plans)
 *   GET    /v1/plans/:slug     (Get a specific plan)
 *   PUT    /v1/plans/:slug     (Replace a plan)
 *   DELETE /v1/plans/:slug     (Delete a plan)
 */

import { HealthTracker } from "./health-do";
import { routeRequest } from "./router";
import type { ClientFormat } from "./types";
import { queryStats, aggregateStats } from "./stats";
import {
  initDb,
  seedPlansIfEmpty,
  listPlans,
  deletePlan,
} from "./db";
import { upsertPlan } from "./config";
import { decryptKey } from "./crypto";

export { HealthTracker };

function corsHeaders(): Record<string, string> {
  return {
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "GET, POST, PUT, DELETE, OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type, Authorization, X-Plan, X-Admin-Key",
  };
}

let dbInitialized = false;

async function ensureDb(env: Env): Promise<void> {
  if (dbInitialized) return;
  await initDb(env.DB);
  await seedPlansIfEmpty(env.DB);
  dbInitialized = true;
}

function isAdmin(request: Request, env: Env): boolean {
  const adminKey = (env as unknown as Record<string, string>).ADMIN_KEY;
  if (!adminKey) return false;
  const header = request.headers.get("X-Admin-Key");
  return header === adminKey;
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders() });
    }

    await ensureDb(env);

    const url = new URL(request.url);
    const path = url.pathname;

    // ── Plan Management API ──────────────────────────────────────────────
    if (path === "/v1/plans") {
      if (request.method === "GET") {
        return handleListPlans(request, env);
      }
    }

    const planMatch = path.match(/^\/v1\/plans\/([^/]+)$/);
    if (planMatch) {
      const slug = decodeURIComponent(planMatch[1]);
      if (request.method === "GET") {
        return handleGetPlan(request, slug, env);
      }
      if (request.method === "PUT") {
        return handleUpdatePlan(slug, request, env);
      }
      if (request.method === "DELETE") {
        return handleDeletePlan(slug, env);
      }
    }

    // ── Health check endpoint ────────────────────────────────────────────
    if (path === "/v1/health" && request.method === "GET") {
      const plan = url.searchParams.get("plan") || "default";
      const id = env.HEALTH_TRACKER.idFromName("global");
      const stub = env.HEALTH_TRACKER.get(id);
      const res = await stub.fetch(`https://fake-host/health/state?plan=${encodeURIComponent(plan)}`);
      return new Response(res.body, {
        status: res.status,
        headers: { ...corsHeaders(), "Content-Type": "application/json" },
      });
    }

    if (path === "/v1/health/activity" && request.method === "GET") {
      const plan = url.searchParams.get("plan") || "default";
      const id = env.HEALTH_TRACKER.idFromName("global");
      const stub = env.HEALTH_TRACKER.get(id);
      const res = await stub.fetch(`https://fake-host/health/activity?plan=${encodeURIComponent(plan)}`);
      return new Response(res.body, {
        status: res.status,
        headers: { ...corsHeaders(), "Content-Type": "application/json" },
      });
    }

    // ── OpenAI Chat Completions ──────────────────────────────────────────
    if (path === "/v1/chat/completions" && request.method === "POST") {
      return handleChatRequest(request, env, "openai", ctx);
    }

    // ── Anthropic Messages ───────────────────────────────────────────────
    if (path === "/v1/messages" && request.method === "POST") {
      return handleChatRequest(request, env, "anthropic", ctx);
    }

    // ── Stats ────────────────────────────────────────────────────────────
    if (path === "/v1/stats" && request.method === "GET") {
      return handleQueryStats(url, env);
    }
    if (path === "/v1/stats/aggregated" && request.method === "GET") {
      return handleAggregateStats(url, env);
    }

    return new Response(JSON.stringify({ error: "Not found" }), {
      status: 404,
      headers: { ...corsHeaders(), "Content-Type": "application/json" },
    });
  },
};

// ── Plan Management Handlers ─────────────────────────────────────────────

async function handleListPlans(request: Request, env: Env): Promise<Response> {
  const admin = isAdmin(request, env);
  const plans = await listPlans(env.DB);

  const safe: Record<string, { providers: Array<Record<string, unknown>> }> = {};
  for (const [slug, config] of Object.entries(plans)) {
    safe[slug] = {
      providers: await Promise.all(config.providers.map(async (p) => {
        const provider = { ...p } as Record<string, unknown>;
        if (admin && provider.api_key) {
          // Admin: decrypt and show plain key
          try {
            provider.api_key = await decryptKey(provider.api_key as string, env);
          } catch {
            provider.api_key = "[decrypt failed]";
          }
          delete provider.masked_key;
        } else {
          // Normal: show masked_key, drop encrypted key
          delete provider.api_key;
        }
        return provider;
      })),
    };
  }
  return new Response(JSON.stringify(safe), {
    headers: { ...corsHeaders(), "Content-Type": "application/json" },
  });
}

async function handleGetPlan(request: Request, slug: string, env: Env): Promise<Response> {
  const admin = isAdmin(request, env);
  const plans = await listPlans(env.DB);
  const config = plans[slug];
  if (!config) {
    return new Response(JSON.stringify({ error: "Plan not found" }), {
      status: 404,
      headers: { ...corsHeaders(), "Content-Type": "application/json" },
    });
  }

  const providers = await Promise.all(config.providers.map(async (p) => {
    const provider = { ...p } as Record<string, unknown>;
    if (admin && provider.api_key) {
      try {
        provider.api_key = await decryptKey(provider.api_key as string, env);
      } catch {
        provider.api_key = "[decrypt failed]";
      }
      delete provider.masked_key;
    } else {
      delete provider.api_key;
    }
    return provider;
  }));

  return new Response(JSON.stringify({ providers }), {
    headers: { ...corsHeaders(), "Content-Type": "application/json" },
  });
}

export async function handleUpdatePlan(
  slug: string,
  request: Request,
  env: Env
): Promise<Response> {
  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return new Response(JSON.stringify({ error: "Invalid JSON body" }), {
      status: 400,
      headers: { ...corsHeaders(), "Content-Type": "application/json" },
    });
  }

  const config = body as { providers?: Array<Record<string, unknown>> };
  if (!config.providers || !Array.isArray(config.providers)) {
    return new Response(
      JSON.stringify({ error: "Missing providers array" }),
      { status: 400, headers: { ...corsHeaders(), "Content-Type": "application/json" } }
    );
  }

  // Fetch existing plan to preserve api_key when not provided in update
  const existingRaw = await listPlans(env.DB);
  const existing = existingRaw[slug];
  if (existing) {
    const existingKeys = new Map(existing.providers.map((p) => [p.name, p]));
    for (const p of config.providers) {
      if (!p.api_key) {
        const prev = existingKeys.get(p.name as string);
        if (prev?.api_key) {
          p.api_key = prev.api_key;
        }
      }
    }
  }

  await upsertPlan(env, slug, config as unknown as import("./types").PlanConfig);

  return new Response(JSON.stringify({ ok: true, slug }), {
    headers: { ...corsHeaders(), "Content-Type": "application/json" },
  });
}

async function handleDeletePlan(slug: string, env: Env): Promise<Response> {
  await deletePlan(env.DB, slug);
  return new Response(JSON.stringify({ ok: true, slug }), {
    headers: { ...corsHeaders(), "Content-Type": "application/json" },
  });
}

async function handleQueryStats(url: URL, env: Env): Promise<Response> {
  const filters = {
    plan: url.searchParams.get("plan") || undefined,
    provider: url.searchParams.get("provider") || undefined,
    model: url.searchParams.get("model") || undefined,
    from: parseTimeParam(url.searchParams.get("from")),
    to: parseTimeParam(url.searchParams.get("to")),
    limit: parseInt(url.searchParams.get("limit") || "100", 10),
  };
  const rows = await queryStats(env.DB, filters);
  return new Response(JSON.stringify(rows), {
    headers: { ...corsHeaders(), "Content-Type": "application/json" },
  });
}

async function handleAggregateStats(url: URL, env: Env): Promise<Response> {
  const groupBy = (url.searchParams.get("group_by") || "provider") as import("./types").AggregateOptions["groupBy"];
  const options: import("./types").AggregateOptions = {
    groupBy,
    from: parseTimeParam(url.searchParams.get("from")),
    to: parseTimeParam(url.searchParams.get("to")),
    limit: parseInt(url.searchParams.get("limit") || "100", 10),
  };
  const results = await aggregateStats(env.DB, options);
  return new Response(JSON.stringify({ group_by: groupBy, results }), {
    headers: { ...corsHeaders(), "Content-Type": "application/json" },
  });
}

function parseTimeParam(value: string | null): number | undefined {
  if (!value) return undefined;
  const num = parseInt(value, 10);
  if (!isNaN(num)) return num;
  const date = Date.parse(value);
  if (!isNaN(date)) return date;
  return undefined;
}

// ── Chat Request Handler ─────────────────────────────────────────────────

async function handleChatRequest(
  request: Request,
  env: Env,
  clientFormat: ClientFormat,
  ctx: ExecutionContext
): Promise<Response> {
  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return new Response(JSON.stringify({ error: "Invalid JSON body" }), {
      status: 400,
      headers: { ...corsHeaders(), "Content-Type": "application/json" },
    });
  }

  const url = new URL(request.url);
  const plan = request.headers.get("X-Plan") || url.searchParams.get("plan") || "default";
  const isStreaming = (body as Record<string, unknown>)?.stream === true;

  const routerReq = {
    body,
    clientFormat,
    plan,
    isStreaming,
  };

  const response = await routeRequest(routerReq, env, ctx);

  // Add CORS headers to the response
  const newHeaders = new Headers(response.headers);
  for (const [k, v] of Object.entries(corsHeaders())) {
    newHeaders.set(k, v);
  }

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: newHeaders,
  });
}
