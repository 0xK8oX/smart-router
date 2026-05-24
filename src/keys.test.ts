import { describe, it, expect, vi } from "vitest";
import { createKey, listKeys, getKey, updateKey, deleteKey } from "./keys";
import type { APIKey } from "./types";

function makeDb(rows: Array<Record<string, unknown>> = []) {
  const stmt = {
    bind: vi.fn(() => ({
      run: vi.fn(() => Promise.resolve()),
      first: vi.fn(() => Promise.resolve(rows[0] ?? null)),
      all: vi.fn(() => Promise.resolve({ results: rows })),
    })),
    run: vi.fn(() => Promise.resolve()),
    first: vi.fn(() => Promise.resolve(rows[0] ?? null)),
    all: vi.fn(() => Promise.resolve({ results: rows })),
  };
  return {
    prepare: vi.fn(() => stmt),
  } as unknown as D1Database;
}

function makeKey(overrides?: Partial<APIKey>): APIKey {
  return {
    key: "sr-test123",
    name: "test",
    plans: ["default"],
    models: [],
    rate_limit_rpm: 60,
    rate_limit_rpd: 1000,
    monthly_token_limit: 0,
    monthly_request_limit: 0,
    disabled: false,
    created_at: Date.now(),
    ...overrides,
  };
}

describe("createKey", () => {
  it("inserts a key into the database", async () => {
    const db = makeDb();
    const key = makeKey();
    await createKey(db, key);
    expect(db.prepare).toHaveBeenCalled();
  });
});

describe("getKey", () => {
  it("returns parsed key when found", async () => {
    const db = makeDb([{
      key: "sr-test123",
      name: "test",
      plans: '["default"]',
      models: '[]',
      rate_limit_rpm: 60,
      rate_limit_rpd: 1000,
      monthly_token_limit: 0,
      monthly_request_limit: 0,
      disabled: 0,
      created_at: Date.now(),
    }]);
    const result = await getKey(db, "sr-test123");
    expect(result).not.toBeNull();
    expect(result!.key).toBe("sr-test123");
    expect(result!.plans).toEqual(["default"]);
    expect(result!.disabled).toBe(false);
  });

  it("returns null when key not found", async () => {
    const db = makeDb([]);
    const result = await getKey(db, "sr-missing");
    expect(result).toBeNull();
  });
});

describe("listKeys", () => {
  it("returns all keys ordered by created_at", async () => {
    const db = makeDb([
      {
        key: "sr-a",
        name: "a",
        plans: '[]',
        models: '[]',
        rate_limit_rpm: 0,
        rate_limit_rpd: 0,
        monthly_token_limit: 0,
        monthly_request_limit: 0,
        disabled: 0,
        created_at: 1000,
      },
      {
        key: "sr-b",
        name: "b",
        plans: '[]',
        models: '[]',
        rate_limit_rpm: 0,
        rate_limit_rpd: 0,
        monthly_token_limit: 0,
        monthly_request_limit: 0,
        disabled: 0,
        created_at: 2000,
      },
    ]);
    const keys = await listKeys(db);
    expect(keys.length).toBe(2);
    expect(keys[0].key).toBe("sr-a");
  });
});

describe("updateKey", () => {
  it("throws when key not found", async () => {
    const db = makeDb([]);
    await expect(updateKey(db, "sr-missing", { name: "new" })).rejects.toThrow("Key not found");
  });

  it("updates key fields when found", async () => {
    const db = makeDb([{
      key: "sr-test123",
      name: "old",
      plans: '[]',
      models: '[]',
      rate_limit_rpm: 0,
      rate_limit_rpd: 0,
      monthly_token_limit: 0,
      monthly_request_limit: 0,
      disabled: 0,
      created_at: Date.now(),
    }]);
    await updateKey(db, "sr-test123", { name: "new" });
    expect(db.prepare).toHaveBeenCalled();
  });
});

describe("deleteKey", () => {
  it("deletes the key", async () => {
    const db = makeDb();
    await deleteKey(db, "sr-test123");
    expect(db.prepare).toHaveBeenCalled();
  });
});
