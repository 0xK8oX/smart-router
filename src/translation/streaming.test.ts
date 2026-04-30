import { describe, it, expect } from "vitest";
import { extractUsageFromStream } from "./streaming";

function makeStream(chunks: string[]): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(new TextEncoder().encode(chunk));
      }
      controller.close();
    },
  });
}

describe("extractUsageFromStream", () => {
  it("extracts usage from Anthropic streaming final event", async () => {
    const sse =
      'event: content_block_delta\n' +
      'data: {"delta":{"type":"text_delta","text":"Hello"}}\n\n' +
      'event: message_delta\n' +
      'data: {"usage":{"input_tokens":10,"output_tokens":5},"stop_reason":"end_turn"}\n\n' +
      'event: message_stop\n' +
      'data: {}\n\n';

    const result = await extractUsageFromStream(makeStream([sse]), "anthropic");
    expect(result).toEqual({ request_tokens: 10, response_tokens: 5 });
  });

  it("extracts usage from OpenAI streaming final event", async () => {
    const sse =
      'data: {"choices":[{"delta":{"content":"Hello"}}]}\n\n' +
      'data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":8}}\n\n' +
      'data: [DONE]\n\n';

    const result = await extractUsageFromStream(makeStream([sse]), "openai");
    expect(result).toEqual({ request_tokens: 20, response_tokens: 8 });
  });

  it("returns null when no usage event is found", async () => {
    const sse =
      'event: content_block_delta\n' +
      'data: {"delta":{"type":"text_delta","text":"Hello"}}\n\n' +
      'event: message_stop\n' +
      'data: {}\n\n';

    const result = await extractUsageFromStream(makeStream([sse]), "anthropic");
    expect(result).toBeNull();
  });

  it("handles chunks split across buffer boundaries", async () => {
    const chunks = [
      'event: message_delta\ndata: {"usage":{"input_tok',
      'ens":15,"output_tokens":7},"stop_reason":"end_turn"}\n\n',
    ];

    const result = await extractUsageFromStream(makeStream(chunks), "anthropic");
    expect(result).toEqual({ request_tokens: 15, response_tokens: 7 });
  });
});
