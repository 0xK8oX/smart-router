import { describe, it, expect, beforeEach } from "vitest";
import { HealthTracker } from "./health-do";

function makeRequest(body: unknown): Request {
  return new Request("https://fake-host/health", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function makeStateRequest(): Request {
  return new Request("https://fake-host/health/state", { method: "GET" });
}

function makeActivityRequest(): Request {
  return new Request("https://fake-host/health/activity", { method: "GET" });
}

describe("HealthTracker", () => {
  let tracker: HealthTracker;

  beforeEach(() => {
    const mockState = {
      storage: {
        get: () => Promise.resolve(null),
        put: () => Promise.resolve(),
      },
    } as unknown as DurableObjectState;
    tracker = new HealthTracker(mockState);
  });

  describe("recordFailure", () => {
    it("increments consecutiveFailures and sets degraded after threshold/2", async () => {
      await tracker.fetch(makeRequest({
        action: "recordFailure",
        plan: "test",
        provider: "p1",
        keyId: "key-a",
        status: 500,
        message: "server error",
      }));

      const res = await tracker.fetch(makeStateRequest());
      const state = (await res.json()) as Record<string, unknown>;
      expect((state["key-a"] as { status: string }).status).toBe("degraded");
      expect((state["key-a"] as { consecutiveFailures: number }).consecutiveFailures).toBe(1);
    });

    it("trips to unhealthy after threshold consecutive failures", async () => {
      for (let i = 0; i < 3; i++) {
        await tracker.fetch(makeRequest({
          action: "recordFailure",
          plan: "test",
          provider: "p1",
          keyId: "key-b",
          status: 429,
          message: "rate limit",
        }));
      }

      const res = await tracker.fetch(makeStateRequest());
      const state = (await res.json()) as Record<string, unknown>;
      expect((state["key-b"] as { status: string }).status).toBe("unhealthy");
      expect((state["key-b"] as { cooldownUntil: number }).cooldownUntil).toBeGreaterThan(Date.now());
    });
  });

  describe("recordSuccess", () => {
    it("resets failures and marks healthy", async () => {
      // First make it degraded
      await tracker.fetch(makeRequest({
        action: "recordFailure",
        plan: "test",
        provider: "p1",
        keyId: "key-c",
        status: 500,
        message: "error",
      }));

      // Then succeed
      await tracker.fetch(makeRequest({
        action: "recordSuccess",
        plan: "test",
        provider: "p1",
        keyId: "key-c",
      }));

      const res = await tracker.fetch(makeStateRequest());
      const state = (await res.json()) as Record<string, unknown>;
      expect((state["key-c"] as { status: string }).status).toBe("healthy");
      expect((state["key-c"] as { consecutiveFailures: number }).consecutiveFailures).toBe(0);
      expect((state["key-c"] as { successCount: number }).successCount).toBe(1);
    });
  });

  describe("getHealthyProviders", () => {
    it("filters out unhealthy providers", async () => {
      // Trip key-x to unhealthy
      for (let i = 0; i < 3; i++) {
        await tracker.fetch(makeRequest({
          action: "recordFailure",
          plan: "test",
          provider: "px",
          keyId: "key-x",
          status: 429,
          message: "rate limit",
        }));
      }

      const res = await tracker.fetch(makeRequest({
        action: "getHealthyProviders",
        plan: "test",
        providerList: [
          { name: "px", keyId: "key-x" },
          { name: "py", keyId: "key-y" },
        ],
      }));

      const data = (await res.json()) as { providers: Array<{ name: string }> };
      expect(data.providers).toHaveLength(1);
      expect(data.providers[0].name).toBe("py");
    });

    it("auto-resets expired cooldowns", async () => {
      // Manually insert an expired unhealthy state
      const mockState = {
        storage: {
          get: () => Promise.resolve({
            "key-expired": {
              status: "unhealthy",
              consecutiveFailures: 5,
              lastFailureAt: Date.now() - 100000,
              cooldownUntil: Date.now() - 1000,
              lastFailureReason: "timeout",
              lastSuccessAt: 0,
              totalRequests: 5,
              successCount: 0,
              lastActivityAt: Date.now() - 100000,
            },
          }),
          put: () => Promise.resolve(),
        },
      } as unknown as DurableObjectState;
      const t = new HealthTracker(mockState);

      const res = await t.fetch(makeRequest({
        action: "getHealthyProviders",
        plan: "test",
        providerList: [{ name: "px", keyId: "key-expired" }],
      }));

      const data = (await res.json()) as { providers: Array<{ name: string }> };
      expect(data.providers).toHaveLength(1);
      expect(data.providers[0].name).toBe("px");
    });
  });

  describe("shared health across plans", () => {
    it("shares state for the same keyId across different plans", async () => {
      // Record failure in plan-a
      await tracker.fetch(makeRequest({
        action: "recordFailure",
        plan: "plan-a",
        provider: "p1",
        keyId: "shared-key",
        status: 429,
        message: "rate limit",
      }));

      // Record another failure in plan-b with same keyId
      await tracker.fetch(makeRequest({
        action: "recordFailure",
        plan: "plan-b",
        provider: "p2",
        keyId: "shared-key",
        status: 429,
        message: "rate limit",
      }));

      const res = await tracker.fetch(makeStateRequest());
      const state = (await res.json()) as Record<string, unknown>;
      expect((state["shared-key"] as { consecutiveFailures: number }).consecutiveFailures).toBe(2);

      // getHealthyProviders in plan-c should see the same state
      const healthyRes = await tracker.fetch(makeRequest({
        action: "getHealthyProviders",
        plan: "plan-c",
        providerList: [{ name: "p3", keyId: "shared-key" }],
      }));
      const healthyData = (await healthyRes.json()) as { providers: Array<{ name: string }> };
      expect(healthyData.providers).toHaveLength(1); // degraded but not yet unhealthy (threshold for rate_limit is 3)
    });

    it("isolates state for different keyIds", async () => {
      await tracker.fetch(makeRequest({
        action: "recordFailure",
        plan: "test",
        provider: "p1",
        keyId: "key-1",
        status: 500,
        message: "error",
      }));

      const res = await tracker.fetch(makeRequest({
        action: "getHealthyProviders",
        plan: "test",
        providerList: [
          { name: "p1", keyId: "key-1" },
          { name: "p2", keyId: "key-2" },
        ],
      }));

      const data = (await res.json()) as { providers: Array<{ name: string }> };
      expect(data.providers).toHaveLength(2); // key-1 degraded, key-2 healthy
    });
  });

  describe("activity endpoint", () => {
    it("returns providers sorted by lastActivityAt", async () => {
      await tracker.fetch(makeRequest({
        action: "recordSuccess",
        plan: "test",
        provider: "p-old",
        keyId: "key-old",
      }));

      await new Promise((r) => setTimeout(r, 10));

      await tracker.fetch(makeRequest({
        action: "recordSuccess",
        plan: "test",
        provider: "p-new",
        keyId: "key-new",
      }));

      const res = await tracker.fetch(makeActivityRequest());
      const data = (await res.json()) as { providers: Array<{ keyId: string }> };
      expect(data.providers[0].keyId).toBe("key-new");
      expect(data.providers[1].keyId).toBe("key-old");
    });
  });
});
