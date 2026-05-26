package router

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/providers"
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

func isVirtualProvider(baseURL string) bool {
	return strings.HasPrefix(baseURL, smartPrefix)
}

func extractPlanFromURL(baseURL string) string {
	return baseURL[len(smartPrefix):]
}

// Route finds a healthy provider, calls it, and returns the response.
func (r *Router) Route(ctx context.Context, planSlug string, body map[string]interface{}, isStreaming bool, clientFormat string, headers http.Header, clientKey string) (*http.Response, types.ProviderConfig, error) {
	return r.routeWithDepth(ctx, planSlug, body, isStreaming, clientFormat, headers, 0, clientKey, nil)
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

func (r *Router) routeWithDepth(ctx context.Context, planSlug string, body map[string]interface{}, isStreaming bool, clientFormat string, headers http.Header, depth int, clientKey string, visited map[string]bool) (*http.Response, types.ProviderConfig, error) {
	plan, err := r.getPlanCached(planSlug)
	if err != nil {
		return nil, types.ProviderConfig{}, fmt.Errorf("load plan: %w", err)
	}
	if plan == nil {
		return nil, types.ProviderConfig{}, fmt.Errorf("plan not found: %s", planSlug)
	}

	requestedModel, _ := body["model"].(string)

	// Build ordered provider list: matching model first, then rest in plan order.
	var orderedProviders []types.ProviderConfig
	var remaining []types.ProviderConfig
	for _, p := range plan.Providers {
		if p.Model == requestedModel {
			orderedProviders = append(orderedProviders, p)
		} else {
			remaining = append(remaining, p)
		}
	}
	// Apply load balancing strategy to non-matching providers.
	remaining = r.applyStrategy(planSlug, plan.Strategy, remaining)
	orderedProviders = append(orderedProviders, remaining...)

	var providerErrors []string
	now := time.Now().Unix()

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
			resp, actualProvider, err := r.routeWithDepth(ctx, targetPlan, body, isStreaming, clientFormat, headers, depth+1, clientKey, visitedCopy)
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

		// Translate request
		translatedBody, err = translation.TranslateRequest(translatedBody, clientFormat, provider.Format)
		if err != nil {
			// Translation error is fatal for this provider, try next
			_ = r.healthTracker.RecordFailure(provider.Name, 0, err.Error())
			r.db.RecordStatAsync(types.StatRecord{
				Plan:      planSlug,
				Provider:  provider.Name,
				Model:     provider.Model,
				KeyMask:   types.MaskAPIKey(provider.APIKey),
				ClientKey: clientKey,
				Status:    "failure",
			})
			providerErrors = append(providerErrors, fmt.Sprintf("%s: translate error", provider.Name))
			continue
		}

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

		if err != nil {
			// Network / timeout error
			latencyMs := time.Since(start).Milliseconds()
			log.Printf("[ROUTER] FAILURE: plan=%s provider=%s error=%v latency=%dms", planSlug, provider.Name, err, latencyMs)
			_ = r.healthTracker.RecordFailure(provider.Name, 0, err.Error())
			r.db.RecordStatAsync(types.StatRecord{
				Plan:        planSlug,
				Provider:    provider.Name,
				Model:       provider.Model,
				KeyMask:     types.MaskAPIKey(provider.APIKey),
				ClientKey:   clientKey,
				Status:      "failure",
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

		latencyMs := time.Since(start).Milliseconds()
		log.Printf("[ROUTER] FAILURE: plan=%s provider=%s status=%d latency=%dms", planSlug, provider.Name, resp.StatusCode, latencyMs)
		_ = r.healthTracker.RecordFailure(provider.Name, resp.StatusCode, errBody)
		r.db.RecordStatAsync(types.StatRecord{
			Plan:        planSlug,
			Provider:    provider.Name,
			Model:       provider.Model,
			KeyMask:     types.MaskAPIKey(provider.APIKey),
			ClientKey:   clientKey,
			Status:      "failure",
			LatencyMs:   latencyMs,
			IsStreaming: isStreaming,
		})
		providerErrors = append(providerErrors, fmt.Sprintf("%s: HTTP %d %s", provider.Name, resp.StatusCode, errBody))
	}

	return nil, types.ProviderConfig{}, fmt.Errorf("all providers failed for plan %s: %s", planSlug, strings.Join(providerErrors, "; "))
}
