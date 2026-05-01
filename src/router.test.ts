import { describe, it, expect, vi, beforeEach } from "vitest";
import { routeRequest } from "./router";
import type { RouterRequest } from "./types";

// Mock dependencies
vi.mock("./config", () => ({
  getPlan: vi.fn(),
}));

vi.mock("./providers/client", () => ({
  callProvider: vi.fn(),
}));

vi.mock("./stats", () => ({
  recordStat: vi.fn(),
  extractUsage: vi.fn(() => null),
}));

import { getPlan } from "./config";
import { callProvider } from "./providers/client";
import { recordStat } from "./stats";

function makeEnv(overrides?: Partial<Env>): Env {
  const healthTracker = {
    idFromName: vi.fn(() => ({ toString: () => "id" })),
    get: vi.fn(() => ({
      fetch: vi.fn(() =>
        Promise.resolve(new Response("error", { status: 500 }))
      ),
    })),
  };
  return {
    DB: {} as D1Database,
    HEALTH_TRACKER: healthTracker as unknown as Env["HEALTH_TRACKER"],
    ...overrides,
  } as Env;
}

function makeCtx(): ExecutionContext {
  return {
    waitUntil: vi.fn((p: Promise<unknown>) => p),
    passThroughOnException: vi.fn(),
  } as unknown as ExecutionContext;
}

