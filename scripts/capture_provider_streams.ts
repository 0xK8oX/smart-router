/**
 * Capture real streaming responses from all configured providers.
 * Run with:
 *   npx tsx scripts/capture_provider_streams.ts
 *
 * Set API keys via env vars (provider name uppercase, hyphens to underscores):
 *   JASON_KIMI_KEY=xxx JASON_VOLCENGINE_KEY=xxx npx tsx scripts/capture_provider_streams.ts
 */

import * as fs from "fs";

interface ProviderConfig {
  name: string;
  base_url: string;
  model: string;
  format: "openai" | "anthropic";
  timeout: number;
}

interface Plan {
  providers: ProviderConfig[];
}

function envKey(providerName: string): string {
  return providerName.toUpperCase().replace(/-/g, "_") + "_KEY";
}

function getApiKey(provider: ProviderConfig): string | undefined {
  return process.env[envKey(provider.name)];
}

function buildEndpoint(baseUrl: string, format: string): string {
  const base = baseUrl.replace(/\/v\d+$/, "");
  if (format === "anthropic") {
    return `${base}/v1/messages`;
  }
  return `${base}/v1/chat/completions`;
}

function makeRequestBody(provider: ProviderConfig): unknown {
  if (provider.format === "anthropic") {
    return {
      model: provider.model,
      max_tokens: 10,
      messages: [{ role: "user", content: "Say hi" }],
      stream: true,
    };
  }
  return {
    model: provider.model,
    max_tokens: 10,
    messages: [{ role: "user", content: "Say hi" }],
    stream: true,
    stream_options: { include_usage: true },
  };
}

function makeHeaders(provider: ProviderConfig, apiKey: string): Record<string, string> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };

  if (provider.base_url.includes("anthropic.com") || provider.base_url.includes("claude-api")) {
    headers["x-api-key"] = apiKey;
    headers["anthropic-version"] = "2023-06-01";
  } else if (provider.base_url.includes("api.kimi.com") && provider.base_url.includes("/coding")) {
    headers["Authorization"] = `Bearer ${apiKey}`;
    headers["User-Agent"] = "claude-code/0.1.0";
  } else {
    headers["Authorization"] = `Bearer ${apiKey}`;
  }

  return headers;
}

async function captureProvider(provider: ProviderConfig): Promise<void> {
  const apiKey = getApiKey(provider);
  if (!apiKey) {
    console.log(`\n=== ${provider.name} === SKIPPED (no ${envKey(provider.name)} env var)\n`);
    return;
  }

  console.log(`\n=== ${provider.name} (${provider.format}) ===`);
  console.log(`  URL: ${buildEndpoint(provider.base_url, provider.format)}`);
  console.log(`  Model: ${provider.model}`);

  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), provider.timeout * 1000);

  try {
    const res = await fetch(buildEndpoint(provider.base_url, provider.format), {
      method: "POST",
      headers: makeHeaders(provider, apiKey),
      body: JSON.stringify(makeRequestBody(provider)),
      signal: controller.signal,
    });

    clearTimeout(timeoutId);

    if (!res.ok) {
      const body = await res.text();
      console.log(`  HTTP ERROR ${res.status}: ${body.slice(0, 500)}`);
      return;
    }

    if (!res.body) {
      console.log("  No response body");
      return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let eventCount = 0;

    console.log("  Events:");

    while (true) {
      const { done, value } = await reader.read();
      if (value) {
        buffer += decoder.decode(value, { stream: !done });
      }

      let lastIndex = 0;
      for (let i = 0; i < buffer.length - 1; i++) {
        if (buffer[i] === "\n" && buffer[i + 1] === "\n") {
          const eventText = buffer.slice(lastIndex, i + 2).trim();
          lastIndex = i + 2;
          eventCount++;

          // Parse the event to show structured output
          const lines = eventText.split("\n");
          const eventLine = lines.find((l) => l.startsWith("event:"));
          const dataLine = lines.find((l) => l.startsWith("data:"));

          const eventName = eventLine ? eventLine.slice(6).trim() : "(no event)";
          let dataPreview = dataLine ? dataLine.slice(5).trim() : "(no data)";

          try {
            const parsed = JSON.parse(dataPreview);
            // Highlight usage events
            if (parsed.usage || (parsed.delta && parsed.delta.usage)) {
              console.log(`    [${eventCount}] event=${eventName} *** USAGE ***`);
              console.log(`         data=${JSON.stringify(parsed, null, 2)}`);
            } else {
              dataPreview = JSON.stringify(parsed);
              if (dataPreview.length > 120) {
                dataPreview = dataPreview.slice(0, 120) + "...";
              }
              console.log(`    [${eventCount}] event=${eventName} data=${dataPreview}`);
            }
          } catch {
            if (dataPreview.length > 120) {
              dataPreview = dataPreview.slice(0, 120) + "...";
            }
            console.log(`    [${eventCount}] event=${eventName} data=${dataPreview}`);
          }
        }
      }
      buffer = buffer.slice(lastIndex);

      if (done) break;
    }

    if (buffer.trim()) {
      eventCount++;
      console.log(`    [${eventCount}] (trailing) ${buffer.trim()}`);
    }

    console.log(`  Total events: ${eventCount}`);
  } catch (err) {
    clearTimeout(timeoutId);
    console.log(`  ERROR: ${err instanceof Error ? err.message : String(err)}`);
  }
}

async function main(): Promise<void> {
  const plansJson = JSON.parse(fs.readFileSync("./plans.json", "utf-8"));
  const providers: ProviderConfig[] = [];

  // Collect unique providers across all plans
  const seen = new Set<string>();
  for (const plan of Object.values(plansJson.plans) as Plan[]) {
    for (const provider of plan.providers) {
      if (!seen.has(provider.name)) {
        seen.add(provider.name);
        providers.push(provider);
      }
    }
  }

  console.log(`Found ${providers.length} unique providers`);
  console.log("Set API keys via env vars:");
  for (const p of providers) {
    const key = getApiKey(p);
    console.log(`  ${envKey(p.name)}=${key ? "***" : "(not set)"}`);
  }

  for (const provider of providers) {
    await captureProvider(provider);
  }
}

main().catch(console.error);
