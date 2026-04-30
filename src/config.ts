/**
 * Smart Router - Config loading
 *
 * Plan configurations and encrypted API keys are fetched from D1 at runtime.
 * Master encryption key (KEY_ENCRYPTION_KEY) stays in Wrangler secrets.
 * Plans are cached in memory for 60s to avoid repeated DB queries.
 */

import type { ProviderConfig, PlanConfig } from "./types";
import { getPlan as dbGetPlan, upsertPlan as dbUpsertPlan, maskKey } from "./db";
import { decryptKey, encryptKey } from "./crypto";

const planCache = new Map<string, { config: PlanConfig; ts: number }>();
const CACHE_TTL_MS = 60_000;

async function loadAndDecryptPlan(
  db: D1Database,
  name: string,
  env: Env
): Promise<PlanConfig | null> {
  const raw = await dbGetPlan(db, name);
  if (!raw) return null;

  const providers: ProviderConfig[] = [];
  for (const p of raw.providers) {
    const provider = { ...p };
    if (provider.api_key) {
      try {
        provider.api_key = await decryptKey(provider.api_key, env);
      } catch {
        provider.api_key = undefined;
      }
    }
    providers.push(provider);
  }
  return { providers };
}

/**
 * Detect whether a key is already encrypted by attempting decryption.
 * Returns { encrypted: string, masked: string } either way.
 */
async function prepareProviderKey(
  apiKey: string,
  env: Env
): Promise<{ encrypted: string; masked: string }> {
  try {
    const decrypted = await decryptKey(apiKey, env);
    // Already encrypted - keep the same ciphertext, update mask from decrypted value
    return { encrypted: apiKey, masked: maskKey(decrypted) };
  } catch {
    // Plaintext - encrypt it
    return { encrypted: await encryptKey(apiKey, env), masked: maskKey(apiKey) };
  }
}

/**
 * Get a plan by name from D1 with in-memory caching.
 * Falls back to "default" if not found.
 */
export async function getPlan(
  env: Env,
  name: string
): Promise<PlanConfig | null> {
  const db = env.DB;
  const cacheKey = name;
  const cached = planCache.get(cacheKey);
  if (cached && Date.now() - cached.ts < CACHE_TTL_MS) {
    return cached.config;
  }

  let config = await loadAndDecryptPlan(db, name, env);
  if (!config) {
    config = await loadAndDecryptPlan(db, "default", env);
  }

  if (config) {
    planCache.set(cacheKey, { config, ts: Date.now() });
  }
  return config;
}

/**
 * Replace a plan. Encrypts plaintext keys, preserves already-encrypted keys.
 * Invalidates cache.
 */
export async function upsertPlan(
  env: Env,
  slug: string,
  config: PlanConfig
): Promise<void> {
  const db = env.DB;
  const providers: ProviderConfig[] = [];

  for (const p of config.providers) {
    const provider = { ...p };
    if (provider.api_key) {
      const prepared = await prepareProviderKey(provider.api_key, env);
      provider.api_key = prepared.encrypted;
      provider.masked_key = prepared.masked;
    } else {
      provider.api_key = undefined;
      provider.masked_key = undefined;
    }
    providers.push(provider);
  }

  await dbUpsertPlan(db, slug, { providers });
  planCache.delete(slug);
}

