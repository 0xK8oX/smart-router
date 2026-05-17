package router

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/providers"
	"smart-router/internal/translation"
	"smart-router/internal/types"
)

type Router struct {
	healthTracker *health.HealthTracker
	db            *db.DB
	client        *providers.Client
}

func New(tracker *health.HealthTracker, database *db.DB) *Router {
	return &Router{
		healthTracker: tracker,
		db:            database,
		client:        providers.NewClient(),
	}
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
func (r *Router) Route(planSlug string, body map[string]interface{}, isStreaming bool) (*http.Response, types.ProviderConfig, error) {
	plan, err := r.db.GetPlan(planSlug)
	if err != nil {
		return nil, types.ProviderConfig{}, fmt.Errorf("load plan: %w", err)
	}

	var providerErrors []string

	for _, provider := range plan.Providers {
		// Check health
		h, err := r.healthTracker.GetHealth(provider.Name)
		if err != nil {
			// If we can't read health, treat as healthy and proceed
			h = types.ProviderHealth{}
		}
		if h.Status == "unhealthy" && h.CooldownUntil > time.Now().Unix() {
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
		translatedBody, err = translation.TranslateRequest(translatedBody, provider.Format)
		if err != nil {
			// Translation error is fatal for this provider, try next
			_ = r.healthTracker.RecordFailure(provider.Name, 0, err.Error())
			providerErrors = append(providerErrors, fmt.Sprintf("%s: translate error", provider.Name))
			continue
		}

		start := time.Now()

		var resp *http.Response
		if isStreaming {
			resp, err = r.client.CallStream(provider, translatedBody)
		} else {
			resp, err = r.client.Call(provider, translatedBody)
		}

		if err != nil {
			// Network / timeout error
			latencyMs := time.Since(start).Milliseconds()
			log.Printf("[ROUTER] FAILURE: plan=%s provider=%s error=%v latency=%dms", planSlug, provider.Name, err, latencyMs)
			_ = r.healthTracker.RecordFailure(provider.Name, 0, err.Error())
			_ = r.db.RecordStat(types.StatRecord{
				Plan:        planSlug,
				Provider:    provider.Name,
				Model:       provider.Model,
				Status:      "failure",
				LatencyMs:   latencyMs,
				IsStreaming: isStreaming,
			})
			providerErrors = append(providerErrors, fmt.Sprintf("%s: %v", provider.Name, err))
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success
			latencyMs := time.Since(start).Milliseconds()
			_ = r.healthTracker.RecordSuccess(provider.Name)
			_ = r.db.RecordStat(types.StatRecord{
				Plan:        planSlug,
				Provider:    provider.Name,
				Model:       provider.Model,
				Status:      "success",
				LatencyMs:   latencyMs,
				IsStreaming: isStreaming,
			})
			return resp, provider, nil
		}

		// Failure: read body, record failure, try next
		var errBody string
		if resp.Body != nil {
			b, _ := io.ReadAll(resp.Body)
			errBody = string(b)
			resp.Body.Close()
		}

		latencyMs := time.Since(start).Milliseconds()
		log.Printf("[ROUTER] FAILURE: plan=%s provider=%s status=%d body=%.200s latency=%dms", planSlug, provider.Name, resp.StatusCode, errBody, latencyMs)
		_ = r.healthTracker.RecordFailure(provider.Name, resp.StatusCode, errBody)
		_ = r.db.RecordStat(types.StatRecord{
			Plan:        planSlug,
			Provider:    provider.Name,
			Model:       provider.Model,
			Status:      "failure",
			LatencyMs:   latencyMs,
			IsStreaming: isStreaming,
		})
		providerErrors = append(providerErrors, fmt.Sprintf("%s: HTTP %d %s", provider.Name, resp.StatusCode, errBody))
	}

	return nil, types.ProviderConfig{}, fmt.Errorf("all providers failed for plan %s: %v", planSlug, providerErrors)
}
