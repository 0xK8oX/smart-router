/**
 * Smart Router - API Key CRUD
 *
 * D1 operations for api_keys table.
 */

import type { APIKey } from "./types";

function parseKeyRow(row: Record<string, unknown>): APIKey {
  return {
    key: String(row.key),
    name: String(row.name ?? ""),
    plans: JSON.parse(String(row.plans ?? "[]")),
    models: JSON.parse(String(row.models ?? "[]")),
    rate_limit_rpm: Number(row.rate_limit_rpm ?? 0),
    rate_limit_rpd: Number(row.rate_limit_rpd ?? 0),
    monthly_token_limit: Number(row.monthly_token_limit ?? 0),
    monthly_request_limit: Number(row.monthly_request_limit ?? 0),
    expires_at: row.expires_at ? Number(row.expires_at) : undefined,
    disabled: Number(row.disabled ?? 0) !== 0,
    created_at: Number(row.created_at ?? 0),
    last_used_at: row.last_used_at ? Number(row.last_used_at) : undefined,
  };
}

export async function createKey(db: D1Database, key: APIKey): Promise<void> {
  await db.prepare(
    "INSERT INTO api_keys " +
    "(key, name, plans, models, rate_limit_rpm, rate_limit_rpd, monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at) " +
    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
  ).bind(
    key.key,
    key.name,
    JSON.stringify(key.plans),
    JSON.stringify(key.models),
    key.rate_limit_rpm,
    key.rate_limit_rpd,
    key.monthly_token_limit,
    key.monthly_request_limit,
    key.expires_at ?? null,
    key.disabled ? 1 : 0,
    key.created_at
  ).run();
}

export async function listKeys(db: D1Database): Promise<APIKey[]> {
  const result = await db.prepare(
    "SELECT key, name, plans, models, rate_limit_rpm, rate_limit_rpd, monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at FROM api_keys ORDER BY created_at DESC"
  ).all();
  return (result.results ?? []).map((r) => parseKeyRow(r as Record<string, unknown>));
}

export async function getKey(db: D1Database, key: string): Promise<APIKey | null> {
  const row = await db.prepare(
    "SELECT key, name, plans, models, rate_limit_rpm, rate_limit_rpd, monthly_token_limit, monthly_request_limit, expires_at, disabled, created_at, last_used_at FROM api_keys WHERE key = ?"
  ).bind(key).first();
  if (!row) return null;
  return parseKeyRow(row as Record<string, unknown>);
}

export async function updateKey(db: D1Database, key: string, updates: Partial<APIKey>): Promise<void> {
  const existing = await getKey(db, key);
  if (!existing) throw new Error("Key not found");

  const merged: APIKey = { ...existing, ...updates };
  await db.prepare(
    "UPDATE api_keys SET " +
    "name = ?, plans = ?, models = ?, rate_limit_rpm = ?, rate_limit_rpd = ?, " +
    "monthly_token_limit = ?, monthly_request_limit = ?, expires_at = ?, disabled = ? " +
    "WHERE key = ?"
  ).bind(
    merged.name,
    JSON.stringify(merged.plans),
    JSON.stringify(merged.models),
    merged.rate_limit_rpm,
    merged.rate_limit_rpd,
    merged.monthly_token_limit,
    merged.monthly_request_limit,
    merged.expires_at ?? null,
    merged.disabled ? 1 : 0,
    key
  ).run();
}

export async function deleteKey(db: D1Database, key: string): Promise<void> {
  await db.prepare("DELETE FROM api_keys WHERE key = ?").bind(key).run();
}

export async function updateKeyLastUsed(db: D1Database, key: string): Promise<void> {
  await db.prepare("UPDATE api_keys SET last_used_at = ? WHERE key = ?").bind(Date.now(), key).run();
}
