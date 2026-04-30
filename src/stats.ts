/**
 * Smart Router - Stats Tracking
 *
 * Non-blocking request/response token usage recording.
 * Uses D1 for storage; writes are fire-and-forget via ctx.waitUntil.
 */

import type { StatRecord, AggregateOptions, AggregateResult } from "./types";

export async function initStatsTable(db: D1Database): Promise<void> {
  await db.prepare(
    "CREATE TABLE IF NOT EXISTS request_stats (" +
    "id INTEGER PRIMARY KEY AUTOINCREMENT, " +
    "plan TEXT NOT NULL, " +
    "provider TEXT NOT NULL, " +
    "model TEXT NOT NULL, " +
    "key_mask TEXT, " +
    "request_tokens INTEGER DEFAULT 0, " +
    "response_tokens INTEGER DEFAULT 0, " +
    "total_tokens INTEGER DEFAULT 0, " +
    "status TEXT NOT NULL, " +
    "latency_ms INTEGER, " +
    "is_streaming INTEGER DEFAULT 0, " +
    "created_at INTEGER NOT NULL)"
  ).run();

  await db.prepare(
    "CREATE INDEX IF NOT EXISTS idx_stats_plan ON request_stats(plan, created_at)"
  ).run();
  await db.prepare(
    "CREATE INDEX IF NOT EXISTS idx_stats_provider ON request_stats(provider, created_at)"
  ).run();
  await db.prepare(
    "CREATE INDEX IF NOT EXISTS idx_stats_model ON request_stats(model, created_at)"
  ).run();
  await db.prepare(
    "CREATE INDEX IF NOT EXISTS idx_stats_created ON request_stats(created_at)"
  ).run();
}

export async function recordStat(db: D1Database, stat: StatRecord): Promise<void> {
  try {
    await db.prepare(
      "INSERT INTO request_stats " +
      "(plan, provider, model, key_mask, request_tokens, response_tokens, total_tokens, status, latency_ms, is_streaming, created_at) " +
      "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
    ).bind(
      stat.plan,
      stat.provider,
      stat.model,
      stat.key_mask ?? null,
      stat.request_tokens,
      stat.response_tokens,
      stat.total_tokens,
      stat.status,
      stat.latency_ms,
      stat.is_streaming ? 1 : 0,
      Date.now()
    ).run();
  } catch (err) {
    console.log(`[STATS] FAILED to record stat: ${err instanceof Error ? err.message : String(err)}`);
  }
}

export async function queryStats(
  db: D1Database,
  filters: {
    plan?: string;
    provider?: string;
    model?: string;
    from?: number;
    to?: number;
    limit?: number;
  }
): Promise<unknown[]> {
  const conditions: string[] = [];
  const params: (string | number)[] = [];

  if (filters.plan) {
    conditions.push("plan = ?");
    params.push(filters.plan);
  }
  if (filters.provider) {
    conditions.push("provider = ?");
    params.push(filters.provider);
  }
  if (filters.model) {
    conditions.push("model = ?");
    params.push(filters.model);
  }
  if (filters.from) {
    conditions.push("created_at >= ?");
    params.push(filters.from);
  }
  if (filters.to) {
    conditions.push("created_at <= ?");
    params.push(filters.to);
  }

  const where = conditions.length > 0 ? "WHERE " + conditions.join(" AND ") : "";
  const limit = Math.min(filters.limit ?? 100, 1000);

  const stmt = db.prepare(
    `SELECT * FROM request_stats ${where} ORDER BY created_at DESC LIMIT ?`
  ).bind(...params, limit);

  const result = await stmt.all();
  return (result.results ?? []) as unknown[];
}

export async function aggregateStats(
  db: D1Database,
  options: AggregateOptions
): Promise<AggregateResult[]> {
  const groupCol = options.groupBy;
  const conditions: string[] = [];
  const params: (string | number)[] = [];

  if (options.from) {
    conditions.push("created_at >= ?");
    params.push(options.from);
  }
  if (options.to) {
    conditions.push("created_at <= ?");
    params.push(options.to);
  }

  const where = conditions.length > 0 ? "WHERE " + conditions.join(" AND ") : "";
  const limit = Math.min(options.limit ?? 100, 1000);

  const stmt = db.prepare(
    `SELECT ${groupCol} as dimension, ` +
    "COUNT(*) as requests, " +
    "SUM(request_tokens) as request_tokens, " +
    "SUM(response_tokens) as response_tokens, " +
    "SUM(total_tokens) as total_tokens, " +
    "AVG(latency_ms) as avg_latency_ms " +
    `FROM request_stats ${where} ` +
    `GROUP BY ${groupCol} ORDER BY requests DESC LIMIT ?`
  ).bind(...params, limit);

  const result = await stmt.all();
  return (result.results ?? []).map((r: unknown) => ({
    dimension: String((r as Record<string, unknown>).dimension),
    requests: Number((r as Record<string, unknown>).requests),
    request_tokens: Number((r as Record<string, unknown>).request_tokens),
    response_tokens: Number((r as Record<string, unknown>).response_tokens),
    total_tokens: Number((r as Record<string, unknown>).total_tokens),
    avg_latency_ms: Math.round(Number((r as Record<string, unknown>).avg_latency_ms) || 0),
  })) as AggregateResult[];
}

export async function getWeeklyUsage(
  db: D1Database,
  keyMask: string
): Promise<{ request_tokens: number; response_tokens: number; request_count: number }> {
  const oneWeekAgo = Date.now() - 7 * 24 * 60 * 60 * 1000;
  try {
    const result = await db
      .prepare(
        "SELECT SUM(request_tokens) as request_tokens, SUM(response_tokens) as response_tokens, COUNT(*) as request_count " +
        "FROM request_stats WHERE key_mask = ? AND created_at > ? AND status = 'success'"
      )
      .bind(keyMask, oneWeekAgo)
      .first();

    if (!result) {
      return { request_tokens: 0, response_tokens: 0, request_count: 0 };
    }

    return {
      request_tokens: Number(result.request_tokens ?? 0),
      response_tokens: Number(result.response_tokens ?? 0),
      request_count: Number(result.request_count ?? 0),
    };
  } catch (err) {
    console.log(`[STATS] FAILED to get weekly usage: ${err instanceof Error ? err.message : String(err)}`);
    return { request_tokens: 0, response_tokens: 0, request_count: 0 };
  }
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function extractUsage(responseBody: any): { request_tokens: number; response_tokens: number } | null {
  if (!responseBody || typeof responseBody !== "object") return null;

  const usage = responseBody.usage;
  if (!usage || typeof usage !== "object") return null;

  const reqTokens = usage.input_tokens ?? usage.prompt_tokens ?? 0;
  const resTokens = usage.output_tokens ?? usage.completion_tokens ?? 0;

  return {
    request_tokens: Number(reqTokens) || 0,
    response_tokens: Number(resTokens) || 0,
  };
}