function makeSseResponse(chunks: string[]): Response {
  return new Response(
    new ReadableStream({
      start(controller) {
        for (const chunk of chunks) {
          controller.enqueue(new TextEncoder().encode(chunk));
        }
        controller.close();
      },
    }),
    { headers: { "Content-Type": "text/event-stream" } }
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("routeRequest streaming", () => {
  it("injects stream_options.include_usage for OpenAI-format providers", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "openai-provider",
        base_url: "http://localhost:23000/v1",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(makeSseResponse([
      'data: {"choices":[{"delta":{"content":"hi"}}]}\n\n',
      'data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}\n\n',
      'data: [DONE]\n\n',
    ]));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], stream: true },
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: true,
    };

    await routeRequest(req, makeEnv(), makeCtx());

    const callBody = vi.mocked(callProvider).mock.calls[0][2];
    const parsed = JSON.parse(callBody);
    expect(parsed.stream_options).toEqual({ include_usage: true });
  });

  it("does NOT inject stream_options for Anthropic-format providers", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "kimi-provider",
        base_url: "https://api.kimi.com/coding/",
        model: "kimi-k2.6",
        format: "anthropic",
        timeout: 30,
        api_key: "test-key",
        masked_key: "test...key",
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(makeSseResponse([
      'event: content_block_delta\ndata: {"delta":{"type":"text_delta","text":"hi"}}\n\n',
      'event: message_delta\ndata: {"usage":{"input_tokens":4,"output_tokens":2},"stop_reason":"end_turn"}\n\n',
      'event: message_stop\ndata: {}\n\n',
    ]));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], stream: true },
      clientFormat: "anthropic",
      plan: "test-plan",
      isStreaming: true,
    };

    await routeRequest(req, makeEnv(), makeCtx());

    const callBody = vi.mocked(callProvider).mock.calls[0][2];
    const parsed = JSON.parse(callBody);
    expect(parsed.stream_options).toBeUndefined();
  });

  it("passthrough streaming records correct usage for Anthropic format", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "kimi-provider",
        base_url: "https://api.kimi.com/coding/",
        model: "kimi-k2.6",
        format: "anthropic",
        timeout: 30,
        api_key: "test-key",
        masked_key: "test...key",
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(makeSseResponse([
      'event: content_block_delta\ndata: {"delta":{"type":"text_delta","text":"Hello"}}\n\n',
      'event: message_delta\ndata: {"usage":{"input_tokens":10,"output_tokens":5},"stop_reason":"end_turn"}\n\n',
      'event: message_stop\ndata: {}\n\n',
    ]));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], stream: true },
      clientFormat: "anthropic",
      plan: "test-plan",
      isStreaming: true,
    };

    const ctx = makeCtx();
    await routeRequest(req, makeEnv(), ctx);

    // Wait for ctx.waitUntil promises to resolve
    await Promise.all((ctx.waitUntil as ReturnType<typeof vi.fn>).mock.calls.map((c: unknown[]) => c[0]));

    const statCall = [...vi.mocked(recordStat).mock.calls].reverse().find(
      (c) => (c[1] as { status: string }).status === "success"
    );
    expect(statCall).toBeDefined();
    expect(statCall![1]).toMatchObject({
      request_tokens: 10,
      response_tokens: 5,
      status: "success",
    });
  });

  it("passthrough streaming records correct usage for OpenAI format", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "openai-provider",
        base_url: "http://localhost:23000/v1",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(makeSseResponse([
      'data: {"choices":[{"delta":{"content":"Hello"}}]}\n\n',
      'data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":8}}\n\n',
      'data: [DONE]\n\n',
    ]));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], stream: true },
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: true,
    };

    const ctx = makeCtx();
    await routeRequest(req, makeEnv(), ctx);

    await Promise.all((ctx.waitUntil as ReturnType<typeof vi.fn>).mock.calls.map((c: unknown[]) => c[0]));

    const statCall = [...vi.mocked(recordStat).mock.calls].reverse().find(
      (c) => (c[1] as { status: string }).status === "success"
    );
    expect(statCall).toBeDefined();
    expect(statCall![1]).toMatchObject({
      request_tokens: 20,
      response_tokens: 8,
      status: "success",
    });
  });

  it("translated streaming Anthropic→OpenAI records correct usage", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "kimi-provider",
        base_url: "https://api.kimi.com/coding/",
        model: "kimi-k2.6",
        format: "anthropic",
        timeout: 30,
        api_key: "test-key",
        masked_key: "test...key",
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(makeSseResponse([
      'event: content_block_delta\ndata: {"delta":{"type":"text_delta","text":"Hello"}}\n\n',
      'event: message_delta\ndata: {"usage":{"input_tokens":15,"output_tokens":7},"stop_reason":"end_turn"}\n\n',
      'event: message_stop\ndata: {}\n\n',
    ]));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], stream: true },
      clientFormat: "openai",  // Different from provider format
      plan: "test-plan",
      isStreaming: true,
    };

    const ctx = makeCtx();
    const res = await routeRequest(req, makeEnv(), ctx);
    // Consume the entire stream body to trigger the transform (which fires onUsage → ctx.waitUntil)
    const reader = res.body!.getReader();
    while (true) {
      const { done } = await reader.read();
      if (done) break;
    }

    await Promise.all((ctx.waitUntil as ReturnType<typeof vi.fn>).mock.calls.map((c: unknown[]) => c[0]));

    const statCall = [...vi.mocked(recordStat).mock.calls].reverse().find(
      (c) => (c[1] as { status: string }).status === "success"
    );
    expect(statCall).toBeDefined();
    expect(statCall![1]).toMatchObject({
      request_tokens: 15,
      response_tokens: 7,
      status: "success",
    });
  });

  it("translated streaming OpenAI→Anthropic records correct usage", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "openai-provider",
        base_url: "http://localhost:23000/v1",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(makeSseResponse([
      'data: {"choices":[{"delta":{"content":"Hello"}}]}\n\n',
      'data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":6}}\n\n',
      'data: [DONE]\n\n',
    ]));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], stream: true },
      clientFormat: "anthropic",  // Different from provider format
      plan: "test-plan",
      isStreaming: true,
    };

    const ctx = makeCtx();
    const res = await routeRequest(req, makeEnv(), ctx);
    // Consume the entire stream body to trigger the transform (which fires onUsage → ctx.waitUntil)
    const reader = res.body!.getReader();
    while (true) {
      const { done } = await reader.read();
      if (done) break;
    }

    await Promise.all((ctx.waitUntil as ReturnType<typeof vi.fn>).mock.calls.map((c: unknown[]) => c[0]));

    const statCall = [...vi.mocked(recordStat).mock.calls].reverse().find(
      (c) => (c[1] as { status: string }).status === "success"
    );
    expect(statCall).toBeDefined();
    expect(statCall![1]).toMatchObject({
      request_tokens: 12,
      response_tokens: 6,
      status: "success",
    });
  });
});

