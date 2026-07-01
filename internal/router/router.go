package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/providers"
	"smart-router/internal/tokenizer"
	"smart-router/internal/translation"
	"smart-router/internal/types"
)

// UpstreamError indicates that a provider returned a non-retryable client error
// that should be passed through to the client as-is.
type UpstreamError struct {
	Resp     *http.Response
	Provider types.ProviderConfig
	Body     []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream error from %s: HTTP %d", e.Provider.Name, e.Resp.StatusCode)
}

// isRequestTooLarge detects provider errors where the request exceeds the
// model's context window. These should not mark the provider unhealthy and
// should be returned to the client rather than retried on another provider.
func isRequestTooLarge(status int, body string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(body)
	return strings.Contains(msg, "exceeded model token limit") ||
		strings.Contains(msg, "token limit") ||
		strings.Contains(msg, "context window")
}

type cachedPlan struct {
	plan     *types.PlanConfig
	loadedAt time.Time
}

type Router struct {
	healthTracker  *health.HealthTracker
	db             *db.DB
	client         *providers.Client
	offsets        map[string]uint64
	weightCounters map[string]uint64
	lastUsed       map[string]time.Time
	planCache      map[string]cachedPlan
	cacheTTL       time.Duration
	mu             sync.RWMutex

	// Adaptive routing state.
	inFlight     map[string]*atomic.Int32
	latencyCache map[string]int64
}

func New(tracker *health.HealthTracker, database *db.DB) *Router {
	return &Router{
		healthTracker:  tracker,
		db:             database,
		client:         providers.NewClient(),
		offsets:        make(map[string]uint64),
		weightCounters: make(map[string]uint64),
		lastUsed:       make(map[string]time.Time),
		planCache:      make(map[string]cachedPlan),
		cacheTTL:       30 * time.Second,
		inFlight:       make(map[string]*atomic.Int32),
		latencyCache:   make(map[string]int64),
	}
}

func (r *Router) getPlanCached(planSlug string) (*types.PlanConfig, error) {
	r.mu.RLock()
	entry, ok := r.planCache[planSlug]
	r.mu.RUnlock()
	if ok && time.Since(entry.loadedAt) < r.cacheTTL {
		return entry.plan, nil
	}

	plan, err := r.db.GetPlan(planSlug)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.planCache[planSlug]; ok && time.Since(entry.loadedAt) < r.cacheTTL {
		return entry.plan, nil
	}
	r.planCache[planSlug] = cachedPlan{plan: plan, loadedAt: time.Now()}
	if len(r.planCache) > maxPlanCacheSize {
		var oldest string
		var oldestTime time.Time
		for slug, entry := range r.planCache {
			if oldest == "" || entry.loadedAt.Before(oldestTime) {
				oldest = slug
				oldestTime = entry.loadedAt
			}
		}
		delete(r.planCache, oldest)
	}
	return plan, nil
}

func (r *Router) InvalidatePlanCache(slug string) {
	r.mu.Lock()
	delete(r.planCache, slug)
	r.mu.Unlock()
}

// Client exposes the underlying provider HTTP client for use by other
// subsystems (e.g. the /v1/models endpoint that fans out to providers).
func (r *Router) Client() *providers.Client {
	return r.client
}

func (r *Router) InvalidateAllPlanCache() {
	r.mu.Lock()
	r.planCache = make(map[string]cachedPlan)
	r.mu.Unlock()
}

// Route finds a healthy provider, calls it, and returns the response.
// Steps:
// 1. Load plan from DB (db.GetPlan)
// 2. Iterate through providers in order
// 3. Check health — skip if unhealthy and still in cooldown
// 4. Translate request body to provider format
// 5. Call provider (Call or CallStream)
// 6. If success (2xx): record success in health tracker, record stat in DB, return response
// 7. If failure: read error body, record failure in health tracker, try next provider
// 8. If all fail: return error
const maxVirtualDepth = 3
const smartPrefix = "smart://"
const maxPlanCacheSize = 1000

// defaultContextLength is used when a provider does not specify one.
const defaultContextLength = 128000

