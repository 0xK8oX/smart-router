/**
 * Smart Router - Core routing logic
 *
 * 1. Get healthy providers from HealthTracker DO
 * 2. Try each provider in priority order
 * 3. Translate request/response between formats
 * 4. On failure: report to DO, try next
 * 5. On success: return translated response
 */

import type { ClientFormat, ProviderConfig, RouterRequest, TranslatedRequest, StatRecord } from "./types";
import { getPlan } from "./config";
import { callProvider } from "./providers/client";
import {
  translateRequestToProvider,
  translateResponseToClient,
} from "./translation";
import { createAnthropicSseToOpenAiTranslator, createOpenAiSseToAnthropicTranslator, consumePassthroughStreamForStats, parseSseEvents, serializeSseEvents } from "./translation/streaming";
import { recordStat, extractUsage, getWeeklyUsage } from "./stats";

/** Global fallback for max output tokens when provider has no specific limit. */
const DEFAULT_MAX_OUTPUT_TOKENS = 65536; // 64K

/** Rough token estimator: counts characters in message contents, divides by 3.
 *  Conservative for mixed English/CJK (Eng ~4 chars/token, CJK ~1 char/token).
 */
function estimateInputTokens(body: unknown): number {
  if (!body || typeof body !== "object") return 0;
  const b = body as Record<string, unknown>;
  const messages = b.messages as Array<Record<string, unknown>> | undefined;
  if (!Array.isArray(messages)) return 0;

  let chars = 0;
  for (const msg of messages) {
    const content = msg.content;
    if (typeof content === "string") {
      chars += content.length;
    } else if (Array.isArray(content)) {
      for (const block of content) {
        if (block && typeof block === "object" && typeof (block as Record<string, unknown>).text === "string") {
          chars += ((block as Record<string, unknown>).text as string).length;
        }
      }
    }
  }
  // Add overhead for message structure (~4 tokens per message)
  return Math.ceil(chars / 3) + messages.length * 4;
}

function classifyFailureForLog(status: number, message: string): string {
  const msg = message.toLowerCase();
  if (status === 402 || msg.includes("quota") || msg.includes("credit") || msg.includes("billing")) return "quota";
  if (status === 429 || msg.includes("rate limit") || msg.includes("too many requests")) return "rate_limit";
  if (status === 504 || msg.includes("timeout")) return "timeout";
  if (status >= 500 && status < 600) return "server_error";
  if (msg.includes("connection") || msg.includes("refused")) return "connection";
  return "unknown";
}

function createSseTransformStream(
  providerFormat: ClientFormat,
  clientFormat: ClientFormat
): TransformStream<Uint8Array, Uint8Array> {
  let buffer = "";

  const transform = providerFormat === "anthropic" && clientFormat === "openai"
    ? createAnthropicSseToOpenAiTranslator()
    : providerFormat === "openai" && clientFormat === "anthropic"
    ? createOpenAiSseToAnthropicTranslator()
    : null;

  return new TransformStream({
    transform(chunk, controller) {
      buffer += new TextDecoder().decode(chunk, { stream: true });

      // Process complete lines (events may span chunks)
      let lastIndex = 0;
      for (let i = 0; i < buffer.length - 1; i++) {
        if (buffer[i] === "\n" && buffer[i + 1] === "\n") {
          const eventText = buffer.slice(lastIndex, i + 2);
          lastIndex = i + 2;

          if (transform) {
            const events = parseSseEvents(eventText);
            const translated = transform(events);
            const output = serializeSseEvents(translated);
            controller.enqueue(new TextEncoder().encode(output));
          } else {
            controller.enqueue(new TextEncoder().encode(eventText));
          }
        }
      }

      // Keep incomplete data in buffer
      buffer = buffer.slice(lastIndex);
    },

    flush(controller) {
      if (buffer.length > 0) {
        if (transform) {
          const events = parseSseEvents(buffer);
          const translated = transform(events);
          const output = serializeSseEvents(translated);
          controller.enqueue(new TextEncoder().encode(output));
        } else {
          controller.enqueue(new TextEncoder().encode(buffer));
        }
        buffer = "";
      }
    },
  });
}

async function getHealthyProviders(
  env: Env,
  plan: string,
  providerList: ProviderConfig[]
): Promise<ProviderConfig[]> {
  const id = env.HEALTH_TRACKER.idFromName("global");
  const stub = env.HEALTH_TRACKER.get(id);

  const res = await stub.fetch("https://fake-host/health", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      action: "getHealthyProviders",
      plan,
      providerList: providerList.map((p) => ({
        name: p.name,
        keyId: p.masked_key ?? p.name,
      })),
    }),
  });

  if (!res.ok) {
    // If DO fails, return all providers (fail open)
    return providerList;
  }

  const data = (await res.json()) as { providers: Array<{ name: string; keyId?: string }> };
  const healthyKeyIds = new Set(data.providers.map((p) => p.keyId ?? p.name));
  return providerList.filter((p) => healthyKeyIds.has(p.masked_key ?? p.name));
}