describe("routeRequest context length and max_tokens", () => {
  it("skips provider when estimated input exceeds context_length", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "limited-provider",
        base_url: "https://api.example.com",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
        context_length: 10, // Very small limit
      }],
    });

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "this is a long message that definitely exceeds ten tokens" }] },
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: false,
    };

    const res = await routeRequest(req, makeEnv(), makeCtx());
    expect(res.status).toBe(503);
    const body = await res.json() as Record<string, unknown>;
    expect((body.details as Array<Record<string, unknown>>)[0].message).toMatch(/Context exceeded/);
  });

  it("caps max_tokens to provider max_output_tokens when client sends higher", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "capped-provider",
        base_url: "https://api.example.com",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
        max_output_tokens: 2048,
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(new Response(JSON.stringify({
      choices: [{ message: { role: "assistant", content: "hi" } }],
      usage: { prompt_tokens: 5, completion_tokens: 2 },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], max_tokens: 8000 },
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: false,
    };

    await routeRequest(req, makeEnv(), makeCtx());

    const callBody = vi.mocked(callProvider).mock.calls[0][2];
    const parsed = JSON.parse(callBody);
    expect(parsed.max_tokens).toBe(2048);
  });

  it("sets max_tokens when client omits it and provider has max_output_tokens", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "default-cap-provider",
        base_url: "https://api.example.com",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
        max_output_tokens: 4096,
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(new Response(JSON.stringify({
      choices: [{ message: { role: "assistant", content: "hi" } }],
      usage: { prompt_tokens: 5, completion_tokens: 2 },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }] }, // no max_tokens
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: false,
    };

    await routeRequest(req, makeEnv(), makeCtx());

    const callBody = vi.mocked(callProvider).mock.calls[0][2];
    const parsed = JSON.parse(callBody);
    expect(parsed.max_tokens).toBe(4096);
  });

  it("preserves client max_tokens when provider has no max_output_tokens (below global fallback)", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "unlimited-provider",
        base_url: "https://api.example.com",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
        // no max_output_tokens
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(new Response(JSON.stringify({
      choices: [{ message: { role: "assistant", content: "hi" } }],
      usage: { prompt_tokens: 5, completion_tokens: 2 },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }], max_tokens: 32000 },
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: false,
    };

    await routeRequest(req, makeEnv(), makeCtx());

    const callBody = vi.mocked(callProvider).mock.calls[0][2];
    const parsed = JSON.parse(callBody);
    expect(parsed.max_tokens).toBe(32000);
  });

  it("injects global fallback max_tokens when provider has no max_output_tokens and client omits it", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "fallback-provider",
        base_url: "https://api.example.com",
        model: "gpt-4",
        format: "openai",
        timeout: 30,
        api_key: "sk-test",
        masked_key: "sk...test",
        // no max_output_tokens
      }],
    });

    vi.mocked(callProvider).mockResolvedValue(new Response(JSON.stringify({
      choices: [{ message: { role: "assistant", content: "hi" } }],
      usage: { prompt_tokens: 5, completion_tokens: 2 },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "hi" }] }, // no max_tokens
      clientFormat: "openai",
      plan: "test-plan",
      isStreaming: false,
    };

    await routeRequest(req, makeEnv(), makeCtx());

    const callBody = vi.mocked(callProvider).mock.calls[0][2];
    const parsed = JSON.parse(callBody);
    expect(parsed.max_tokens).toBe(65536);
  });
});

describe("routeRequest non-streaming reasoning", () => {
  it("accepts anthropic response with reasoning/thinking but no text content", async () => {
    vi.mocked(getPlan).mockResolvedValue({
      providers: [{
        name: "minimax-provider",
        base_url: "https://api.minimaxi.com/anthropic",
        model: "minimax2.7",
        format: "anthropic",
        timeout: 60,
        api_key: "test-key",
        masked_key: "test...key",
      }],
    });

    // Minimax returns thinking block, no text block
    vi.mocked(callProvider).mockResolvedValue(new Response(JSON.stringify({
      id: "msg_123",
      type: "message",
      role: "assistant",
      model: "minimax2.7",
      content: [
        { type: "thinking", thinking: "The user just said hi." }
      ],
      stop_reason: "end_turn",
      usage: { input_tokens: 10, output_tokens: 5 }
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    const req: RouterRequest = {
      body: { model: "auto", messages: [{ role: "user", content: "Hi" }] },
      clientFormat: "openai",  // Client is openai format
      plan: "test-plan",
      isStreaming: false,
    };

    const res = await routeRequest(req, makeEnv(), makeCtx());
    
    // Should return 200, not 503 (all providers failed)
    expect(res.status).toBe(200);
    
    const body = await res.json() as Record<string, unknown>;
    // Should have reasoning in the response
    const msg = (body.choices as Array<Record<string, unknown>>)[0].message as Record<string, unknown>;
    expect(msg.reasoning).toBe("The user just said hi.");
    
    // Should record success, not empty
    const statCall = [...vi.mocked(recordStat).mock.calls].reverse().find(
      (c) => (c[1] as { status: string }).status === "success"
    );
    expect(statCall).toBeDefined();
  });
});