// countBlockTokens counts tokens in a single content block.
// Handles Anthropic text/thinking/tool_use/tool_result and OpenAI image_url types.
func countBlockTokens(block map[string]interface{}) int {
	total := 0
	t := ""
	if typ, ok := block["type"].(string); ok {
		t = typ
	}
	switch t {
	case "text", "thinking":
		if text, ok := block[t].(string); ok {
			total += tokenizer.CountString(text)
		}
	case "tool_use":
		// Tool name + input parameters
		if name, ok := block["name"].(string); ok {
			total += tokenizer.CountString(name)
		}
		if input, ok := block["input"]; ok {
			total += tokenizer.CountString(fmt.Sprintf("%v", input))
		}
	case "tool_result":
		if content, ok := block["content"].(string); ok {
			total += tokenizer.CountString(content)
		} else if contentArr, ok := block["content"].([]interface{}); ok {
			for _, c := range contentArr {
				if cm, ok := c.(map[string]interface{}); ok {
					total += countBlockTokens(cm)
				}
			}
		}
	case "image_url":
		if url, ok := block["image_url"].(map[string]interface{}); ok {
			if s, ok := url["url"].(string); ok {
				total += tokenizer.CountString(s) / 4
			}
		}
	case "image":
		if src, ok := block["source"].(map[string]interface{}); ok {
			if s, ok := src["data"].(string); ok {
				total += len(s) / 4
			}
		}
	default:
		// Fallback: stringify the entire block
		total += tokenizer.CountString(fmt.Sprintf("%v", block))
	}
	return total
}

// CountRequestTokens estimates tokens in the request body.
// It counts messages content, system prompts, tools definitions, and adds
// a small per-message overhead. Exported for the count_tokens endpoint.
func CountRequestTokens(body map[string]interface{}) int {
	// Normalize types by round-tripping through JSON so that typed slices
	// (e.g. []map[string]string) become []interface{} for uniform handling.
	var normalized map[string]interface{}
	if b, err := json.Marshal(body); err == nil {
		_ = json.Unmarshal(b, &normalized)
	}
	if normalized == nil {
		normalized = body
	}

	total := 0

	// System messages (Anthropic format: array of blocks or single string)
	if sys, ok := normalized["system"].([]interface{}); ok {
		for _, s := range sys {
			if sm, ok := s.(map[string]interface{}); ok {
				if text, ok := sm["text"].(string); ok {
					total += tokenizer.CountString(text)
				}
			} else if text, ok := s.(string); ok {
				total += tokenizer.CountString(text)
			}
		}
	} else if text, ok := normalized["system"].(string); ok {
		total += tokenizer.CountString(text)
	}

	// Messages array
	if msgs, ok := normalized["messages"].([]interface{}); ok {
		for _, m := range msgs {
			msg, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			// Content can be string (OpenAI) or []blocks (Anthropic)
			switch c := msg["content"].(type) {
			case string:
				total += tokenizer.CountString(c)
			case []interface{}:
				for _, block := range c {
					if bm, ok := block.(map[string]interface{}); ok {
						total += countBlockTokens(bm)
					}
				}
			}
			// Per-message overhead: real Anthropic API adds ~6-8 tokens
			// per message for role/metadata/formatting. Use 8 to be safe.
			total += 8
		}
	}

	// Tools definitions
	if tools, ok := normalized["tools"].([]interface{}); ok {
		for _, t := range tools {
			total += tokenizer.CountString(fmt.Sprintf("%v", t))
			// Per-tool overhead: tool definition metadata
			total += 50
		}
	}

	// System message overhead (system prompt framing)
	if _, hasSystem := normalized["system"]; hasSystem {
		total += 20
	}

	// Max tokens reservation (what the client expects to receive back)
	if maxTok, ok := normalized["max_tokens"].(float64); ok {
		total += int(maxTok)
	} else if maxTok, ok := normalized["max_tokens"].(int); ok {
		total += maxTok
	}

	return total
}

func isVirtualProvider(baseURL string) bool {
	return strings.HasPrefix(baseURL, smartPrefix)
}

func extractPlanFromURL(baseURL string) string {
	return baseURL[len(smartPrefix):]
}

