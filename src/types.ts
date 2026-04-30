/**
 * Smart Router - Shared TypeScript types
 */

export interface ProviderConfig {
  name: string;
  base_url: string;
  model: string;
  format: "openai" | "anthropic";
  timeout: number;
  api_key?: string;
  masked_key?: string;
  weekly_token_limit?: number;
  weekly_request_limit?: number;
  /** Optional override for context_length from model registry. If omitted, looked up from registry. */
  context_length?: number;
  /** Optional override for max_output_tokens. If omitted, falls back to registry then 4096. */
  max_output_tokens?: number;
}

export interface PlanConfig {
  providers: ProviderConfig[];
}

export interface PlansConfig {
  plans: Record<string, PlanConfig>;
}

export interface ProviderHealth {
  status: "healthy" | "degraded" | "unhealthy";
  consecutiveFailures: number;
  lastFailureAt: number;
  cooldownUntil: number;
  lastFailureReason: string;
  lastSuccessAt: number;
  totalRequests: number;
  successCount: number;
  lastActivityAt: number;
}

export type ClientFormat = "openai" | "anthropic";

export interface RouterRequest {
  body: unknown;
  clientFormat: ClientFormat;
  plan: string;
  isStreaming: boolean;
}

export interface TranslatedRequest {
  url: string;
  headers: Record<string, string>;
  body: string;
  providerFormat: ClientFormat;
}

export interface StatRecord {
  plan: string;
  provider: string;
  model: string;
  key_mask?: string;
  request_tokens: number;
  response_tokens: number;
  total_tokens: number;
  status: "success" | "failure" | "empty" | "timeout";
  latency_ms: number;
  is_streaming: boolean;
}

export interface AggregateOptions {
  groupBy: "plan" | "provider" | "model" | "key_mask";
  from?: number;
  to?: number;
  limit?: number;
}

export interface AggregateResult {
  dimension: string;
  requests: number;
  request_tokens: number;
  response_tokens: number;
  total_tokens: number;
  avg_latency_ms: number;
}
