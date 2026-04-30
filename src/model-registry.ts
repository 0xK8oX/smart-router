/**
 * Smart Router - Model Metadata Registry
 *
 * Populated from OpenRouter API: https://openrouter.ai/api/v1/models
 * Maps model identifiers to their context/output token limits.
 * Providers can override these values in their plan config.
 */

export interface ModelMetadata {
  /** Maximum context window (input + output) in tokens */
  context_length: number;
  /** Maximum output tokens this model can generate */
  max_output_tokens: number;
  /** Human-readable model name */
  display_name?: string;
}

// Registry keyed by common model identifiers used across providers.
// If a model name isn't found here, the provider must specify it explicitly.
export const MODEL_REGISTRY: Record<string, ModelMetadata> = {
  // ---- GLM (Z.ai) ----
  "glm-5.1": {
    context_length: 202_752,
    max_output_tokens: 65_535,
    display_name: "Z.ai GLM 5.1",
  },
  "glm-5-turbo": {
    context_length: 202_752,
    max_output_tokens: 131_072,
    display_name: "Z.ai GLM 5 Turbo",
  },
  "glm-5v-turbo": {
    context_length: 202_752,
    max_output_tokens: 131_072,
    display_name: "Z.ai GLM 5V Turbo",
  },
  "glm-5": {
    context_length: 202_752,
    max_output_tokens: 16_384,
    display_name: "Z.ai GLM 5",
  },
  "glm-4.7": {
    context_length: 202_752,
    max_output_tokens: 16_384, // OpenRouter reports null; conservative default
    display_name: "Z.ai GLM 4.7",
  },
  "glm-4.7-flash": {
    context_length: 202_752,
    max_output_tokens: 16_384,
    display_name: "Z.ai GLM 4.7 Flash",
  },
  "glm-4.6": {
    context_length: 204_800,
    max_output_tokens: 204_800,
    display_name: "Z.ai GLM 4.6",
  },
  "glm-4.6v": {
    context_length: 131_072,
    max_output_tokens: 24_000,
    display_name: "Z.ai GLM 4.6V",
  },

  // ---- Kimi (MoonshotAI) ----
  "kimi-k2.6": {
    context_length: 262_142,
    max_output_tokens: 262_142,
    display_name: "MoonshotAI Kimi K2.6",
  },
  "k2p6": {
    // Alias used by kimi.com coding endpoint
    context_length: 262_142,
    max_output_tokens: 262_142,
    display_name: "MoonshotAI Kimi K2.6",
  },
  "kimi-latest": {
    context_length: 262_142,
    max_output_tokens: 262_142,
    display_name: "MoonshotAI Kimi Latest",
  },
  "kimi-k2.5": {
    context_length: 262_144,
    max_output_tokens: 65_535,
    display_name: "MoonshotAI Kimi K2.5",
  },
  "kimi-k2-thinking": {
    context_length: 262_144,
    max_output_tokens: 262_144,
    display_name: "MoonshotAI Kimi K2 Thinking",
  },
  "kimi-k2-0905": {
    context_length: 262_144,
    max_output_tokens: 262_144,
    display_name: "MoonshotAI Kimi K2 0905",
  },
  "kimi-k2": {
    context_length: 131_072,
    max_output_tokens: 32_768,
    display_name: "MoonshotAI Kimi K2",
  },

  // ---- MiniMax ----
  "minimax2.7": {
    context_length: 128_000,
    max_output_tokens: 32_768,
    display_name: "MiniMax MiniMax-2.7",
  },

  // ---- Volcengine Ark ----
  "ark-code-latest": {
    context_length: 262_144,
    max_output_tokens: 262_144,
    display_name: "Volcengine Ark Code Latest",
  },
};

/**
 * Look up model metadata from the registry.
 * Returns null if the model is unknown — caller must have explicit config then.
 */
export function getModelMetadata(model: string): ModelMetadata | null {
  return MODEL_REGISTRY[model] ?? null;
}

/**
 * Resolve effective token limits for a provider.
 * Plan config values take priority over the registry.
 */
export function resolveTokenLimits(
  model: string,
  planContextLength?: number,
  planMaxOutputTokens?: number
): { context_length: number; max_output_tokens: number } {
  const registry = getModelMetadata(model);

  const context_length = planContextLength ?? registry?.context_length ?? 128_000;
  const max_output_tokens = planMaxOutputTokens ?? registry?.max_output_tokens ?? 4096;

  return { context_length, max_output_tokens };
}