// resolveEndpoint returns a copy of p with BaseURL/Format set to the endpoint
// matching clientFormat when one is declared. This lets a provider that offers
// both an anthropic and an openai API be hit in the client's native format
// (passthrough, no translation). Falls back to p's default BaseURL/Format.
func resolveEndpoint(p types.ProviderConfig, clientFormat string) types.ProviderConfig {
	if p.Endpoints != nil {
		if url, ok := p.Endpoints[clientFormat]; ok && url != "" {
			p.BaseURL = url
			p.Format = clientFormat
		}
	}
	return p
}

// Route finds a healthy provider, calls it, and returns the response.
// getInFlight returns the atomic counter for a provider, lazily initialising if needed.
func (r *Router) getInFlight(name string) *atomic.Int32 {
	r.mu.RLock()
	v, ok := r.inFlight[name]
	r.mu.RUnlock()
	if ok {
		return v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok = r.inFlight[name]; ok {
		return v
	}
	v = new(atomic.Int32)
	r.inFlight[name] = v
	return v
}

// recordLatency updates the EWMA latency cache for a provider.
func (r *Router) recordLatency(provider string, latencyMs int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	const alpha = 0.3
	old := r.latencyCache[provider]
	if old == 0 {
		r.latencyCache[provider] = latencyMs
	} else {
		r.latencyCache[provider] = int64(alpha*float64(latencyMs) + (1-alpha)*float64(old))
	}
}

// getLatency returns the cached EWMA latency for a provider, or a default if unknown.
func (r *Router) getLatency(provider string) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if v := r.latencyCache[provider]; v > 0 {
		return v
	}
	return 10000 // default 10s when unknown
}

// providerScore returns a lower-is-better score for adaptive routing.
// Factors: latency, in-flight ratio, same-model bonus, and format-affinity
// bonus (preferring providers that can serve the request in the client's
// native format — i.e. passthrough — over providers that would require
// format translation).
func (r *Router) providerScore(p types.ProviderConfig, requestedModel, clientFormat string) float64 {
	score := float64(r.getLatency(p.Name))

	// In-flight penalty. Providers with higher max_concurrency can absorb more load.
	inFlight := r.getInFlight(p.Name).Load()
	maxConc := int32(30)
	if p.MaxConcurrency != nil && *p.MaxConcurrency > 0 {
		maxConc = int32(*p.MaxConcurrency)
	}
	if inFlight >= maxConc {
		score *= 1000.0
	} else {
		// Penalty is normalized against max_concurrency so providers
		// with higher capacity don't get unfairly penalized.
		score *= 1.0 + float64(inFlight)/float64(maxConc)
	}

	// Same-model bonus (preserves prompt cache for same-model providers).
	if p.Model == requestedModel {
		score *= 0.7
	}

	// Format-affinity bonus: prefer providers whose effective format matches
	// the client's request format, so the request is forwarded as-is instead
	// of being translated at the edge. When multiple providers share the
	// requested model, this picks the one that avoids translation.
	if clientFormat != "" {
		if resolveEndpoint(p, clientFormat).Format == clientFormat {
			score *= 0.8
		}
	}

	return score
}

// ExtractSource delegates to db.ExtractSource for use in stat records.
func extractSource(headers http.Header) string {
	return db.ExtractSource(headers.Get("User-Agent"))
}

// extractUserAgent returns the raw User-Agent header for stats.
func extractUserAgent(headers http.Header) string {
	return headers.Get("User-Agent")
}

// buildOrderedProviders returns plan.Providers in routing order for the given
// requested model and client format. Adaptive strategy sorts all providers by
// providerScore; other strategies put same-model providers first, then apply
// the configured strategy to the rest. Shared by ResolveProvider (precheck)
// and routeWithDepth (actual call) so the two always agree on order.
func (r *Router) buildOrderedProviders(plan *types.PlanConfig, planSlug, requestedModel, clientFormat string) []types.ProviderConfig {
	if plan.Strategy == "adaptive" {
		ordered := make([]types.ProviderConfig, len(plan.Providers))
		copy(ordered, plan.Providers)
		sort.Slice(ordered, func(i, j int) bool {
			return r.providerScore(ordered[i], requestedModel, clientFormat) <
				r.providerScore(ordered[j], requestedModel, clientFormat)
		})
		return ordered
	}
	var ordered, remaining []types.ProviderConfig
	for _, p := range plan.Providers {
		if p.Model == requestedModel {
			ordered = append(ordered, p)
		} else {
			remaining = append(remaining, p)
		}
	}
	remaining = r.applyStrategy(planSlug, plan.Strategy, remaining)
	return append(ordered, remaining...)
}

