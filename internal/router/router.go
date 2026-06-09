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
			// Role + formatting overhead per message
			total += 4
		}
	}

	// Tools definitions
	if tools, ok := normalized["tools"].([]interface{}); ok {
		for _, t := range tools {
			total += tokenizer.CountString(fmt.Sprintf("%v", t))
		}
	}

	// Max tokens reservation (what the client expects to receive back)
	if maxTok, ok := normalized["max_tokens"].(float64); ok {
		total += int(maxTok)
	} else if maxTok, ok := normalized["max_tokens"].(int); ok {
		total += maxTok
	}

	return total
}

// tokenLimitMargin is the safety margin applied to provider context limits.
// A 15% buffer accounts for tokenizer/counting differences between the router
// and upstream providers (especially tool_use/tool_result blocks).
const tokenLimitMargin = 0.85

// providerLimit returns the effective context length limit for a provider,
// with a safety margin applied.
func providerLimit(p types.ProviderConfig) int {
	var limit int
	if p.ContextLength != nil && *p.ContextLength > 0 {
		limit = *p.ContextLength
	} else {
		limit = defaultContextLength
	}
	return int(float64(limit) * tokenLimitMargin)
}

func isVirtualProvider(baseURL string) bool {
	return strings.HasPrefix(baseURL, smartPrefix)
}

func extractPlanFromURL(baseURL string) string {
	return baseURL[len(smartPrefix):]
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
// Factors: latency, in-flight ratio, same-model bonus.
func (r *Router) providerScore(p types.ProviderConfig, requestedModel string) float64 {
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

	// Pre-flight token count — computed once, checked against each provider's limit.
	tokCount := CountRequestTokens(body)

	// Build ordered provider list.
	var orderedProviders []types.ProviderConfig
	if plan.Strategy == "adaptive" {
		orderedProviders = make([]types.ProviderConfig, len(plan.Providers))
		copy(orderedProviders, plan.Providers)
		sort.Slice(orderedProviders, func(i, j int) bool {
			return r.providerScore(orderedProviders[i], requestedModel) <
				r.providerScore(orderedProviders[j], requestedModel)
		})
	} else {
		var remaining []types.ProviderConfig
		for _, p := range plan.Providers {
			if p.Model == requestedModel {
				orderedProviders = append(orderedProviders, p)
			} else {
				remaining = append(remaining, p)
			}
		}
		remaining = r.applyStrategy(planSlug, plan.Strategy, remaining)
		orderedProviders = append(orderedProviders, remaining...)
	}

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

		// Pre-flight token check: skip provider if request exceeds its context limit.
		if limit := providerLimit(provider); tokCount > limit {
			providerErrors = append(providerErrors, fmt.Sprintf("%s: request too large (%d tokens > %d limit)", provider.Name, tokCount, limit))
			continue
		}

		// Override model with provider's configured model
		translatedBody := make(map[string]interface{}, len(body))
		for k, v := range body {
			translatedBody[k] = v
		}
		translatedBody["model"] = provider.Model

		// Translate request
		translatedBody, err = translation.TranslateRequest(translatedBody, clientFormat, provider.Format)
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
		if clientFormat == provider.Format {
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
			resp, err = r.client.CallStream(ctx, provider, translatedBody, fwdHeaders)
		} else {
			resp, err = r.client.Call(ctx, provider, translatedBody, fwdHeaders)
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
			return resp, provider, nil
		}

		// Failure: read body (bounded), record failure, try next
		var errBody string
		if resp.Body != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			errBody = string(b)
			resp.Body.Close()
		}

		// Special case: token-limit 400 — the request is too big for THIS
		// provider but might fit in providers with higher context. Skip
		// the failure stat and try the next provider instead of counting
		// this as a generic failure.
		if resp.StatusCode == 400 && strings.Contains(errBody, "token limit") {
			providerErrors = append(providerErrors, fmt.Sprintf("%s: request too large for provider (token limit)", provider.Name))
			// Don't record this as a provider failure — the provider is fine,
			// the request is just too big for it. Move on.
			continue
		}

		latencyMs = time.Since(start).Milliseconds()
		log.Printf("[ROUTER] FAILURE: plan=%s provider=%s status=%d latency=%dms body=%s", planSlug, provider.Name, resp.StatusCode, latencyMs, errBody)
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
