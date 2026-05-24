import { describe, it, expect, vi, beforeEach } from "vitest";
import { validateKey, generateAPIKey, maskAPIKey } from "./auth";
import type { APIKey } from "./types";

vi.mock("./keys", () => ({
  getKey: vi.fn(),
  updateKeyLastUsed: vi.fn(() => Promise.resolve()),
}));

vi.mock("./stats", () => ({
  getKeyUsageSince: vi.fn(),
  getKeyMonthlyUsage: vi.fn(),
}));

import { getKey } from "./keys";
import { getKeyUsageSince, getKeyMonthlyUsage } from "./stats";

function makeKey(overrides?: Partial<APIKey>): APIKey {
  return {
    key: "sr-testkey123",
    name: "test",
    plans: [],
    models: [],
    rate_limit_rpm: 0,
    rate_limit_rpd: 0,
    monthly_token_limit: 0,
    monthly_request_limit: 0,
    disabled: false,
    created_at: Date.now(),
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("generateAPIKey", () => {
  it("generates keys with sr- prefix", () => {
    const key = generateAPIKey();
    expect(key.startsWith("sr-")).toBe(true);
    expect(key.length).toBeGreaterThan(10);
  });

  it("generates unique keys", () => {
    const k1 = generateAPIKey();
    const k2 = generateAPIKey();
    expect(k1).not.toBe(k2);
  });
});

describe("maskAPIKey", () => {
  it("masks all but last 4 chars", () => {
    expect(maskAPIKey("sk-test-1234")).toBe("****1234");
  });

  it("masks short keys entirely", () => {
    expect(maskAPIKey("abc")).toBe("****");
  });
});

describe("validateKey", () => {
  it("returns 401 for missing key", async () => {
    vi.mocked(getKey).mockResolvedValue(null);
    const result = await validateKey({} as D1Database, "missing", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.status).toBe(401);
  });

  it("returns 403 for disabled key", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ disabled: true }));
    const result = await validateKey({} as D1Database, "sr-disabled", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.status).toBe(403);
  });

  it("returns 403 for expired key", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ expires_at: Date.now() - 1000 }));
    const result = await validateKey({} as D1Database, "sr-expired", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.status).toBe(403);
  });

  it("returns 403 for plan restriction", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ plans: ["pro"] }));
    const result = await validateKey({} as D1Database, "sr-test", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.message).toContain('Plan "default" is not allowed');
  });

  it("returns 403 for model restriction", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ models: ["claude-3"] }));
    const result = await validateKey({} as D1Database, "sr-test", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.message).toContain('Model "gpt-4" is not allowed');
  });

  it("returns 429 for RPM exceeded", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ rate_limit_rpm: 10 }));
    vi.mocked(getKeyUsageSince).mockResolvedValue({ request_tokens: 0, response_tokens: 0, request_count: 10 });
    const result = await validateKey({} as D1Database, "sr-test", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.status).toBe(429);
  });

  it("returns 429 for monthly token quota exceeded", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ monthly_token_limit: 1000 }));
    vi.mocked(getKeyUsageSince).mockResolvedValue({ request_tokens: 0, response_tokens: 0, request_count: 0 });
    vi.mocked(getKeyMonthlyUsage).mockResolvedValue({ request_tokens: 600, response_tokens: 500, request_count: 10 });
    const result = await validateKey({} as D1Database, "sr-test", "default", "gpt-4");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.status).toBe(429);
  });

  it("returns ok for valid key", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey());
    vi.mocked(getKeyUsageSince).mockResolvedValue({ request_tokens: 0, response_tokens: 0, request_count: 0 });
    vi.mocked(getKeyMonthlyUsage).mockResolvedValue({ request_tokens: 0, response_tokens: 0, request_count: 0 });
    const result = await validateKey({} as D1Database, "sr-test", "default", "gpt-4");
    expect(result.ok).toBe(true);
  });

  it("allows any plan when plans array is empty", async () => {
    vi.mocked(getKey).mockResolvedValue(makeKey({ plans: [] }));
    vi.mocked(getKeyUsageSince).mockResolvedValue({ request_tokens: 0, response_tokens: 0, request_count: 0 });
    vi.mocked(getKeyMonthlyUsage).mockResolvedValue({ request_tokens: 0, response_tokens: 0, request_count: 0 });
    const result = await validateKey({} as D1Database, "sr-test", "default", "gpt-4");
    expect(result.ok).toBe(true);
  });
});
