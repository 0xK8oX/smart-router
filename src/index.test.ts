import { describe, it, expect, vi, beforeEach } from "vitest";
import { handleUpdatePlan } from "./index";
import type { PlanConfig } from "./types";

vi.mock("./db", () => ({
  listPlans: vi.fn(),
}));

vi.mock("./config", () => ({
  upsertPlan: vi.fn(),
}));

vi.mock("./crypto", () => ({
  decryptKey: vi.fn(),
}));

import { listPlans } from "./db";
import { upsertPlan } from "./config";

function makeEnv(): Env {
  return {
    DB: {} as D1Database,
    HEALTH_TRACKER: {} as unknown as Env["HEALTH_TRACKER"],
  } as Env;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("handleUpdatePlan", () => {
  it("preserves existing api_key when update body has no api_key", async () => {
    const existingPlan: PlanConfig = {
      providers: [
        {
          name: "test-provider",
          base_url: "https://api.example.com",
          model: "gpt-4",
          format: "openai",
          timeout: 60,
          api_key: "sk-existing-key-12345",
          masked_key: "sk-e****ey45",
        },
      ],
    };

    vi.mocked(listPlans).mockResolvedValue({
      "test-plan": existingPlan,
    });

    const updateBody = {
      providers: [
        {
          name: "test-provider",
          base_url: "https://api.example.com",
          model: "gpt-4",
          format: "openai",
          timeout: 30,
        },
      ],
    };

    const req = new Request("http://localhost/v1/plans/test-plan", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(updateBody),
    });

    const res = await handleUpdatePlan("test-plan", req, makeEnv());

    expect(res.status).toBe(200);

    const upsertCall = vi.mocked(upsertPlan).mock.calls[0];
    const savedConfig = upsertCall[2] as PlanConfig;

    expect(savedConfig.providers[0].api_key).toBe("sk-existing-key-12345");
  });

  it("overwrites existing api_key when update body includes a new api_key", async () => {
    const existingPlan: PlanConfig = {
      providers: [
        {
          name: "test-provider",
          base_url: "https://api.example.com",
          model: "gpt-4",
          format: "openai",
          timeout: 60,
          api_key: "sk-old-key-abc",
          masked_key: "sk-o****key",
        },
      ],
    };

    vi.mocked(listPlans).mockResolvedValue({
      "test-plan": existingPlan,
    });

    const updateBody = {
      providers: [
        {
          name: "test-provider",
          base_url: "https://api.example.com",
          model: "gpt-4",
          format: "openai",
          timeout: 30,
          api_key: "sk-new-key-xyz",
        },
      ],
    };

    const req = new Request("http://localhost/v1/plans/test-plan", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(updateBody),
    });

    const res = await handleUpdatePlan("test-plan", req, makeEnv());

    expect(res.status).toBe(200);

    const upsertCall = vi.mocked(upsertPlan).mock.calls[0];
    const savedConfig = upsertCall[2] as PlanConfig;

    expect(savedConfig.providers[0].api_key).toBe("sk-new-key-xyz");
  });

  it("adds api_key for new provider when existing plan has no matching provider", async () => {
    const existingPlan: PlanConfig = {
      providers: [
        {
          name: "existing-provider",
          base_url: "https://api.example.com",
          model: "gpt-4",
          format: "openai",
          timeout: 60,
          api_key: "sk-existing",
          masked_key: "sk-ex****ting",
        },
      ],
    };

    vi.mocked(listPlans).mockResolvedValue({
      "test-plan": existingPlan,
    });

    const updateBody = {
      providers: [
        {
          name: "existing-provider",
          base_url: "https://api.example.com",
          model: "gpt-4",
          format: "openai",
          timeout: 30,
        },
        {
          name: "new-provider",
          base_url: "https://api.new.com",
          model: "gpt-4",
          format: "openai",
          timeout: 30,
          api_key: "sk-new-provider-key",
        },
      ],
    };

    const req = new Request("http://localhost/v1/plans/test-plan", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(updateBody),
    });

    const res = await handleUpdatePlan("test-plan", req, makeEnv());

    expect(res.status).toBe(200);

    const upsertCall = vi.mocked(upsertPlan).mock.calls[0];
    const savedConfig = upsertCall[2] as PlanConfig;

    expect(savedConfig.providers[0].api_key).toBe("sk-existing");
    expect(savedConfig.providers[1].api_key).toBe("sk-new-provider-key");
  });

  it("returns 400 when providers array is missing", async () => {
    const req = new Request("http://localhost/v1/plans/test-plan", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });

    const res = await handleUpdatePlan("test-plan", req, makeEnv());

    expect(res.status).toBe(400);
    const body = await res.json() as Record<string, unknown>;
    expect(body.error).toBe("Missing providers array");
  });

  it("returns 400 when body is not valid JSON", async () => {
    const req = new Request("http://localhost/v1/plans/test-plan", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: "not-json",
    });

    const res = await handleUpdatePlan("test-plan", req, makeEnv());

    expect(res.status).toBe(400);
    const body = await res.json() as Record<string, unknown>;
    expect(body.error).toBe("Invalid JSON body");
  });
});