// ResolveProvider selects the first healthy provider for the given plan and
// client format, applying endpoint resolution. It does not make an upstream
// call. The caller can inspect eff.Format to decide whether native passthrough
// is available (e.g. eff.Format == "responses" means the provider has a
// declared responses endpoint).
func (r *Router) ResolveProvider(ctx context.Context, planSlug string, body map[string]interface{}, clientFormat string) (types.ProviderConfig, error) {
	plan, err := r.getPlanCached(planSlug)
	if err != nil {
		return types.ProviderConfig{}, fmt.Errorf("load plan: %w", err)
	}
	if plan == nil {
		return types.ProviderConfig{}, fmt.Errorf("plan not found: %s", planSlug)
	}

	requestedModel, _ := body["model"].(string)
	now := time.Now().Unix()

	orderedProviders := r.buildOrderedProviders(plan, planSlug, requestedModel, clientFormat)

	for _, provider := range orderedProviders {
		if isVirtualProvider(provider.BaseURL) {
			continue
		}
		h, err := r.healthTracker.GetHealth(provider.Name)
		if err != nil {
			continue
		}
		if h.Status == "unhealthy" && h.CooldownUntil > now {
			continue
		}
		eff := resolveEndpoint(provider, clientFormat)
		if eff.Format == clientFormat {
			return eff, nil
		}
	}
	return types.ProviderConfig{}, fmt.Errorf("no healthy provider supports format %s for plan %s", clientFormat, planSlug)
}

func (r *Router) Route(ctx context.Context, planSlug string, body map[string]interface{}, isStreaming bool, clientFormat string, headers http.Header, clientKey string) (*http.Response, types.ProviderConfig, error) {
	source := extractSource(headers)
	userAgent := extractUserAgent(headers)
	return r.routeWithDepth(ctx, planSlug, body, isStreaming, clientFormat, headers, 0, clientKey, nil, source, userAgent)
}

func (r *Router) applyStrategy(planSlug string, strategy string, providers []types.ProviderConfig) []types.ProviderConfig {
	if len(providers) <= 1 {
		return providers
	}
	switch strategy {
	case "round_robin":
		r.mu.Lock()
		offset := r.offsets[planSlug]
		r.offsets[planSlug] = offset + 1
		r.mu.Unlock()
		idx := int(offset % uint64(len(providers)))
		return append(append([]types.ProviderConfig{}, providers[idx:]...), providers[:idx]...)
	case "weighted_round_robin":
		totalWeight := 0
		for _, p := range providers {
			w := p.Weight
			if w <= 0 {
				w = 1
			}
			totalWeight += w
		}
		if totalWeight == 0 {
			return providers
		}
		r.mu.Lock()
		counter := r.weightCounters[planSlug]
		r.weightCounters[planSlug] = counter + 1
		r.mu.Unlock()
		target := int(counter % uint64(totalWeight))
		cum := 0
		startIdx := 0
		for i, p := range providers {
			w := p.Weight
			if w <= 0 {
				w = 1
			}
			cum += w
			if target < cum {
				startIdx = i
				break
			}
		}
		return append(append([]types.ProviderConfig{}, providers[startIdx:]...), providers[:startIdx]...)
	case "lru":
		r.mu.RLock()
		sorted := make([]types.ProviderConfig, len(providers))
		copy(sorted, providers)
		snapshot := make(map[string]time.Time, len(r.lastUsed))
		for k, v := range r.lastUsed {
			snapshot[k] = v
		}
		r.mu.RUnlock()
		sort.Slice(sorted, func(i, j int) bool {
			li := snapshot[sorted[i].Name]
			lj := snapshot[sorted[j].Name]
			return li.Before(lj)
		})
		return sorted
	default:
		return providers
	}
}