async function reportFailure(
  env: Env,
  plan: string,
  provider: string,
  keyId: string,
  status: number,
  message: string
): Promise<void> {
  const id = env.HEALTH_TRACKER.idFromName("global");
  const stub = env.HEALTH_TRACKER.get(id);

  try {
    await stub.fetch("https://fake-host/health", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        action: "recordFailure",
        plan,
        provider,
        keyId,
        status,
        message,
      }),
    });
  } catch {
    // Best-effort; don't fail the request if DO is unreachable
  }
}

async function reportSuccess(
  env: Env,
  plan: string,
  provider: string,
  keyId: string
): Promise<void> {
  const id = env.HEALTH_TRACKER.idFromName("global");
  const stub = env.HEALTH_TRACKER.get(id);

  try {
    await stub.fetch("https://fake-host/health", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        action: "recordSuccess",
        plan,
        provider,
        keyId,
      }),
    });
  } catch {
    // Best-effort
  }
}

export async function routeRequest(
  req: RouterRequest,
  env: Env,
  ctx: ExecutionContext
): Promise<Response> {
  const startTime = Date.now();

  // Parse model hints like "auto-jason" -> plan="jason", model="auto"
  let effectivePlan = req.plan;
  if (req.body && typeof req.body === "object" && typeof (req.body as Record<string, unknown>).model === "string") {
    const model = (req.body as Record<string, unknown>).model as string;
    if (model.startsWith("auto-")) {
      effectivePlan = model.slice(5);
      req.body = { ...req.body, model: "auto" };
    }
  }

  const planConfig = await getPlan(env, effectivePlan);
  if (!planConfig || planConfig.providers.length === 0) {
    return new Response(
      JSON.stringify({ error: `Plan "${req.plan}" not found or empty` }),
      { status: 404, headers: { "Content-Type": "application/json" } }
    );
  }

  // If model name matches a provider name, target that provider directly
  // e.g. model="jason-kimi-debbie" -> use only that provider with its configured model
  let targetProvider: ProviderConfig | undefined;
  if (req.body && typeof req.body === "object" && typeof (req.body as Record<string, unknown>).model === "string") {
    const model = (req.body as Record<string, unknown>).model as string;
    if (model !== "auto" && !model.startsWith("auto-")) {
      targetProvider = planConfig.providers.find((p) => p.name === model);
      if (targetProvider) {
        req.body = { ...req.body, model: targetProvider.model };
        console.log(`[ROUTER] TARGET_PROVIDER: plan=${effectivePlan} provider=${targetProvider.name} model_override=${targetProvider.model}`);
      }
    }
  }

  // 1a. Filter out providers that have exceeded their weekly quota
  const quotaErrors: Array<{ provider: string; status: number; message: string }> = [];
  const providersAfterQuota: typeof planConfig.providers = [];

  for (const provider of planConfig.providers) {
    const tokenLimit = provider.weekly_token_limit;
    const requestLimit = provider.weekly_request_limit;
    if (!tokenLimit && !requestLimit) {
      providersAfterQuota.push(provider);
      continue;
    }

    const usage = await getWeeklyUsage(env.DB, provider.masked_key ?? provider.name);

    const overToken = tokenLimit && usage.request_tokens + usage.response_tokens >= tokenLimit;
    const overRequest = requestLimit && usage.request_count >= requestLimit;

    if (overToken || overRequest) {
      const reason = overToken ? `weekly_token_limit ${tokenLimit}` : `weekly_request_limit ${requestLimit}`;
      console.log(`[ROUTER] QUOTA_EXCEEDED: plan=${effectivePlan} provider=${provider.name} ${reason} used=${usage.request_tokens + usage.response_tokens} tokens/${usage.request_count} requests`);
      fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "empty" });
      await reportFailure(env, effectivePlan, provider.name, provider.masked_key ?? provider.name, 0, `quota_exceeded:${reason}`);
      quotaErrors.push({ provider: provider.name, status: 0, message: `Quota exceeded (${reason})` });
      continue;
    }

    providersAfterQuota.push(provider);
  }

  // 1. Get healthy providers (from the quota-filtered list)
  const healthyProviders = targetProvider
    ? [targetProvider]
    : await getHealthyProviders(env, effectivePlan, providersAfterQuota);
  if (healthyProviders.length === 0) {
    return new Response(
      JSON.stringify({ error: "All providers in cooldown" }),
      { status: 503, headers: { "Content-Type": "application/json" } }
    );
  }

  const errors: Array<{ provider: string; status: number; message: string }> = [];

  // Helper to fire stats without blocking the response
  function fireStat(partial: Partial<StatRecord> & { provider: string; model: string }): void {
    const stat: StatRecord = {
      plan: effectivePlan,
      provider: partial.provider,
      model: partial.model,
      key_mask: partial.key_mask,
      request_tokens: partial.request_tokens ?? 0,
      response_tokens: partial.response_tokens ?? 0,
      total_tokens: (partial.request_tokens ?? 0) + (partial.response_tokens ?? 0),
      status: partial.status ?? "failure",
      latency_ms: Date.now() - startTime,
      is_streaming: req.isStreaming,
    };
    ctx.waitUntil(recordStat(env.DB, stat));
  }

  // 2. Try each healthy provider
  for (const provider of healthyProviders) {
    const apiKey = provider.api_key;
    if (!apiKey) {
      console.log(`[ROUTER] MISSING_API_KEY: plan=${effectivePlan} provider=${provider.name}`);
      fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "failure" });
      errors.push({ provider: provider.name, status: 0, message: "Missing API key" });
      continue;
    }

    // Translate request to provider's native format
    let translatedReq: TranslatedRequest;
    try {
      translatedReq = translateRequestToProvider(req.body, req.clientFormat, provider.format, provider.model);
    } catch (err) {
      fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "failure" });
      errors.push({
        provider: provider.name,
        status: 0,
        message: `Translation error: ${err instanceof Error ? err.message : String(err)}`,
      });
      continue;
    }

    // Check context length limit
    if (provider.context_length) {
      const estimatedTokens = estimateInputTokens(req.body);
      if (estimatedTokens > provider.context_length) {
        console.log(`[ROUTER] CONTEXT_EXCEEDED: plan=${effectivePlan} provider=${provider.name} estimated=${estimatedTokens} limit=${provider.context_length}`);
        fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "failure" });
        errors.push({ provider: provider.name, status: 0, message: `Context exceeded: ${estimatedTokens} > ${provider.context_length}` });
        continue;
      }
    }

    // Inject max_tokens cap to prevent output truncation
    let requestBody = translatedReq.body;
    const effectiveMaxOutput = provider.max_output_tokens ?? DEFAULT_MAX_OUTPUT_TOKENS;
    const parsed = JSON.parse(requestBody);
    const clientMax = typeof parsed.max_tokens === "number" ? parsed.max_tokens : undefined;
    if (clientMax === undefined || clientMax > effectiveMaxOutput) {
      parsed.max_tokens = effectiveMaxOutput;
      requestBody = JSON.stringify(parsed);
    }

    // Inject stream_options for OpenAI providers so usage is included in streaming responses
    if (req.isStreaming && provider.format === "openai") {
      const parsed = JSON.parse(requestBody);
      parsed.stream_options = { ...(parsed.stream_options || {}), include_usage: true };
      requestBody = JSON.stringify(parsed);
    }

    let providerRes: Response;
    try {
      providerRes = await callProvider(
        provider,
        apiKey,
        requestBody,
        req.isStreaming
      );
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      console.log(`[ROUTER] PROVIDER_EXCEPTION: plan=${effectivePlan} provider=${provider.name} error="${message}"`);
      fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "failure" });
      await reportFailure(env, effectivePlan, provider.name, provider.masked_key ?? provider.name, 0, message);
      errors.push({ provider: provider.name, status: 0, message });
      continue;
    }

    // Check for HTTP errors
    if (!providerRes.ok) {
      let message = `HTTP ${providerRes.status}`;
      try {
        const body = await providerRes.text();
        message = body.substring(0, 500);
      } catch {
        // ignore
      }
      const failureType = classifyFailureForLog(providerRes.status, message);
      console.log(`[ROUTER] PROVIDER_HTTP_ERROR: plan=${effectivePlan} provider=${provider.name} status=${providerRes.status} type=${failureType} message="${message.replace(/\n/g, ' ')}"`);
      fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "failure" });
      await reportFailure(env, effectivePlan, provider.name, provider.masked_key ?? provider.name, providerRes.status, message);
      errors.push({ provider: provider.name, status: providerRes.status, message });
      continue;
    }

    // Success! Report it and return translated response
    await reportSuccess(env, effectivePlan, provider.name, provider.masked_key ?? provider.name);
    console.log(`[ROUTER] SUCCESS: plan=${effectivePlan} provider=${provider.name} format=${provider.format} client=${req.clientFormat}`);

    // For streaming with matching formats: passthrough
    if (req.isStreaming && req.clientFormat === provider.format) {
      if (!providerRes.body) {
        return new Response("Provider returned empty body", { status: 502 });
      }
      const [clientStream, statsStream] = providerRes.body.tee();
      ctx.waitUntil(consumePassthroughStreamForStats(
        statsStream,
        provider.format,
        (inputTokens, outputTokens) => {
          recordStat(env.DB, {
            plan: effectivePlan,
            provider: provider.name,
            model: provider.model,
            key_mask: provider.masked_key,
            request_tokens: inputTokens,
            response_tokens: outputTokens,
            total_tokens: inputTokens + outputTokens,
            status: "success",
            latency_ms: Date.now() - startTime,
            is_streaming: req.isStreaming,
          });
        }
      ));
      return new Response(clientStream, {
        status: 200,
        headers: {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          Connection: "keep-alive",
        },
      });
    }

    // For streaming with mismatched formats: real-time SSE translation
    if (req.isStreaming && req.clientFormat !== provider.format) {
      if (!providerRes.body) {
        return new Response("Provider returned empty body", { status: 502 });
      }
      const [rawForClient, rawForStats] = providerRes.body.tee();
      // Transform handles format translation only
      const transform = createSseTransformStream(provider.format, req.clientFormat);
      const clientStream = rawForClient.pipeThrough(transform);
      // Stats are recorded from the independent stats branch — guaranteed even if client aborts
      ctx.waitUntil(consumePassthroughStreamForStats(
        rawForStats,
        provider.format,
        (inputTokens, outputTokens) => {
          recordStat(env.DB, {
            plan: effectivePlan,
            provider: provider.name,
            model: provider.model,
            key_mask: provider.masked_key,
            request_tokens: inputTokens,
            response_tokens: outputTokens,
            total_tokens: inputTokens + outputTokens,
            status: "success",
            latency_ms: Date.now() - startTime,
            is_streaming: req.isStreaming,
          });
        }
      ));
      return new Response(clientStream, {
        status: 200,
        headers: {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          Connection: "keep-alive",
        },
      });
    }

    // Non-streaming: translate and return
    const responseBody = await providerRes.text();
    let parsedBody: unknown;
    try {
      parsedBody = JSON.parse(responseBody);
    } catch {
      parsedBody = { content: responseBody };
    }

    const clientResponse = translateResponseToClient(
      parsedBody,
      provider.format,
      req.clientFormat
    ) as Record<string, unknown>;

    // Validate response has content; empty responses are treated as failures
    let hasContent = false;
    let hasToolCalls = false;
    let hasReasoning = false;
    let emptyReason = "empty_content";

    if (req.clientFormat === "anthropic") {
      const contentBlocks = clientResponse.content as Array<Record<string, unknown>> | undefined;
      if (Array.isArray(contentBlocks) && contentBlocks.length > 0) {
        for (const block of contentBlocks) {
          if (block.type === "text" && typeof block.text === "string" && block.text.length > 0) {
            hasContent = true;
          }
          if (block.type === "tool_use") {
            hasToolCalls = true;
          }
          if (block.type === "thinking" && typeof block.thinking === "string" && block.thinking.length > 0) {
            hasReasoning = true;
          }
        }
      }
      if (!contentBlocks || contentBlocks.length === 0) {
        emptyReason = "null_content";
      }
    } else {
      const choices = clientResponse.choices as Array<Record<string, unknown>> | undefined;
      const message = choices?.[0]?.message as Record<string, unknown> | undefined;
      const content = message?.content as string | null | undefined;
      const toolCalls = message?.tool_calls as unknown[] | undefined;
      hasContent = typeof content === "string" && content.length > 0;
      hasToolCalls = Array.isArray(toolCalls) && toolCalls.length > 0;
      hasReasoning = !!message?.reasoning;
      if (content === null || content === undefined) {
        emptyReason = "null_content";
      }
    }

    if (!hasContent && !hasToolCalls && !hasReasoning) {
      console.log(`[ROUTER] EMPTY_RESPONSE: plan=${effectivePlan} provider=${provider.name} reason=${emptyReason} body_preview=${responseBody.substring(0, 200)}`);
      fireStat({ provider: provider.name, model: provider.model, key_mask: provider.masked_key, status: "empty" });
      await reportFailure(env, effectivePlan, provider.name, provider.masked_key ?? provider.name, 200, `empty_response:${emptyReason}`);
      errors.push({ provider: provider.name, status: 200, message: `Empty response (${emptyReason})` });
      continue;
    }

    const usage = extractUsage(parsedBody);
    fireStat({
      provider: provider.name,
      model: provider.model,
      key_mask: provider.masked_key,
      request_tokens: usage?.request_tokens ?? 0,
      response_tokens: usage?.response_tokens ?? 0,
      status: "success",
    });
    return new Response(JSON.stringify(clientResponse), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }

  // All providers failed (combine routing errors + quota errors)
  const allErrors = [...quotaErrors, ...errors];
  return new Response(
    JSON.stringify({
      error: "All providers failed",
      details: allErrors,
    }),
    { status: 503, headers: { "Content-Type": "application/json" } }
  );
}
