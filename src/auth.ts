/**
 * Smart Router - API Key Authentication
 *
 * Key generation, validation, rate limiting, and quota enforcement.
 */

import type { APIKey } from "./types";
import { getKey, updateKeyLastUsed } from "./keys";
import { getKeyUsageSince, getKeyMonthlyUsage } from "./stats";

export interface AuthResult {
  ok: true;
  key: APIKey;
}

export interface AuthError {
  ok: false;
  status: number;
  message: string;
}

export function generateAPIKey(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let result = "sr-";
  const rand = crypto.getRandomValues(new Uint8Array(32));
  for (let i = 0; i < 32; i++) {
    result += chars[rand[i] % chars.length];
  }
  return result;
}

export function maskAPIKey(key: string): string {
  if (key.length <= 4) return "****";
  return "****" + key.slice(-4);
}

export async function validateKey(
  db: D1Database,
  key: string,
  plan: string,
  model: string
): Promise<AuthResult | AuthError> {
  const record = await getKey(db, key);
  if (!record) {
    return { ok: false, status: 401, message: "Invalid API key" };
  }

  if (record.disabled) {
    return { ok: false, status: 403, message: "API key is disabled" };
  }

  if (record.expires_at && Date.now() > record.expires_at) {
    return { ok: false, status: 403, message: "API key has expired" };
  }

  // Plan restriction
  if (record.plans.length > 0 && !record.plans.includes(plan)) {
    return { ok: false, status: 403, message: `Plan "${plan}" is not allowed for this key` };
  }

  // Model restriction
  if (record.models.length > 0 && !record.models.includes(model)) {
    return { ok: false, status: 403, message: `Model "${model}" is not allowed for this key` };
  }

  // Rate limits
  if (record.rate_limit_rpm > 0 || record.rate_limit_rpd > 0) {
    const now = Date.now();
    const oneMinuteAgo = now - 60 * 1000;
    const oneDayAgo = now - 24 * 60 * 60 * 1000;

    if (record.rate_limit_rpm > 0) {
      const usage = await getKeyUsageSince(db, key, oneMinuteAgo);
      if (usage.request_count >= record.rate_limit_rpm) {
        return { ok: false, status: 429, message: "Rate limit exceeded: too many requests per minute" };
      }
    }

    if (record.rate_limit_rpd > 0) {
      const usage = await getKeyUsageSince(db, key, oneDayAgo);
      if (usage.request_count >= record.rate_limit_rpd) {
        return { ok: false, status: 429, message: "Rate limit exceeded: too many requests per day" };
      }
    }
  }

  // Monthly quotas
  if (record.monthly_token_limit > 0 || record.monthly_request_limit > 0) {
    const now = new Date();
    const monthly = await getKeyMonthlyUsage(db, key, now.getFullYear(), now.getMonth() + 1);

    if (record.monthly_token_limit > 0) {
      const totalTokens = monthly.request_tokens + monthly.response_tokens;
      if (totalTokens >= record.monthly_token_limit) {
        return { ok: false, status: 429, message: "Monthly token quota exceeded" };
      }
    }

    if (record.monthly_request_limit > 0) {
      if (monthly.request_count >= record.monthly_request_limit) {
        return { ok: false, status: 429, message: "Monthly request quota exceeded" };
      }
    }
  }

  // Update last_used_at (fire-and-forget)
  updateKeyLastUsed(db, key).catch(() => {});

  return { ok: true, key: record };
}