func (r *Router) routeWithDepth(ctx context.Context, planSlug string, body map[string]interface{}, isStreaming bool, clientFormat string, headers http.Header, depth int, clientKey string, visited map[string]bool, source string, userAgent string) (*http.Response, types.ProviderConfig, error) {
	plan, err := r.getPlanCached(planSlug)
	if err != nil {
		return nil, types.ProviderConfig{}, fmt.Errorf("load plan: %w", err)
	}
	if plan == nil {
		return nil, types.ProviderConfig{}, fmt.Errorf("plan not found: %s", planSlug)
	}

	requestedModel, _ := body["model"].(string)

	var providerErrors []string
	now := time.Now().Unix()

	orderedProviders := r.buildOrderedProviders(plan, planSlug, requestedModel, clientFormat)

	for _, provider := range orderedProviders {
		// Virtual provider: route internally to another plan instead of making an HTTP call.
		if isVirtualProvider(provider.BaseURL) {
			if depth >= maxVirtualDepth {
				providerErrors = append(providerErrors, fmt.Sprintf("%s: max virtual depth exceeded", provider.Name))
				continue
			}
			targetPlan := extractPlanFromURL(provider.BaseURL)
			if visited == nil {
				visited = make(map[string]bool)
			}
			if visited[targetPlan] {
				providerErrors = append(providerErrors, fmt.Sprintf("%s: virtual provider cycle detected at %s", provider.Name, targetPlan))
				continue
			}
			// Virtual providers are not eligible for the too-large check because
			// they delegate to a different plan with its own limits.
			visitedCopy := make(map[string]bool, len(visited)+1)
			for k, v := range visited {
				visitedCopy[k] = v
			}
			visitedCopy[targetPlan] = true
			resp, actualProvider, err := r.routeWithDepth(ctx, targetPlan, body, isStreaming, clientFormat, headers, depth+1, clientKey, visitedCopy, source, userAgent)
			if err != nil {
				providerErrors = append(providerErrors, fmt.Sprintf("%s: virtual redirect to %s failed: %v", provider.Name, targetPlan, err))
				continue
			}
			return resp, actualProvider, nil
		}

		// Check health
		h, err := r.healthTracker.GetHealth(provider.Name)
		if err != nil {
			log.Printf("[ROUTER] health read error for %s: %v; treating as unhealthy", provider.Name, err)
			providerErrors = append(providerErrors, fmt.Sprintf("%s: health read error", provider.Name))
			continue
		}
		if h.Status == "unhealthy" && h.CooldownUntil > now {
			providerErrors = append(providerErrors, fmt.Sprintf("%s: unhealthy (cooldown)", provider.Name))
			continue
		}

		// Override model with provider's configured model
		translatedBody := make(map[string]interface{}, len(body))
		for k, v := range body {
			translatedBody[k] = v
		}
		translatedBody["model"] = provider.Model

		// Resolve effective endpoint for this request format: if the provider
		// declares an endpoint matching clientFormat, use it (passthrough, no
		// translation); otherwise fall back to its default BaseURL/Format.
		eff := resolveEndpoint(provider, clientFormat)

		// Skip providers that can't serve the requested format natively and
		// have no valid translation (only openai<->anthropic is translatable;
		// "responses" requires a declared responses endpoint). Skipping without
		// RecordFailure avoids wrongly marking a healthy provider unhealthy.
		if clientFormat == "responses" && eff.Format != "responses" {
			providerErrors = append(providerErrors, fmt.Sprintf("%s: no responses endpoint", provider.Name))
			continue
		}

		// Translate request
		translatedBody, err = translation.TranslateRequest(translatedBody, clientFormat, eff.Format)
		if err != nil {
			// Translation error is fatal for this provider, try next
			_ = r.healthTracker.RecordFailure(provider.Name, 0, err.Error())
			r.db.RecordStatAsync(types.StatRecord{
				Plan:        planSlug,
				Provider:    provider.Name,
				Model:       provider.Model,
				KeyMask:     types.MaskAPIKey(provider.APIKey),
				ClientKey:   types.MaskAPIKey(clientKey),
				Source:      source,
				UserAgent:   userAgent,
				Status:      "failure",
				StatusCode:  0,
				ErrorReason: health.ClassifyFailure(0, err.Error()),
			})
			providerErrors = append(providerErrors, fmt.Sprintf("%s: translate error", provider.Name))
			continue
		}

		// Track in-flight requests for adaptive routing.
		r.getInFlight(provider.Name).Add(1)
		start := time.Now()

		var resp *http.Response
		var fwdHeaders http.Header
		if clientFormat == eff.Format {
			fwdHeaders = headers
		} else if headers != nil {
			// Forward tracing headers even when translating formats
			fwdHeaders = make(http.Header)
			for _, h := range []string{
				"X-Request-ID", "X-Trace-ID", "X-Span-ID",
				"X-Forwarded-For", "X-Real-IP",
			} {
				if v := headers.Get(h); v != "" {
					fwdHeaders.Set(h, v)
				}
			}
		}
		if isStreaming {
			resp, err = r.client.CallStream(ctx, eff, translatedBody, fwdHeaders)
		} else {
			resp, err = r.client.Call(ctx, eff, translatedBody, fwdHeaders)
		}
		latencyMs := time.Since(start).Milliseconds()
		r.getInFlight(provider.Name).Add(-1)
		r.recordLatency(provider.Name, latencyMs)

		if err != nil {
			// Network / timeout error
			log.Printf("[ROUTER] FAILURE: plan=%s provider=%s error=%v latency=%dms", planSlug, provider.Name, err, latencyMs)
			_ = r.healthTracker.RecordFailure(provider.Name, 0, err.Error())
			r.db.RecordStatAsync(types.StatRecord{
				Plan:        planSlug,
				Provider:    provider.Name,
				Model:       provider.Model,
				KeyMask:     types.MaskAPIKey(provider.APIKey),
				ClientKey:   types.MaskAPIKey(clientKey),
				Source:      source,
				UserAgent:   userAgent,
				Status:      "failure",
				StatusCode:  0,
				ErrorReason: health.ClassifyFailure(0, err.Error()),
				LatencyMs:   latencyMs,
				IsStreaming: isStreaming,
			})
			providerErrors = append(providerErrors, fmt.Sprintf("%s: %v", provider.Name, err))
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success — health tracker updated; stat with token counts recorded by handler.
			_ = r.healthTracker.RecordSuccess(provider.Name)
			r.mu.Lock()
			r.lastUsed[provider.Name] = time.Now()
			r.mu.Unlock()
			// Return the effective provider (carrying the resolved Format/BaseURL),
			// not the declared one, so handlers decide response translation based on
			// the format that was actually used upstream.
			return resp, eff, nil
		}

		// Failure: read body (bounded), record failure, try next
		var errBody string
		if resp.Body != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			errBody = string(b)
			resp.Body.Close()
		}

		latencyMs = time.Since(start).Milliseconds()
		log.Printf("[ROUTER] FAILURE: plan=%s provider=%s status=%d latency=%dms body=%s", planSlug, provider.Name, resp.StatusCode, latencyMs, errBody)

		// Token-limit / context-window errors mean the request is too large for
		// this model family. Don't mark the provider unhealthy; return the
		// upstream response to the client so it can compact or shorten input.
		if isRequestTooLarge(resp.StatusCode, errBody) {
			resp.Body = io.NopCloser(strings.NewReader(errBody))
			return nil, types.ProviderConfig{}, &UpstreamError{Resp: resp, Provider: provider, Body: []byte(errBody)}
		}

		_ = r.healthTracker.RecordFailure(provider.Name, resp.StatusCode, errBody)
		r.db.RecordStatAsync(types.StatRecord{
			Plan:        planSlug,
			Provider:    provider.Name,
			Model:       provider.Model,
			KeyMask:     types.MaskAPIKey(provider.APIKey),
			ClientKey:   types.MaskAPIKey(clientKey),
			Source:      source,
			UserAgent:   userAgent,
			Status:      "failure",
			StatusCode:  resp.StatusCode,
			ErrorReason: health.ClassifyFailure(resp.StatusCode, errBody),
			LatencyMs:   latencyMs,
			IsStreaming: isStreaming,
		})
		providerErrors = append(providerErrors, fmt.Sprintf("%s: HTTP %d %s", provider.Name, resp.StatusCode, errBody))
	}

	return nil, types.ProviderConfig{}, fmt.Errorf("all providers failed for plan %s: %s", planSlug, strings.Join(providerErrors, "; "))
}
